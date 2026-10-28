package detect

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// composeFiles sao os nomes de compose procurados na raiz, do mais pro menos
// especifico: o de producao descreve como o app roda de verdade.
var composeFiles = []string{
	"docker-compose.producao.yml", "docker-compose.producao.yaml",
	"docker-compose-producao.yml", "docker-compose-producao.yaml",
	"docker-compose.production.yml", "docker-compose.production.yaml",
	"docker-compose-production.yml", "docker-compose-production.yaml",
	"docker-compose.prod.yml", "docker-compose.prod.yaml",
	"docker-compose-prod.yml", "docker-compose-prod.yaml",
	"compose.producao.yml", "compose.producao.yaml",
	"compose.production.yml", "compose.production.yaml",
	"compose.prod.yml", "compose.prod.yaml",
	"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml",
}

// exampleFiles sao os exemplos de variaveis, do de producao pro generico.
var exampleFiles = []string{
	".env.producao.example", ".env.production.example", ".env.prod.example",
	".env.example", ".env.sample", ".env.dist",
}

// manifests sao os arquivos lidos na pasta de build do app.
var manifests = []string{
	"package.json", "prisma/schema.prisma", "requirements.txt", "pyproject.toml",
	"composer.json", "Gemfile", "go.mod", "pom.xml", "build.gradle", "build.gradle.kts",
}

// Build e como a imagem do app e construida, a partir da raiz do repositorio.
type Build struct {
	// Context e a pasta de build ("." e a raiz).
	Context string `json:"context"`
	// Dockerfile e o caminho do Dockerfile a partir da raiz do repositorio.
	Dockerfile string `json:"dockerfile"`
	// Args sao os build args (so valores publicos: ficam gravados na imagem).
	Args map[string]string `json:"args,omitempty"`
}

// composeInfo e o que interessa do compose: o service do app (o que tem
// build), o banco e o resto.
type composeInfo struct {
	file       string
	ctx        string // pasta de build
	dockerfile string // relativo a pasta de build
	args       map[string]string
	command    []string
	port       int
	volumes    []string
	envKeys    []string          // variaveis do environment do app, na ordem
	envValues  map[string]string // valor como esta no compose (pode ter ${})
	envFile    bool              // o app usa env_file: as do exemplo tambem chegam
	refs       map[string]bool   // variaveis usadas em ${} no compose
	dbEngine   string
	dbService  string // nome do service do banco no compose (db, postgres...)
	dbWhy      string
	extensions []string
	notes      []string
}

var safePath = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// cleanRel normaliza um caminho do compose relativo a raiz; "" se ele sai
// do repositorio ou nao e um caminho simples.
func cleanRel(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "."
	}
	c := path.Clean(p)
	if strings.HasPrefix(c, "/") || c == ".." || strings.HasPrefix(c, "../") || !safePath.MatchString(c) {
		return ""
	}
	return c
}

func findCompose(files Files) (string, map[string]any) {
	for _, name := range composeFiles {
		data, ok := files[name]
		if !ok {
			continue
		}
		var doc map[string]any
		if err := yaml.Unmarshal(data, &doc); err == nil && doc["services"] != nil {
			return name, doc
		}
	}
	return "", nil
}

