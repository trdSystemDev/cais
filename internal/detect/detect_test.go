package detect

import (
	"slices"
	"strings"
	"testing"
)

func notesHave(r Result, text string) bool {
	for _, n := range r.Notes {
		if strings.Contains(n, text) {
			return true
		}
	}
	return false
}

func TestNodePrismaComDatabaseURL(t *testing.T) {
	r := Analyze(Files{
		"Dockerfile": []byte(`FROM node:22 AS build
EXPOSE 9999
RUN npm ci && npm run build

FROM node:22-slim
WORKDIR /app
COPY --from=build /app/dist ./dist
EXPOSE 3000
HEALTHCHECK --interval=10s CMD node healthcheck.js
VOLUME ["/app/uploads"]
CMD ["node", "dist/server.js"]
`),
		"package.json":         []byte(`{"dependencies": {"@prisma/client": "6.0.0", "express": "5"}}`),
		"prisma/schema.prisma": []byte("datasource db {\n  provider = \"postgresql\"\n  url = env(\"DATABASE_URL\")\n}\n"),
		".env.example":         []byte("NODE_ENV=production\nPORT=3000\nDATABASE_URL=\"postgresql://user:pass@localhost:5432/app\"\nJWT_SECRET=troque-isso\nSTRIPE_API_KEY=sk_test_123\n"),
	})
	if r.Port != 3000 || r.PortSource != "EXPOSE no Dockerfile" {
		t.Errorf("porta %d (%s); o EXPOSE do estagio de build nao conta", r.Port, r.PortSource)
	}
	if !r.Healthcheck || len(r.Volumes) != 1 || r.Volumes[0] != "/app/uploads" {
		t.Errorf("healthcheck %v volumes %v", r.Healthcheck, r.Volumes)
	}
	if r.Language != "node" || r.Database == nil || r.Database.Engine != "postgres" {
		t.Fatalf("linguagem %q banco %+v", r.Language, r.Database)
	}
	if r.Database.Env["DATABASE_URL_FILE"] != "url_file" || len(r.Database.Env) != 1 {
		t.Errorf("env do banco %v", r.Database.Env)
	}
	if !notesHave(r, "process.env.DATABASE_URL ??= require('fs').readFileSync(process.env.DATABASE_URL_FILE") {
		t.Errorf("faltou o trecho de codigo pro node:\n%s", strings.Join(r.Notes, "\n"))
	}
	kinds := map[string]string{}
	for _, v := range r.Vars {
		kinds[v.Name] = v.Kind
		if v.Kind != VarPlain && v.Example != "" {
			t.Errorf("guardou o exemplo de %s (%s)", v.Name, v.Kind)
		}
	}
	want := map[string]string{"NODE_ENV": VarPlain, "PORT": VarPlain, "DATABASE_URL": VarDatabase, "JWT_SECRET": VarSecret, "STRIPE_API_KEY": VarSecret}
	for k, v := range want {
		if kinds[k] != v {
			t.Errorf("%s = %q, esperava %q", k, kinds[k], v)
		}
	}
	if !r.Supported() {
		t.Error("app completo devia ser publicavel sem perguntas")
	}
}

func TestLaravelComVariaveisSeparadas(t *testing.T) {
	r := Analyze(Files{
		"Dockerfile":    []byte("FROM php:8.4-fpm\nEXPOSE 8000\n"),
		"composer.json": []byte(`{"require": {"laravel/framework": "^12.0"}}`),
		".env.example":  []byte("APP_KEY=\nDB_CONNECTION=mysql\nDB_HOST=127.0.0.1\nDB_PORT=3306\nDB_DATABASE=laravel\nDB_USERNAME=root\nDB_PASSWORD=\n"),
	})
	if r.Database == nil || r.Database.Engine != "mysql" {
		t.Fatalf("banco %+v", r.Database)
	}
	want := map[string]string{"DB_HOST": "host", "DB_PORT": "port", "DB_DATABASE": "name", "DB_USERNAME": "user", "DB_PASSWORD_FILE": "password_file"}
	for k, v := range want {
		if r.Database.Env[k] != v {
			t.Errorf("%s = %q, esperava %q (env %v)", k, r.Database.Env[k], v, r.Database.Env)
		}
	}
	if _, ok := r.Database.Env["DB_PASSWORD"]; ok {
		t.Error("senha mapeada em variavel")
	}
	if !notesHave(r, "file_get_contents(env('DB_PASSWORD_FILE'))") {
		t.Errorf("faltou o trecho pro php:\n%s", strings.Join(r.Notes, "\n"))
	}
	if !notesHave(r, "APP_KEY parece segredo") {
		t.Error("APP_KEY devia virar arquivo de secret")
	}
	if !notesHave(r, "HEALTHCHECK") {
		t.Error("devia avisar que nao ha HEALTHCHECK")
	}
}

