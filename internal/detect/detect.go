package detect

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Files sao os arquivos do repositorio que a analise le (caminho relativo a
// raiz -> conteudo). Quem chama busca so os de Wanted: nada do resto do
// repositorio e lido.
type Files map[string][]byte

// Wanted sao os caminhos que interessam a analise.
var Wanted = []string{
	"Dockerfile", ".env.example", ".env.sample", ".env.dist",
	"package.json", "prisma/schema.prisma",
	"requirements.txt", "pyproject.toml",
	"composer.json", "Gemfile", "go.mod", "pom.xml", "build.gradle", "build.gradle.kts",
}

// Var e uma variavel de ambiente que o app espera (do .env.example).
type Var struct {
	Name    string `json:"name"`
	Example string `json:"example,omitempty"`
	Kind    string `json:"kind"`
}

// Tipos de variavel.
const (
	// VarPlain e configuracao comum (NODE_ENV, APP_URL): vai no ambiente.
	VarPlain = "plain"
	// VarSecret parece segredo (TOKEN, SECRET, KEY...): vira arquivo em
	// /run/secrets, nunca variavel.
	VarSecret = "secret"
	// VarDatabase e conexao de banco: o cais preenche (host, usuario, nome)
	// ou monta como arquivo (senha, URL).
	VarDatabase = "database"
)

// Database e o banco que o app parece usar.
type Database struct {
	Engine string `json:"engine"`
	// Why diz de onde veio a conclusao (ex: "dependencia pg no package.json").
	Why string `json:"why"`
	// Env e o mapeamento sugerido VAR -> host|port|name|user|password_file|url_file.
	Env map[string]string `json:"env"`
}

// Result e o que a analise descobriu.
type Result struct {
	Dockerfile bool   `json:"dockerfile"`
	Language   string `json:"language,omitempty"`
	// Port e a porta HTTP do app; 0 quando nao deu pra descobrir.
	Port       int    `json:"port,omitempty"`
	PortSource string `json:"port_source,omitempty"`
	// Healthcheck indica HEALTHCHECK no Dockerfile: o deploy espera ficar
	// healthy. Sem ele, "rodando" ja conta como pronto.
	Healthcheck bool `json:"healthcheck"`
	// Volumes sao as pastas do VOLUME do Dockerfile (dados persistentes).
	Volumes  []string  `json:"volumes,omitempty"`
	Database *Database `json:"database,omitempty"`
	Vars     []Var     `json:"vars,omitempty"`
	// Notes sao avisos e o que o app precisa mudar, em texto pra mostrar.
	Notes []string `json:"notes,omitempty"`
}

// Analyze le os arquivos do repositorio e descreve como publicar o app.
// Nunca falha: o que nao der pra descobrir vira nota.
func Analyze(files Files) Result {
	var r Result
	if df, ok := files["Dockerfile"]; ok {
		r.Dockerfile = true
		dockerfile(df, &r)
	} else {
		r.Notes = append(r.Notes, "o repositorio nao tem Dockerfile na raiz: o cais publica a imagem construida a partir dele")
	}
	r.Language = language(files)
	r.Vars = envVars(files)

	if r.Port == 0 {
		for _, v := range r.Vars {
			if v.Name == "PORT" {
				if p, err := strconv.Atoi(v.Example); err == nil && p > 0 && p < 65536 {
					r.Port, r.PortSource = p, "PORT no .env.example"
				}
			}
		}
	}
	if r.Port == 0 {
		r.Notes = append(r.Notes, "nao achei a porta do app (EXPOSE no Dockerfile ou PORT no .env.example): informe no import")
	}
	if r.Dockerfile && !r.Healthcheck {
		r.Notes = append(r.Notes, "o Dockerfile nao tem HEALTHCHECK: o deploy considera o app pronto assim que o container sobe; com HEALTHCHECK ele espera o app responder e volta a versao anterior se nao responder")
	}

	r.Database = database(files, r.Vars)
	// exemplo de segredo ou de conexao nao serve pra nada depois da
	// analise e as vezes e real (senha de desenvolvimento commitada)
	for i, v := range r.Vars {
		if v.Kind != VarPlain {
			r.Vars[i].Example = ""
		}
	}
	if r.Database != nil {
		r.Notes = append(r.Notes, databaseNotes(r)...)
	}
	for _, v := range r.Vars {
		if v.Kind == VarSecret {
			r.Notes = append(r.Notes, fmt.Sprintf("%s parece segredo: vai como arquivo /run/secrets/%s (nunca em variavel); o app precisa ler o arquivo", v.Name, strings.ToLower(v.Name)))
		}
	}
	return r
}

// dockerfile le EXPOSE, HEALTHCHECK e VOLUME do ultimo estagio (os
// anteriores so constroem: nao valem pra imagem final).
func dockerfile(data []byte, r *Result) {
	var expose []string
	r.Healthcheck = false
	r.Volumes = nil
	for _, ins := range instructions(data) {
		cmd, args, _ := strings.Cut(ins, " ")
		args = strings.TrimSpace(args)
		switch strings.ToUpper(cmd) {
		case "FROM":
			expose, r.Healthcheck, r.Volumes = nil, false, nil
		case "EXPOSE":
			expose = append(expose, strings.Fields(args)...)
		case "HEALTHCHECK":
			r.Healthcheck = !strings.EqualFold(strings.TrimSpace(args), "NONE")
		case "VOLUME":
			r.Volumes = append(r.Volumes, jsonOrFields(args)...)
		}
	}
	for _, e := range expose {
		port, proto, _ := strings.Cut(e, "/")
		if proto != "" && proto != "tcp" {
			continue
		}
		p, err := strconv.Atoi(port)
		if err != nil {
			r.Notes = append(r.Notes, fmt.Sprintf("EXPOSE %s usa variavel: confirme a porta no import", e))
			continue
		}
		if r.Port == 0 {
			r.Port, r.PortSource = p, "EXPOSE no Dockerfile"
		} else if p != r.Port {
			r.Notes = append(r.Notes, fmt.Sprintf("o Dockerfile expoe mais de uma porta; usando %d (a primeira)", r.Port))
		}
	}
}

// instructions junta as linhas continuadas com \ e tira comentarios.
func instructions(data []byte) []string {
	var out []string
	var cur strings.Builder
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasSuffix(line, `\`) {
			cur.WriteString(strings.TrimSuffix(line, `\`) + " ")
			continue
		}
		cur.WriteString(line)
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, s)
		}
		cur.Reset()
	}
	return out
}

func jsonOrFields(s string) []string {
	var list []string
	if strings.HasPrefix(s, "[") && json.Unmarshal([]byte(s), &list) == nil {
		return list
	}
	return strings.Fields(s)
}

func language(files Files) string {
	switch {
	case has(files, "package.json"):
		return "node"
	case has(files, "requirements.txt", "pyproject.toml"):
		return "python"
	case has(files, "composer.json"):
		return "php"
	case has(files, "Gemfile"):
		return "ruby"
	case has(files, "go.mod"):
		return "go"
	case has(files, "pom.xml", "build.gradle", "build.gradle.kts"):
		return "java"
	}
	return ""
}

func has(files Files, names ...string) bool {
	for _, n := range names {
		if _, ok := files[n]; ok {
			return true
		}
	}
	return false
}

var (
	dbVar     = regexp.MustCompile(`^(DATABASE_|DB_|POSTGRES_|PG|MYSQL_)|^DATABASE$`)
	secretVar = regexp.MustCompile(`SECRET|PASSWORD|PASSWD|PASS$|TOKEN|PRIVATE|CREDENTIAL|_KEY$|^KEY$|APIKEY|API_KEY|DSN$`)
)

// envVars le o .env.example (ou .env.sample/.env.dist) e classifica cada
// variavel.
func envVars(files Files) []Var {
	var data []byte
	for _, name := range []string{".env.example", ".env.sample", ".env.dist"} {
		if d, ok := files[name]; ok {
			data = d
			break
		}
	}
	var out []Var
	seen := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		name, val, ok := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" || seen[name] {
			continue
		}
		seen[name] = true
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		v := Var{Name: name, Example: val, Kind: VarPlain}
		switch {
		case dbVar.MatchString(name):
			v.Kind = VarDatabase
		case secretVar.MatchString(name):
			v.Kind = VarSecret
		}
		out = append(out, v)
	}
	return out
}

// sinais de banco nos manifestos: trecho procurado -> engine
var dbSignals = []struct {
	file, needle, engine, why string
}{
	{"prisma/schema.prisma", `provider = "postgresql"`, "postgres", "provider postgresql no schema.prisma"},
	{"prisma/schema.prisma", `provider = "mysql"`, "mysql", "provider mysql no schema.prisma"},
	{"package.json", `"pg"`, "postgres", "dependencia pg no package.json"},
	{"package.json", `"postgres"`, "postgres", "dependencia postgres no package.json"},
	{"package.json", `"mysql2"`, "mysql", "dependencia mysql2 no package.json"},
	{"package.json", `"mysql"`, "mysql", "dependencia mysql no package.json"},
	{"requirements.txt", "psycopg", "postgres", "psycopg no requirements.txt"},
	{"requirements.txt", "asyncpg", "postgres", "asyncpg no requirements.txt"},
	{"requirements.txt", "mysqlclient", "mysql", "mysqlclient no requirements.txt"},
	{"requirements.txt", "pymysql", "mysql", "pymysql no requirements.txt"},
	{"pyproject.toml", "psycopg", "postgres", "psycopg no pyproject.toml"},
	{"pyproject.toml", "asyncpg", "postgres", "asyncpg no pyproject.toml"},
	{"pyproject.toml", "mysqlclient", "mysql", "mysqlclient no pyproject.toml"},
	{"pyproject.toml", "pymysql", "mysql", "pymysql no pyproject.toml"},
	{"Gemfile", `gem "pg"`, "postgres", "gem pg no Gemfile"},
	{"Gemfile", `gem 'pg'`, "postgres", "gem pg no Gemfile"},
	{"Gemfile", "mysql2", "mysql", "gem mysql2 no Gemfile"},
	{"go.mod", "github.com/lib/pq", "postgres", "lib/pq no go.mod"},
	{"go.mod", "github.com/jackc/pgx", "postgres", "pgx no go.mod"},
	{"go.mod", "github.com/go-sql-driver/mysql", "mysql", "go-sql-driver/mysql no go.mod"},
	{"pom.xml", "postgresql", "postgres", "driver postgresql no pom.xml"},
	{"pom.xml", "mysql-connector", "mysql", "mysql-connector no pom.xml"},
	{"build.gradle", "postgresql", "postgres", "driver postgresql no build.gradle"},
	{"build.gradle", "mysql-connector", "mysql", "mysql-connector no build.gradle"},
	{"build.gradle.kts", "postgresql", "postgres", "driver postgresql no build.gradle.kts"},
	{"build.gradle.kts", "mysql-connector", "mysql", "mysql-connector no build.gradle.kts"},
}

// database descobre o banco: primeiro o .env.example (o que o app de fato
// configura), depois os drivers nos manifestos.
func database(files Files, vars []Var) *Database {
	var engine, why string
	for _, v := range vars {
		ex := strings.ToLower(v.Example)
		switch {
		case v.Name == "DB_CONNECTION" && (ex == "pgsql" || ex == "postgres" || ex == "postgresql"):
			engine, why = "postgres", "DB_CONNECTION="+v.Example+" no .env.example"
		case v.Name == "DB_CONNECTION" && ex == "mysql":
			engine, why = "mysql", "DB_CONNECTION=mysql no .env.example"
		case v.Kind == VarDatabase && (strings.HasPrefix(ex, "postgres://") || strings.HasPrefix(ex, "postgresql://")):
			engine, why = "postgres", v.Name+" com postgres:// no .env.example"
		case v.Kind == VarDatabase && strings.HasPrefix(ex, "mysql://"):
			engine, why = "mysql", v.Name+" com mysql:// no .env.example"
		}
		if engine != "" {
			break
		}
	}
	if engine == "" {
		found := map[string]string{}
		for _, s := range dbSignals {
			if data, ok := files[s.file]; ok && bytes.Contains(data, []byte(s.needle)) {
				if _, dup := found[s.engine]; !dup {
					found[s.engine] = s.why
				}
			}
		}
		if len(found) > 1 {
			return &Database{Engine: "", Why: "o app tem driver de Postgres e de MySQL: escolha no import"}
		}
		for e, w := range found {
			engine, why = e, w
		}
	}
	if engine == "" {
		return nil
	}
	return &Database{Engine: engine, Why: why, Env: dbEnv(vars)}
}

// dbEnv sugere o mapeamento a partir das variaveis de banco do
// .env.example. Senha e URL viram *_FILE (o app le o arquivo). Sem
// variaveis de banco no exemplo, usa o padrao do cais.
func dbEnv(vars []Var) map[string]string {
	env := map[string]string{}
	for _, v := range vars {
		if v.Kind != VarDatabase || v.Name == "DB_CONNECTION" {
			continue
		}
		n := v.Name
		switch {
		case n == "DATABASE_URL" || strings.HasSuffix(n, "_URL") || strings.HasSuffix(n, "_URI"):
			env[n+"_FILE"] = "url_file"
		case strings.HasSuffix(n, "PASSWORD") || strings.HasSuffix(n, "_PASS"):
			env[n+"_FILE"] = "password_file"
		case strings.HasSuffix(n, "HOST"):
			env[n] = "host"
		case strings.HasSuffix(n, "PORT"):
			env[n] = "port"
		case strings.HasSuffix(n, "USER") || strings.HasSuffix(n, "USERNAME"):
			env[n] = "user"
		case strings.HasSuffix(n, "DATABASE") || strings.HasSuffix(n, "_NAME") || strings.HasSuffix(n, "_DB") || n == "PGDATABASE":
			env[n] = "name"
		}
	}
	if len(env) == 0 {
		return map[string]string{
			"DB_HOST": "host", "DB_PORT": "port", "DB_NAME": "name", "DB_USER": "user",
			"DB_PASSWORD_FILE": "password_file",
		}
	}
	return env
}

// databaseNotes explica o que o app precisa mudar pra ler senha/URL de
// arquivo, com o trecho de codigo da linguagem.
func databaseNotes(r Result) []string {
	db := r.Database
	if db.Engine == "" {
		return []string{db.Why}
	}
	notes := []string{fmt.Sprintf("banco %s detectado (%s): o cais cria database e usuario na infra; o app nao declara banco no compose", db.Engine, db.Why)}
	var files []string
	for k, v := range db.Env {
		if v == "url_file" || v == "password_file" {
			files = append(files, k)
		}
	}
	sort.Strings(files)
	for _, f := range files {
		orig := strings.TrimSuffix(f, "_FILE")
		notes = append(notes, fmt.Sprintf("%s nao vem em variavel (senha nunca vai no ambiente): o cais monta %s com o caminho do arquivo; %s",
			orig, f, snippet(r.Language, orig, f)))
	}
	return notes
}

// snippet e o trecho que faz o app ler o arquivo, por linguagem.
func snippet(lang, name, fileVar string) string {
	switch lang {
	case "node":
		return fmt.Sprintf("no inicio do app: process.env.%s ??= require('fs').readFileSync(process.env.%s, 'utf8').trim()", name, fileVar)
	case "python":
		return fmt.Sprintf("no settings: os.environ.setdefault('%s', open(os.environ['%s']).read().strip())", name, fileVar)
	case "php":
		return fmt.Sprintf("no config: env('%s') ?: trim(file_get_contents(env('%s')))", name, fileVar)
	case "ruby":
		return fmt.Sprintf("no config: ENV['%s'] ||= File.read(ENV['%s']).strip", name, fileVar)
	}
	return fmt.Sprintf(`ou num entrypoint: export %s="$(cat "$%s")"; exec "$@"`, name, fileVar)
}

// Supported diz se a analise conseguiu o minimo pra publicar sem perguntar.
func (r Result) Supported() bool {
	return r.Dockerfile && r.Port > 0 && (r.Database == nil || r.Database.Engine != "")
}