// parseCompose acha o service do app no compose de producao. nil quando o
// repositorio nao tem compose (o app e so o Dockerfile da raiz).
func parseCompose(files Files) *composeInfo {
	name, doc := findCompose(files)
	if doc == nil {
		return nil
	}
	c := &composeInfo{file: name, refs: map[string]bool{}, envValues: map[string]string{}}
	collectRefs(doc, c.refs)
	services, _ := doc["services"].(map[string]any)

	// o app e o service com build; com varios, o de nome mais comum de web
	var builds []string
	for n, s := range services {
		if m, ok := s.(map[string]any); ok && m["build"] != nil {
			builds = append(builds, n)
		}
	}
	sort.Strings(builds)
	if len(builds) == 0 {
		c.notes = append(c.notes, fmt.Sprintf("%s nao tem service com build: o cais publica a imagem construida a partir do repositorio", name))
		return c
	}
	app := builds[0]
	for _, pref := range []string{"web", "app", "frontend", "site", "api", "server"} {
		for _, b := range builds {
			if b == pref {
				app = b
				goto chosen
			}
		}
	}
chosen:
	for _, b := range builds {
		if b != app {
			c.notes = append(c.notes, fmt.Sprintf("o compose tem outro service com build (%s): o cais publica um app por vez; publique %s como outro app se ele rodar na VPS", b, b))
		}
	}
	svc := services[app].(map[string]any)
	c.parseBuild(svc["build"])
	c.parseApp(svc)
	defer c.fixDBHost()

	for n, s := range services {
		if n == app {
			continue
		}
		m, _ := s.(map[string]any)
		img, _ := m["image"].(string)
		switch {
		case m["build"] != nil:
		case isImage(img, "postgis"):
			c.dbEngine, c.extensions, c.dbService = "postgres", []string{"postgis"}, n
			c.dbWhy = fmt.Sprintf("service %s (%s) no %s", n, img, name)
		case isImage(img, "postgres"):
			c.dbEngine, c.dbService, c.dbWhy = "postgres", n, fmt.Sprintf("service %s (%s) no %s", n, img, name)
		case isImage(img, "mysql"), isImage(img, "mariadb"):
			c.dbEngine, c.dbService, c.dbWhy = "mysql", n, fmt.Sprintf("service %s (%s) no %s", n, img, name)
		case fmt.Sprint(m["restart"]) == "no":
			c.notes = append(c.notes, fmt.Sprintf("o service %s roda uma vez e sai (restart: \"no\"): o cais nao tem service de inicializacao; o volume do app ja nasce com o dono que o Dockerfile define -- se %s faz mais que isso, leve pro Dockerfile ou pro inicio do app", n, n))
		default:
			c.notes = append(c.notes, fmt.Sprintf("o service %s (%s) nao entra: o cais publica o app e usa o banco da infra; outro servico (redis, fila...) ainda nao e suportado", n, img))
		}
	}
	return c
}

func isImage(img, name string) bool {
	repo, _, _ := strings.Cut(img, ":")
	return repo == name || strings.HasSuffix(repo, "/"+name)
}

func (c *composeInfo) parseBuild(b any) {
	c.ctx, c.dockerfile = ".", "Dockerfile"
	switch v := b.(type) {
	case string:
		c.ctx = cleanRel(v)
	case map[string]any:
		if s, ok := v["context"].(string); ok {
			c.ctx = cleanRel(s)
		}
		if s, ok := v["dockerfile"].(string); ok {
			c.dockerfile = cleanRel(s)
		}
		c.args = map[string]string{}
		for k, val := range kvList(v["args"]) {
			c.args[k] = val
		}
	}
	if c.ctx == "" || c.dockerfile == "" {
		c.notes = append(c.notes, "o build do app aponta pra fora do repositorio: confira context e dockerfile")
		c.ctx, c.dockerfile = ".", "Dockerfile"
	}
}