func TestDriverSemEnvUsaPadraoDoCais(t *testing.T) {
	r := Analyze(Files{
		"Dockerfile": []byte("FROM golang:1.25 AS b\nRUN go build -o /app\nFROM gcr.io/distroless/static\nCOPY --from=b /app /app\nEXPOSE 8080/tcp\n"),
		"go.mod":     []byte("module x\nrequire github.com/jackc/pgx/v5 v5.7.0\n"),
	})
	if r.Port != 8080 || r.Database == nil || r.Database.Engine != "postgres" || r.Database.Env["DB_PASSWORD_FILE"] != "password_file" {
		t.Fatalf("porta %d banco %+v", r.Port, r.Database)
	}
}

func TestDjangoComPsycopg(t *testing.T) {
	r := Analyze(Files{
		"Dockerfile":       []byte("FROM python:3.13\nEXPOSE ${PORT}\n"),
		"requirements.txt": []byte("Django==5.2\npsycopg[binary]==3.2\n"),
		".env.example":     []byte("PORT=8000\nSECRET_KEY=x\n"),
	})
	if r.Port != 8000 || r.PortSource != "PORT no exemplo de variaveis" {
		t.Errorf("porta %d (%s)", r.Port, r.PortSource)
	}
	if !notesHave(r, "EXPOSE ${PORT} usa variavel") {
		t.Error("devia avisar do EXPOSE com variavel")
	}
	if r.Database == nil || r.Database.Engine != "postgres" || r.Language != "python" {
		t.Fatalf("banco %+v linguagem %s", r.Database, r.Language)
	}
}

func TestCasosQueOImportPrecisaPerguntar(t *testing.T) {
	semDockerfile := Analyze(Files{"package.json": []byte("{}")})
	if semDockerfile.Supported() || !notesHave(semDockerfile, "nao achei o Dockerfile") {
		t.Error("sem Dockerfile tem que avisar")
	}
	semPorta := Analyze(Files{"Dockerfile": []byte("FROM nginx\n")})
	if semPorta.Supported() || !notesHave(semPorta, "nao achei a porta") {
		t.Error("sem porta tem que perguntar")
	}
	doisBancos := Analyze(Files{
		"Dockerfile":   []byte("FROM node\nEXPOSE 3000\n"),
		"package.json": []byte(`{"dependencies": {"pg": "8", "mysql2": "3"}}`),
	})
	if doisBancos.Supported() || doisBancos.Database == nil || doisBancos.Database.Engine != "" {
		t.Errorf("dois drivers tem que perguntar o banco: %+v", doisBancos.Database)
	}
	semBanco := Analyze(Files{"Dockerfile": []byte("FROM nginx\nEXPOSE 80\nHEALTHCHECK NONE\n")})
	if semBanco.Database != nil || semBanco.Healthcheck {
		t.Errorf("banco %+v healthcheck %v (NONE desliga)", semBanco.Database, semBanco.Healthcheck)
	}
}

func TestDockerfileComLinhasContinuadasEComentarios(t *testing.T) {
	r := Analyze(Files{"Dockerfile": []byte("FROM alpine\n# EXPOSE 1111\nEXPOSE \\\n  4000 \\\n  4001/udp\nHEALTHCHECK --interval=5s \\\n  CMD wget -q localhost:4000\nVOLUME /data /cache\n")})
	if r.Port != 4000 || !r.Healthcheck || strings.Join(r.Volumes, ",") != "/data,/cache" {
		t.Fatalf("porta %d healthcheck %v volumes %v", r.Port, r.Healthcheck, r.Volumes)
	}
}

