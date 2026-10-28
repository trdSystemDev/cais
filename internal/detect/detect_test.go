package detect

import (
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
	if r.Port != 8000 || r.PortSource != "PORT no .env.example" {
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
	if semDockerfile.Supported() || !notesHave(semDockerfile, "nao tem Dockerfile") {
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
	for _, n := range []string{"JWT_SECRET", "DB_PASSWORD", "API_KEY", "GITHUB_TOKEN", "SENTRY_DSN", "DATABASE_URL", "MONGO_URI", "app_key"} {
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
