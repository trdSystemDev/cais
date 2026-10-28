package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseRepo(t *testing.T) {
	ok := map[string]Repo{
		"https://github.com/trdSystemDev/meu-app":     {"trdSystemDev", "meu-app"},
		"https://github.com/trdSystemDev/meu-app.git": {"trdSystemDev", "meu-app"},
		"https://github.com/trdSystemDev/meu-app/":    {"trdSystemDev", "meu-app"},
		"git@github.com:trdSystemDev/meu-app.git":     {"trdSystemDev", "meu-app"},
		"trdSystemDev/meu-app":                        {"trdSystemDev", "meu-app"},
		"  https://www.github.com/trdSystemDev/app  ": {"trdSystemDev", "app"},
	}
	for in, want := range ok {
		got, err := ParseRepo(in)
		if err != nil || got != want {
			t.Errorf("ParseRepo(%q) = %+v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "meu-app", "https://gitlab.com/a/b", "https://github.com/a/b/tree/main",
		"https://github.com/a/..", "https://github.com/-a/b", "a/b c", "https://evil.com/github.com/a/b"} {
		if _, err := ParseRepo(bad); err == nil {
			t.Errorf("ParseRepo(%q) aceitou", bad)
		}
	}
}

func testKey(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
}

// fakeGitHub simula as rotas da API que o cais usa e confere a
// autenticacao de cada uma.
type fakeGitHub struct {
	t     *testing.T
	key   *rsa.PublicKey
	files map[string]string // caminho -> conteudo no branch principal
	mu    sync.Mutex
	calls []string
	puts  map[string]string // arquivo gravado no branch do PR
	scope []string          // repositorios pedidos no token
}

func (f *fakeGitHub) checkJWT(r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
	if len(parts) != 3 {
		f.t.Errorf("%s sem JWT", r.URL.Path)
		return
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(f.key, crypto.SHA256, sum[:], sig); err != nil {
		f.t.Errorf("assinatura do JWT invalida: %v", err)
	}
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c struct {
		Iat, Exp int64
		Iss      string
	}
	json.Unmarshal(payload, &c)
	if c.Iss != "5512022" || c.Exp-c.Iat > 600 || c.Exp <= time.Now().Unix() {
		f.t.Errorf("claims do JWT %+v", c)
	}
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	f.mu.Unlock()
	if r.Header.Get("User-Agent") == "" || r.Header.Get("X-GitHub-Api-Version") == "" {
		f.t.Errorf("%s sem User-Agent/versao da API", r.URL.Path)
	}
	auth := r.Header.Get("Authorization")
	inst := auth == "token ghs_teste"
	p := r.URL.Path
	switch {
	case p == "/repos/trdSystemDev/nao-instalado/installation":
		f.checkJWT(r)
		w.WriteHeader(404)
		io.WriteString(w, `{"message":"Not Found"}`)
	case p == "/repos/trdSystemDev/meu-app/installation":
		f.checkJWT(r)
		io.WriteString(w, `{"id": 77}`)
	case p == "/app/installations/77/access_tokens" && r.Method == "POST":
		f.checkJWT(r)
		var body struct{ Repositories []string }
		json.NewDecoder(r.Body).Decode(&body)
		f.scope = body.Repositories
		w.WriteHeader(201)
		io.WriteString(w, `{"token": "ghs_teste"}`)
	case !inst:
		f.t.Errorf("%s %s sem token de instalacao", r.Method, p)
		w.WriteHeader(401)
	case p == "/repos/trdSystemDev/meu-app":
		io.WriteString(w, `{"default_branch": "principal"}`)
	case strings.HasPrefix(p, "/repos/trdSystemDev/meu-app/contents/") && r.Method == "GET":
		path := strings.TrimPrefix(p, "/repos/trdSystemDev/meu-app/contents/")
		ref := r.URL.Query().Get("ref")
		if ref == "cais/ci" {
			w.WriteHeader(404)
			return
		}
		if ref != "principal" {
			f.t.Errorf("leu %s do ref %q", path, ref)
		}
		if r.Header.Get("Accept") != "application/vnd.github.raw+json" {
			f.t.Errorf("leitura de %s sem Accept raw", path)
		}
		c, ok := f.files[path]
		if !ok {
			w.WriteHeader(404)
			io.WriteString(w, `{"message":"Not Found"}`)
			return
		}
		io.WriteString(w, c)
	case p == "/repos/trdSystemDev/meu-app/git/ref/heads/principal":
		io.WriteString(w, `{"object": {"sha": "abc"}}`)
	case p == "/repos/trdSystemDev/meu-app/git/refs" && r.Method == "POST":
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["ref"] != "refs/heads/cais/ci" || body["sha"] != "abc" {
			f.t.Errorf("branch criado %v", body)
		}
		w.WriteHeader(201)
		io.WriteString(w, `{}`)
	case strings.HasPrefix(p, "/repos/trdSystemDev/meu-app/contents/") && r.Method == "PUT":
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		c, _ := base64.StdEncoding.DecodeString(body["content"])
		if body["branch"] != "cais/ci" {
			f.t.Errorf("gravou no branch %q", body["branch"])
		}
		f.puts = map[string]string{strings.TrimPrefix(p, "/repos/trdSystemDev/meu-app/contents/"): string(c)}
		w.WriteHeader(201)
		io.WriteString(w, `{}`)
	case p == "/repos/trdSystemDev/meu-app/pulls" && r.Method == "POST":
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["head"] != "cais/ci" || body["base"] != "principal" {
			f.t.Errorf("PR %v", body)
		}
		w.WriteHeader(201)
		io.WriteString(w, `{"html_url": "https://github.com/trdSystemDev/meu-app/pull/1"}`)
	default:
		f.t.Errorf("rota inesperada %s %s", r.Method, p)
		w.WriteHeader(404)
	}
}

func newApp(t *testing.T, files map[string]string) (App, *fakeGitHub) {
	k, pemKey := testKey(t)
	parsed, err := ParseKey(pemKey)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeGitHub{t: t, key: &k.PublicKey, files: files}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return App{ID: 5512022, Key: parsed, BaseURL: srv.URL}, f
}

func TestLeArquivosDoRepositorioPrivado(t *testing.T) {
	app, f := newApp(t, map[string]string{
		"Dockerfile":           "FROM node:22\nEXPOSE 3000\n",
		"prisma/schema.prisma": `provider = "postgresql"`,
	})
	s, err := app.Open(context.Background(), Repo{"trdSystemDev", "meu-app"})
	if err != nil {
		t.Fatal(err)
	}
	if s.DefaultBranch != "principal" {
		t.Errorf("branch principal %q", s.DefaultBranch)
	}
	// o token pedido so enxerga o repositorio importado
	if len(f.scope) != 1 || f.scope[0] != "meu-app" {
		t.Errorf("token com escopo %v", f.scope)
	}
	files, err := s.ReadFiles(context.Background(), []string{"Dockerfile", ".env.example", "prisma/schema.prisma"})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || !strings.Contains(string(files["Dockerfile"]), "EXPOSE 3000") || files["prisma/schema.prisma"] == nil {
		t.Fatalf("arquivos %v", files)
	}
}

func TestAppNaoInstalado(t *testing.T) {
	app, _ := newApp(t, nil)
	_, err := app.Open(context.Background(), Repo{"trdSystemDev", "nao-instalado"})
	if err == nil || !strings.Contains(err.Error(), "nao esta instalado") {
		t.Fatalf("erro %v", err)
	}
	if _, err := (App{}).Open(context.Background(), Repo{"a", "b"}); err == nil || !strings.Contains(err.Error(), "nao configurado") {
		t.Fatalf("app sem configuracao: %v", err)
	}
}

func TestAbrePRComArquivo(t *testing.T) {
	app, f := newApp(t, nil)
	s, err := app.Open(context.Background(), Repo{"trdSystemDev", "meu-app"})
	if err != nil {
		t.Fatal(err)
	}
	link, err := s.ProposeFile(context.Background(), "cais/ci", ".woodpecker.yml", []byte("steps: []\n"), "CI do cais", "corpo")
	if err != nil {
		t.Fatal(err)
	}
	if link != "https://github.com/trdSystemDev/meu-app/pull/1" || f.puts[".woodpecker.yml"] != "steps: []\n" {
		t.Fatalf("PR %s arquivo %v", link, f.puts)
	}
}

func TestParseKeyRecusa(t *testing.T) {
	if _, err := ParseKey([]byte("nao e pem")); err == nil {
		t.Error("aceitou chave que nao e PEM")
	}
}

func TestNormalizeKeyColadaNumaLinha(t *testing.T) {
	k, pemKey := testKey(t)
	// o campo de uma linha troca as quebras por espaco
	pasted := "  " + strings.ReplaceAll(string(pemKey), "\n", " ") + " "
	got, err := NormalizeKey(pasted)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseKey(got)
	if err != nil || !parsed.Equal(k) {
		t.Fatalf("chave refeita diferente: %v", err)
	}
	if string(got) != string(pemKey) {
		t.Errorf("PEM refeito diferente do original:\n%s\n%s", got, pemKey)
	}
	for _, bad := range []string{"", "senha", "-----BEGIN RSA PRIVATE KEY----- abc", "-----BEGIN RSA PRIVATE KEY----- AAAA -----END RSA PRIVATE KEY-----"} {
		if _, err := NormalizeKey(bad); err == nil {
			t.Errorf("aceitou %q", bad)
		}
	}
}