func TestLooksSecret(t *testing.T) {
	for _, n := range []string{"JWT_SECRET", "DB_PASSWORD", "API_KEY", "GITHUB_TOKEN", "SENTRY_DSN", "DATABASE_URL", "MONGO_URI", "app_key",
		"ADMIN_SENHA", "SENHA_SMTP", "CHAVE_API", "SEGREDO_JWT", "MYSQL_PWD"} {
		if !LooksSecret(n) {
			t.Errorf("%s devia parecer segredo", n)
		}
	}
	for _, n := range []string{"NODE_ENV", "APP_URL", "PORT", "LOG_LEVEL", "DB_HOST"} {
		if LooksSecret(n) {
			t.Errorf("%s nao e segredo", n)
		}
	}
}

// a estrutura do TRDSmartPodCast (valores inventados): app em app/, banco
// no compose de producao, env_file com o exemplo de producao
func TestComposeDeProducaoComAppEmSubpasta(t *testing.T) {
	r := Analyze(Files{
		"docker-compose.yml": []byte("services:\n  app:\n    build: ./app\n"),
		"docker-compose.producao.yml": []byte(`name: studio
x-logs: &logs
  driver: json-file
services:
  permissoes:
    image: alpine:3.22
    command: ["sh", "-c", "chown -R 1001:1001 /data"]
    volumes: ["${DADOS_DIR:-/data/x}/app:/data"]
    restart: "no"
  app:
    build:
      context: ./app
    restart: unless-stopped
    env_file: .env.producao
    environment:
      NODE_ENV: production
      AUTH_URL: https://${DOMINIO:?defina}
      DATABASE_URL: postgres://${POSTGRES_USER:-u}:${POSTGRES_PASSWORD:?x}@postgres:5432/${POSTGRES_DB:-d}
      WORKER_DIR: /worker/musetalk
    ports:
      - "127.0.0.1:${APP_PORTA:-3220}:3000"
    volumes:
      - ${DADOS_DIR:-/data/x}/app:/data
      - ./worker:/worker:ro
    logging: *logs
  postgres:
    image: postgres:16
    environment:
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD:?x}
`),
		".env.example":          []byte("AUTH_SECRET=dev\n"),
		".env.producao.example": []byte("DOMINIO=studio.exemplo.com.br\nAPP_PORTA=3220\nDADOS_DIR=/data/x\nPOSTGRES_USER=u\nPOSTGRES_PASSWORD=\nPOSTGRES_DB=d\nAUTH_SECRET=\nADMIN_SENHA=\nLIVEKIT_URL=\nLIVEKIT_API_KEY=\nGERACAO_DISCO_GB=80\n"),
		"app/Dockerfile":        []byte("FROM node:24 AS b\nFROM node:24-slim\nVOLUME [\"/data\"]\nEXPOSE 3000\nHEALTHCHECK CMD node -e 1\n"),
		"app/package.json":      []byte(`{"dependencies": {"next": "15", "postgres": "3"}}`),
	})
	if r.ComposeFile != "docker-compose.producao.yml" {
		t.Fatalf("compose escolhido %q (o de producao tem prioridade)", r.ComposeFile)
	}
	if r.Build.Context != "app" || r.Build.Dockerfile != "app/Dockerfile" {
		t.Errorf("build %+v", r.Build)
	}
	if !r.Dockerfile || !r.Healthcheck || r.Port != 3000 || r.Language != "node" {
		t.Errorf("dockerfile %v healthcheck %v porta %d linguagem %q", r.Dockerfile, r.Healthcheck, r.Port, r.Language)
	}
	if len(r.Volumes) != 1 || r.Volumes[0] != "/data" {
		t.Errorf("volumes %v", r.Volumes)
	}
	if r.Database == nil || r.Database.Engine != "postgres" || r.Database.Env["DATABASE_URL_FILE"] != "url_file" {
		t.Fatalf("banco %+v", r.Database)
	}
	vars := map[string]Var{}
	for _, v := range r.Vars {
		vars[v.Name] = v
	}
	// AUTH_URL sai resolvida com o DOMINIO do exemplo
	if vars["AUTH_URL"].Example != "https://studio.exemplo.com.br" || vars["NODE_ENV"].Example != "production" {
		t.Errorf("AUTH_URL %q NODE_ENV %q", vars["AUTH_URL"].Example, vars["NODE_ENV"].Example)
	}
	// do env_file chegam as variaveis do app; as que so alimentam o compose nao
	for _, must := range []string{"AUTH_SECRET", "ADMIN_SENHA", "LIVEKIT_URL", "LIVEKIT_API_KEY", "GERACAO_DISCO_GB"} {
		if _, ok := vars[must]; !ok {
			t.Errorf("faltou %s", must)
		}
	}
	for _, not := range []string{"DOMINIO", "APP_PORTA", "DADOS_DIR", "POSTGRES_PASSWORD", "POSTGRES_USER"} {
		if _, ok := vars[not]; ok {
			t.Errorf("%s so alimenta o compose e nao devia chegar no app", not)
		}
	}
	if vars["ADMIN_SENHA"].Kind != VarSecret || vars["AUTH_SECRET"].Kind != VarSecret {
		t.Error("segredos do env_file nao reconhecidos")
	}
	for _, want := range []string{"./worker:/worker monta codigo do repositorio", "service permissoes roda uma vez", "service postgres (postgres:16)"} {
		if !notesHave(r, want) {
			t.Errorf("faltou o aviso %q:\n%s", want, strings.Join(r.Notes, "\n"))
		}
	}
	more := More(Files{"docker-compose.producao.yml": []byte("services:\n  app:\n    build:\n      context: ./app\n")})
	if len(more) == 0 || more[0] != "app/Dockerfile" || !slices.Contains(more, "app/package.json") {
		t.Errorf("More %v", more)
	}
}