func (c *composeInfo) parseApp(svc map[string]any) {
	switch cmd := svc["command"].(type) {
	case string:
		c.command = splitCommand(cmd)
	case []any:
		for _, x := range cmd {
			c.command = append(c.command, fmt.Sprint(x))
		}
	}
	for _, p := range listOfAny(svc["ports"]) {
		s := fmt.Sprint(p)
		if m, ok := p.(map[string]any); ok {
			s = fmt.Sprint(m["target"])
		}
		s = strings.TrimSuffix(strings.TrimSuffix(s, "/tcp"), "/udp")
		target := s[strings.LastIndex(s, ":")+1:]
		var port int
		if _, err := fmt.Sscan(target, &port); err == nil && port > 0 && c.port == 0 {
			c.port = port
		}
	}
	for _, v := range listOfAny(svc["volumes"]) {
		src, dst := "", ""
		switch x := v.(type) {
		case string:
			parts := strings.Split(x, ":")
			if len(parts) >= 2 {
				src, dst = parts[0], parts[1]
			}
		case map[string]any:
			src, _ = x["source"].(string)
			dst, _ = x["target"].(string)
		}
		switch {
		case dst == "" || !strings.HasPrefix(dst, "/"):
		case strings.HasPrefix(src, "."):
			c.notes = append(c.notes, fmt.Sprintf("o volume %s:%s monta codigo do repositorio, que nao existe na VPS (so a imagem): copie essa pasta pra dentro da imagem no Dockerfile", src, dst))
		default:
			c.volumes = append(c.volumes, path.Clean(dst))
		}
	}
	for k, v := range kvList(svc["environment"]) {
		c.envValues[k] = v
	}
	c.envKeys = sortedKeys(c.envValues)
	c.envFile = svc["env_file"] != nil
}

// vars junta as variaveis que chegam no app: as do environment e, com
// env_file, as do exemplo. Variavel que so alimenta ${} de outro lugar do
// compose (DOMINIO, APP_PORTA, POSTGRES_PASSWORD) nao chega como tal.
func (c *composeInfo) vars(example []Var) []Var {
	ex := map[string]string{}
	for _, v := range example {
		ex[v.Name] = v.Example
	}
	var out []Var
	seen := map[string]bool{}
	add := func(name, value string) {
		if seen[name] {
			return
		}
		seen[name] = true
		out = append(out, Var{Name: name, Example: value, Kind: classify(name)})
	}
	for _, k := range c.envKeys {
		add(k, interpolate(c.envValues[k], ex))
	}
	if c.envFile {
		for _, v := range example {
			if c.refs[v.Name] {
				continue
			}
			add(v.Name, v.Example)
		}
	}
	return out
}

func (c *composeInfo) buildArgs(example []Var) map[string]string {
	if len(c.args) == 0 {
		return nil
	}
	ex := map[string]string{}
	for _, v := range example {
		ex[v.Name] = v.Example
	}
	out := map[string]string{}
	for k, v := range c.args {
		out[k] = interpolate(v, ex)
	}
	return out
}

var interp = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?:(:?[-?=])([^}]*))?\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// interpolate resolve ${VAR}, ${VAR:-padrao} e ${VAR:?msg} com os valores
// do exemplo, como o compose faria; $$ vira $.
func interpolate(s string, ex map[string]string) string {
	const dollar = "\x00"
	s = strings.ReplaceAll(s, "$$", dollar)
	s = interp.ReplaceAllStringFunc(s, func(m string) string {
		g := interp.FindStringSubmatch(m)
		name, op, def := g[1], g[2], g[3]
		if name == "" {
			name = g[4]
		}
		if v := ex[name]; v != "" {
			return v
		}
		if strings.HasSuffix(op, "-") || strings.HasSuffix(op, "=") {
			return def
		}
		return ""
	})
	return strings.ReplaceAll(s, dollar, "$")
}

// collectRefs anota as variaveis usadas em ${} em qualquer valor do compose.
func collectRefs(node any, refs map[string]bool) {
	switch n := node.(type) {
	case string:
		for _, g := range interp.FindAllStringSubmatch(strings.ReplaceAll(n, "$$", ""), -1) {
			if g[1] != "" {
				refs[g[1]] = true
			} else {
				refs[g[4]] = true
			}
		}
	case map[string]any:
		for _, v := range n {
			collectRefs(v, refs)
		}
	case []any:
		for _, v := range n {
			collectRefs(v, refs)
		}
	}
}

// kvList le environment/args em mapa ou em lista KEY=VALUE.
func kvList(v any) map[string]string {
	out := map[string]string{}
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if val == nil {
				out[k] = ""
			} else {
				out[k] = fmt.Sprint(val)
			}
		}
	case []any:
		for _, item := range x {
			k, val, _ := strings.Cut(fmt.Sprint(item), "=")
			out[k] = val
		}
	}
	return out
}

