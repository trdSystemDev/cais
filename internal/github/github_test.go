package github

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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
	"os"
	"path/filepath"
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
	t       *testing.T
	key     *rsa.PublicKey
	files   map[string]string // caminho -> conteudo no branch principal
	tarball []byte            // o que o tarball devolve (nil = tarOf(files))
	mu      sync.Mutex
	calls   []string
	scope   []string // repositorios pedidos no token
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
	case !inst && !strings.HasPrefix(p, "/codeload/"):
		f.t.Errorf("%s %s sem token de instalacao", r.Method, p)
		w.WriteHeader(401)
	case p == "/repos/trdSystemDev/meu-app":
		io.WriteString(w, `{"default_branch": "principal"}`)
	case strings.HasPrefix(p, "/repos/trdSystemDev/meu-app/contents/") && r.Method == "GET":
		path := strings.TrimPrefix(p, "/repos/trdSystemDev/meu-app/contents/")
		if ref := r.URL.Query().Get("ref"); ref != "principal" {
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
	case strings.HasPrefix(p, "/repos/trdSystemDev/meu-app/commits/"):
		if r.Header.Get("Accept") != "application/vnd.github.sha" {
			f.t.Errorf("commit sem Accept sha")
		}
		if ref := strings.TrimPrefix(p, "/repos/trdSystemDev/meu-app/commits/"); ref != "principal" && ref != testCommit {
			w.WriteHeader(404) // o GitHub devolve 422 ou 404; os dois viram "nao existe"
			io.WriteString(w, `{"message":"Not Found"}`)
			return
		}
		io.WriteString(w, testCommit)
	case p == "/repos/trdSystemDev/meu-app/compare/principal..."+testCommit:
		if r.URL.Query().Get("per_page") != "1" {
			f.t.Error("compare sem per_page=1 (a lista de commits nao interessa)")
		}
		io.WriteString(w, `{"status": "behind", "ahead_by": 0, "behind_by": 2, "files": [{"filename": "x", "patch": "@@"}]}`)
	case p == "/repos/trdSystemDev/meu-app/compare/principal...nao-existe":
		w.WriteHeader(404)
		io.WriteString(w, `{"message":"Not Found"}`)
	case p == "/repos/trdSystemDev/meu-app/git/commits/"+testCommit:
		io.WriteString(w, `{"sha": "`+testCommit+`", "tree": {"sha": "arvore1"}}`)
	case p == "/repos/trdSystemDev/meu-app/tarball/"+testCommit:
		// o GitHub redireciona pro codeload, que nao pode receber o token
		http.Redirect(w, r, "/codeload/"+testCommit, http.StatusFound)
	case p == "/codeload/"+testCommit:
		data := f.tarball
		if data == nil {
			data = tarOf(f.t, "trdSystemDev-meu-app-"+testCommit[:7], f.files)
		}
		w.Write(data)
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
	got, err := s.ReadFile(context.Background(), "Dockerfile")
	if err != nil || !strings.Contains(string(got), "EXPOSE 3000") {
		t.Fatalf("Dockerfile %q, %v", got, err)
	}
	if _, err := s.ReadFile(context.Background(), ".env.example"); !IsNotFound(err) {
		t.Errorf("arquivo que nao existe: %v", err)
	}
}

const testCommit = "0123456789abcdef0123456789abcdef01234567"

// tarEntry e uma entrada do tar.gz falso; link != "" e symlink.
type tarEntry struct{ name, body, link string }

func tarOf(t *testing.T, top string, files map[string]string) []byte {
	var list []tarEntry
	for n, c := range files {
		list = append(list, tarEntry{name: top + "/" + n, body: c})
	}
	return tarEntries(t, list)
}

func tarEntries(t *testing.T, list []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range list {
		h := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		if e.link != "" {
			h = &tar.Header{Name: e.name, Linkname: e.link, Typeflag: tar.TypeSymlink}
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		io.WriteString(tw, e.body)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestResolveEDownload(t *testing.T) {
	app, f := newApp(t, map[string]string{
		"docker-compose.yml": "services: {}\n",
		"app/Dockerfile":     "FROM node:22\n",
	})
	s, err := app.Open(context.Background(), Repo{"trdSystemDev", "meu-app"})
	if err != nil {
		t.Fatal(err)
	}
	commit, err := s.Resolve(context.Background(), "")
	if err != nil || commit != testCommit {
		t.Fatalf("Resolve = %q, %v", commit, err)
	}
	if _, err := s.Resolve(context.Background(), "nao-existe"); err == nil || !strings.Contains(err.Error(), "nao existe") {
		t.Errorf("ref inexistente: %v", err)
	}

	dir := filepath.Join(t.TempDir(), "src")
	if err := s.Download(context.Background(), commit, dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "app", "Dockerfile"))
	if err != nil || string(got) != "FROM node:22\n" {
		t.Fatalf("Dockerfile extraido %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "docker-compose.yml")); err != nil {
		t.Error("a pasta de cima do tarball nao foi tirada")
	}
	// pasta que ja existe nao e sobrescrita
	if err := s.Download(context.Background(), commit, dir); err == nil {
		t.Error("baixou por cima de uma pasta existente")
	}
	if len(f.scope) != 1 {
		t.Errorf("token com escopo %v", f.scope)
	}
}

func TestDownloadRecusaCaminhoPraFora(t *testing.T) {
	cases := map[string][]tarEntry{
		"..":               {{name: "top/../../fora", body: "x"}},
		"symlink absoluto": {{name: "top/etc", link: "/etc"}},
		"symlink pra fora": {{name: "top/sobe", link: "../../.."}},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			app, f := newApp(t, nil)
			f.tarball = tarEntries(t, entries)
			s, err := app.Open(context.Background(), Repo{"trdSystemDev", "meu-app"})
			if err != nil {
				t.Fatal(err)
			}
			base := t.TempDir()
			dir := filepath.Join(base, "src")
			if err := s.Download(context.Background(), testCommit, dir); err == nil {
				t.Fatal("aceitou tarball que grava fora da pasta")
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Error("a pasta do download falho ficou")
			}
			if _, err := os.Stat(filepath.Join(base, "fora")); err == nil {
				t.Error("gravou fora da pasta")
			}
		})
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

func TestCompareETree(t *testing.T) {
	app, _ := newApp(t, map[string]string{"docker-compose.yml": "services: {}\n"})
	ctx := context.Background()
	s, err := app.Open(ctx, Repo{"trdSystemDev", "meu-app"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Compare(ctx, "principal", testCommit)
	if err != nil || c != (Comparison{Status: "behind", BehindBy: 2}) {
		t.Fatalf("Compare = %+v, %v", c, err)
	}
	if _, err := s.Compare(ctx, "principal", "nao-existe"); err == nil || !strings.Contains(err.Error(), "nao achei") {
		t.Errorf("compare de ref inexistente: %v", err)
	}
	if tree, err := s.Tree(ctx, testCommit); err != nil || tree != "arvore1" {
		t.Fatalf("Tree = %q, %v", tree, err)
	}
}