// a estrutura do portal-web (valores inventados): sem .env.example, PostGIS,
// build arg publico e command trocado no compose
func TestComposeDeProducaoComPostGISEBuildArg(t *testing.T) {
	r := Analyze(Files{
		"Dockerfile":   []byte("FROM node:20-alpine\nEXPOSE 3000\nCMD [\"node\", \"server.js\"]\n"),
		"package.json": []byte(`{"dependencies": {"pg": "8", "next": "16"}}`),
		"docker-compose-prod.yml": []byte(`services:
  db:
    image: postgis/postgis:17-3.5-alpine
    environment:
      POSTGRES_PASSWORD: senha-inventada
  web:
    build:
      context: .
      dockerfile: Dockerfile
      args:
        - NEXT_PUBLIC_MAPBOX_ACCESS_TOKEN=${NEXT_PUBLIC_MAPBOX_ACCESS_TOKEN}
    ports:
      - "4036:3000"
    environment:
      - DATABASE_URL=${DATABASE_URL}
      - JWT_ACCESS_SECRET=${JWT_ACCESS_SECRET}
      - R2_ACCESS_KEY_ID=${R2_ACCESS_KEY_ID}
      - R2_SECRET_ACCESS_KEY=${R2_SECRET_ACCESS_KEY}
      - R2_BUCKET_NAME=${R2_BUCKET_NAME}
      - NEXT_PUBLIC_R2_PUBLIC_DOMAIN=${NEXT_PUBLIC_R2_PUBLIC_DOMAIN}
      - SMTP_PASS=${SMTP_PASS}
      - SMTP_HOST=${SMTP_HOST}
    command: ["sh", "-c", "pnpm run start:prod"]
`),
	})
	if r.Build.Context != "." || r.Build.Dockerfile != "Dockerfile" || r.Port != 3000 {
		t.Errorf("build %+v porta %d", r.Build, r.Port)
	}
	if _, ok := r.Build.Args["NEXT_PUBLIC_MAPBOX_ACCESS_TOKEN"]; !ok || notesHave(r, "build arg NEXT_PUBLIC_MAPBOX_ACCESS_TOKEN parece segredo") {
		t.Errorf("build arg publico: %v\n%s", r.Build.Args, strings.Join(r.Notes, "\n"))
	}
	if strings.Join(r.Command, " ") != "sh -c pnpm run start:prod" {
		t.Errorf("command %q", r.Command)
	}
	if r.Database == nil || r.Database.Engine != "postgres" || strings.Join(r.Database.Extensions, ",") != "postgis" {
		t.Fatalf("banco %+v", r.Database)
	}
	kinds := map[string]string{}
	for _, v := range r.Vars {
		kinds[v.Name] = v.Kind
	}
	want := map[string]string{"DATABASE_URL": VarDatabase, "JWT_ACCESS_SECRET": VarSecret, "R2_SECRET_ACCESS_KEY": VarSecret,
		"SMTP_PASS": VarSecret, "R2_BUCKET_NAME": VarPlain, "SMTP_HOST": VarPlain, "NEXT_PUBLIC_R2_PUBLIC_DOMAIN": VarPlain}
	for k, v := range want {
		if kinds[k] != v {
			t.Errorf("%s = %q, esperava %q", k, kinds[k], v)
		}
	}
	if !notesHave(r, "extensao postgis") {
		t.Error("faltou o aviso da extensao")
	}
}