func listOfAny(v any) []any {
	if l, ok := v.([]any); ok {
		return l
	}
	return nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// splitCommand separa um command em string como o compose (sem shell):
// por espaco, respeitando aspas.
func splitCommand(s string) []string {
	var out []string
	var cur strings.Builder
	quote := rune(0)
	inWord := false
	for _, r := range s {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote == 0 && (r == '"' || r == '\''):
			quote, inWord = r, true
		case quote == 0 && (r == ' ' || r == '\t' || r == '\n'):
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out
}

// More sao os arquivos que faltam ler depois da primeira leitura (Wanted):
// o Dockerfile e os manifestos da pasta de build, quando o compose diz que
// ela nao e a raiz. Quem le o repositorio le esses tambem e chama Analyze.
func More(files Files) []string {
	c := parseCompose(files)
	if c == nil || c.ctx == "" {
		return nil
	}
	var out []string
	add := func(p string) {
		if p = cleanRel(p); p != "" {
			out = append(out, p)
		}
	}
	add(path.Join(c.ctx, c.dockerfile))
	if c.ctx != "." {
		for _, m := range manifests {
			add(path.Join(c.ctx, m))
		}
	}
	return out
}

// sub e a visao dos arquivos a partir da pasta de build.
func sub(files Files, ctx string) Files {
	if ctx == "." || ctx == "" {
		return files
	}
	out := Files{}
	for k, v := range files {
		if rest, ok := strings.CutPrefix(k, ctx+"/"); ok {
			out[rest] = v
		}
	}
	return out
}

// Collect le os arquivos da analise com read (pasta local, GitHub...):
// primeiro Wanted, depois o que o compose pede da pasta de build (More).
// read devolve ok=false pra arquivo que nao existe.
func Collect(read func(path string) (data []byte, ok bool, err error)) (Files, error) {
	files := Files{}
	get := func(paths []string) error {
		for _, p := range paths {
			if _, done := files[p]; done {
				continue
			}
			data, ok, err := read(p)
			if err != nil {
				return fmt.Errorf("%s: %w", p, err)
			}
			if ok {
				files[p] = data
			}
		}
		return nil
	}
	if err := get(Wanted); err != nil {
		return nil, err
	}
	if err := get(More(files)); err != nil {
		return nil, err
	}
	return files, nil
}

// Allowed diz se um caminho pode fazer parte da analise desses arquivos
// (o daemon confere o que o cliente mandou).
func Allowed(files Files, p string) bool {
	return slices.Contains(Wanted, p) || slices.Contains(More(files), p)
}

// dbHosts e o nome do banco da infra na rede do app, por engine.
var dbHosts = map[string]string{"postgres": "postgres", "mysql": "mysql"}

// fixDBHost troca, no comando do app, o service de banco do compose pelo
// banco da infra (wait-for-it db:3306 -> mysql:3306): senao o app espera
// pra sempre um host que nao existe no cais.
func (c *composeInfo) fixDBHost() {
	host := dbHosts[c.dbEngine]
	if c.dbService == "" || host == "" || c.dbService == host {
		return
	}
	old := regexp.MustCompile(`(^|[^A-Za-z0-9_.-])` + regexp.QuoteMeta(c.dbService) + `:([0-9]+)`)
	changed := false
	for i, a := range c.command {
		if n := old.ReplaceAllString(a, "${1}"+host+":${2}"); n != a {
			c.command[i], changed = n, true
		}
	}
	if changed {
		c.notes = append(c.notes, fmt.Sprintf("o comando do app esperava o banco no host %s: trocado por %s, o banco da infra (se o app so espera o banco subir, da pra tirar a espera: o banco da infra ja esta no ar)", c.dbService, host))
	}
}