func TestPublicoNaoESegredo(t *testing.T) {
	if LooksSecret("NEXT_PUBLIC_MAPBOX_ACCESS_TOKEN") || LooksSecret("VITE_API_KEY") {
		t.Error("variavel publica tratada como segredo")
	}
	if !LooksSecret("MAPBOX_SECRET_TOKEN") {
		t.Error("segredo sem prefixo publico nao reconhecido")
	}
}

func TestInterpolate(t *testing.T) {
	ex := map[string]string{"A": "1", "VAZIO": ""}
	cases := map[string]string{
		"${A}":              "1",
		"x-${A}-y":          "x-1-y",
		"${B:-padrao}":      "padrao",
		"${VAZIO:-padrao}":  "padrao",
		"${B:?mensagem}":    "",
		"$A":                "1",
		"$${A}":             "${A}",
		"https://${A}/path": "https://1/path",
	}
	for in, want := range cases {
		if got := interpolate(in, ex); got != want {
			t.Errorf("interpolate(%q) = %q, esperava %q", in, got, want)
		}
	}
}

// a estrutura do Estoque (valores inventados): espera o banco pelo host do
// compose, que no cais e o MySQL da infra
func TestComandoQueEsperaOBancoDoCompose(t *testing.T) {
	r := Analyze(Files{
		"Dockerfile": []byte("FROM node:22\nEXPOSE 3000\n"),
		"docker-compose.prod.yaml": []byte(`services:
  app:
    build: .
    ports: ["3000:3000"]
    environment:
      - DATABASE_URL=${DATABASE_URL}
      - MERCADO_PAGO_PUBLIC_KEY=${MERCADO_PAGO_PUBLIC_KEY}
      - MERCADO_PAGO_ACCESS_TOKEN=${MERCADO_PAGO_ACCESS_TOKEN}
    command: sh -c "/app/wait-for-it.sh db:3306 -- pnpm db:push && pnpm start"
  db:
    image: mysql:latest
`),
	})
	if got := strings.Join(r.Command, "|"); got != "sh|-c|/app/wait-for-it.sh mysql:3306 -- pnpm db:push && pnpm start" {
		t.Errorf("comando %q", got)
	}
	if !notesHave(r, "esperava o banco no host db") {
		t.Errorf("faltou o aviso da troca do host:\n%s", strings.Join(r.Notes, "\n"))
	}
	kinds := map[string]string{}
	for _, v := range r.Vars {
		kinds[v.Name] = v.Kind
	}
	if kinds["MERCADO_PAGO_PUBLIC_KEY"] != VarPlain || kinds["MERCADO_PAGO_ACCESS_TOKEN"] != VarSecret {
		t.Errorf("chave publica / token: %v", kinds)
	}
}
