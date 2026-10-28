package apps

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trdSystemDev/cais/internal/docker"
)

// fakeSource e o GitHub: cada commit e um conjunto de arquivos.
type fakeSource struct {
	head    string                       // commit da branch
	commits map[string]map[string]string // commit -> caminho -> conteudo
	refs    []string                     // refs pedidos no Resolve
}

func (f *fakeSource) Resolve(_ context.Context, repo, ref string) (string, error) {
	f.refs = append(f.refs, ref)
	if _, ok := f.commits[ref]; ok {
		return ref, nil
	}
	if ref == "" || ref == "main" {
		return f.head, nil
	}
	return "", fmt.Errorf("%s nao existe em %s", ref, repo)
}

func (f *fakeSource) Download(_ context.Context, _, commit, dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	for p, c := range f.commits[commit] {
		full := filepath.Join(dir, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// fakeDocker registra o que o deploy pediu, com o compose de cada chamada
// (pra saber de qual pasta subiu).
type fakeDocker struct {
	calls   []string
	config  string            // json do compose resolvido
	failUp  map[string]bool   // compose (conteudo) cujo up falha
	envSeen map[string]string // .env lido no Config
}

func (f *fakeDocker) compose(p docker.Project) string {
	data, _ := os.ReadFile(p.Files[0])
	return strings.TrimSpace(string(data))
}

func (f *fakeDocker) Config(_ context.Context, p docker.Project) ([]byte, error) {
	f.calls = append(f.calls, "config "+f.compose(p))
	env, _ := os.ReadFile(filepath.Join(filepath.Dir(p.Files[0]), ".env"))
	f.envSeen, _ = ParseEnv(env)
	if f.config == "" {
		return []byte(`{"services": {}}`), nil
	}
	return []byte(f.config), nil
}

func (f *fakeDocker) Build(_ context.Context, p docker.Project) error {
	f.calls = append(f.calls, "build "+f.compose(p))
	return nil
}

func (f *fakeDocker) Up(_ context.Context, p docker.Project) error {
	c := f.compose(p)
	f.calls = append(f.calls, "up "+c)
	if f.failUp[c] {
		return errors.New("container web is unhealthy")
	}
	return nil
}

func (f *fakeDocker) PS(context.Context, docker.Project) ([]docker.Container, error) {
	return []docker.Container{{Name: "x-web-1", State: "running"}}, nil
}

func (f *fakeDocker) Restart(_ context.Context, p docker.Project, service string) error {
	f.calls = append(f.calls, "restart "+service)
	return nil
}

func (f *fakeDocker) Stop(context.Context, docker.Project) error {
	f.calls = append(f.calls, "stop")
	return nil
}

func newManager(t *testing.T) (Manager, *fakeSource, *fakeDocker) {
	src := &fakeSource{head: "c1", commits: map[string]map[string]string{
		"c1": {"docker-compose.yml": "v1"},
		"c2": {"docker-compose.yml": "v2"},
	}}
	d := &fakeDocker{failUp: map[string]bool{}}
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	return Manager{Root: filepath.Join(t.TempDir(), "apps"), Source: src, Docker: d, Now: func() time.Time { return now }}, src, d
}

func TestCheckRecusa(t *testing.T) {
	ok := App{Name: "meu-app", Repo: "trdSystemDev/meu-app"}
	if err := Check(ok); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*App){
		"nome de stack":       func(a *App) { a.Name = "proxy" },
		"nome maiusculo":      func(a *App) { a.Name = "MeuApp" },
		"repo sem dono":       func(a *App) { a.Repo = "meu-app" },
		"repo url":            func(a *App) { a.Repo = "https://github.com/a/b" },
		"branch com ..":       func(a *App) { a.Branch = "a/../b" },
		"compose fora":        func(a *App) { a.Compose = "../x.yml" },
		"compose absoluto":    func(a *App) { a.Compose = "/etc/x.yml" },
		"compose sem yml":     func(a *App) { a.Compose = "Dockerfile" },
		"compose com espaco":  func(a *App) { a.Compose = "a b.yml" },
		"compose com ./ sujo": func(a *App) { a.Compose = "./x.yml" },
	}
	for name, mutate := range cases {
		a := ok
		mutate(&a)
		if Check(a) == nil {
			t.Errorf("%s: aceitou %+v", name, a)
		}
	}
	a := ok
	a.Compose = "deploy/docker-compose.producao.yaml"
	if err := Check(a); err != nil {
		t.Errorf("recusou compose numa subpasta: %v", err)
	}
}

func TestCreateGetList(t *testing.T) {
	m, _, _ := newManager(t)
	if _, err := m.Create(App{Name: "b", Repo: "o/b", Commit: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(App{Name: "a", Repo: "o/a", Branch: "prod", Compose: "prod.yml"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(App{Name: "a", Repo: "o/a"}); err == nil {
		t.Error("criou app repetido")
	}
	list, err := m.List()
	if err != nil || len(list) != 2 || list[0].Name != "a" || list[1].Commit != "" {
		t.Fatalf("List = %+v, %v", list, err)
	}
	if list[0].ComposeFile() != "prod.yml" || list[1].ComposeFile() != DefaultCompose {
		t.Errorf("compose: %s %s", list[0].ComposeFile(), list[1].ComposeFile())
	}
	info, _ := os.Stat(m.SecretsPath("a"))
	if info == nil || info.Mode().Perm() != 0o700 {
		t.Errorf("pasta de secrets %v", info)
	}
	if _, err := m.Get("c"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get de app que nao existe: %v", err)
	}
}

func TestDeployGravaEnvESobe(t *testing.T) {
	m, src, d := newManager(t)
	m.Create(App{Name: "web", Repo: "o/web", Branch: "main"})
	if err := m.SetEnv("web", map[string]string{"NODE_ENV": "production", "TOKEN": "a$b'c"}); err == nil {
		t.Error("aceitou valor com $ e aspa simples juntos")
	}
	if err := m.SetEnv("web", map[string]string{"NODE_ENV": "production", "PRECO": "R$10"}); err != nil {
		t.Fatal(err)
	}
	m.SetSecret("web", "jwt", []byte("segredo"))

	res, err := m.Deploy(context.Background(), "web", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Commit != "c1" || src.refs[0] != "main" {
		t.Errorf("commit %s, refs %v", res.Commit, src.refs)
	}
	// build antes da troca, up do codigo novo
	if !slices.Equal(d.calls, []string{"config v1", "build v1", "up v1"}) {
		t.Errorf("chamadas %v", d.calls)
	}
	if d.envSeen["NODE_ENV"] != "production" || d.envSeen["PRECO"] != "R$10" || d.envSeen["CAIS_COMMIT"] != "c1" ||
		d.envSeen["CAIS_APP"] != "web" || d.envSeen["CAIS_SECRETS"] != m.SecretsPath("web") {
		t.Errorf(".env do projeto %v", d.envSeen)
	}
	info, err := os.Stat(filepath.Join(m.Root, "web", SrcDir, ".env"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf(".env %v %v", info, err)
	}
	a, _ := m.Get("web")
	if a.Commit != "c1" || a.Last == nil || a.Last.Error != "" {
		t.Errorf("app depois do deploy %+v", a)
	}
	if _, err := os.Stat(filepath.Join(m.Root, "web", "src.next")); !os.IsNotExist(err) {
		t.Error("sobrou src.next")
	}
}

func TestDeployComposeNumaSubpasta(t *testing.T) {
	m, src, d := newManager(t)
	src.commits["c1"] = map[string]string{"deploy/prod.yml": "sub"}
	m.Create(App{Name: "web", Repo: "o/web", Compose: "deploy/prod.yml"})
	m.SetEnv("web", map[string]string{"A": "1"})
	if _, err := m.Deploy(context.Background(), "web", ""); err != nil {
		t.Fatal(err)
	}
	// o compose le o .env da pasta do arquivo
	if d.envSeen["A"] != "1" {
		t.Errorf(".env nao ficou ao lado do compose: %v", d.envSeen)
	}
}

func TestDeploySemComposeNaoMexeNoNoAr(t *testing.T) {
	m, src, d := newManager(t)
	m.Create(App{Name: "web", Repo: "o/web"})
	m.Deploy(context.Background(), "web", "")
	src.commits["c3"] = map[string]string{"README.md": "sem compose"}
	d.calls = nil

	_, err := m.Deploy(context.Background(), "web", "c3")
	if err == nil || !strings.Contains(err.Error(), "nao tem o compose") {
		t.Fatalf("erro %v", err)
	}
	if len(d.calls) != 0 {
		t.Errorf("chamou o docker: %v", d.calls)
	}
	a, _ := m.Get("web")
	if a.Commit != "c1" || a.Last.Error == "" || a.Last.Commit != "c3" {
		t.Errorf("app %+v / %+v", a, a.Last)
	}
	if data, _ := os.ReadFile(filepath.Join(m.Root, "web", SrcDir, DefaultCompose)); string(data) != "v1" {
		t.Error("o codigo no ar mudou")
	}
}

func TestDeployFalhoVoltaAAnterior(t *testing.T) {
	m, _, d := newManager(t)
	m.Create(App{Name: "web", Repo: "o/web"})
	if _, err := m.Deploy(context.Background(), "web", "c1"); err != nil {
		t.Fatal(err)
	}
	d.calls = nil
	d.failUp["v2"] = true

	_, err := m.Deploy(context.Background(), "web", "c2")
	if err == nil || !strings.Contains(err.Error(), "unhealthy") || !strings.Contains(err.Error(), "restaurada") {
		t.Fatalf("erro %v", err)
	}
	if !slices.Equal(d.calls, []string{"config v2", "build v2", "up v2", "build v1", "up v1"}) {
		t.Errorf("chamadas %v", d.calls)
	}
	a, _ := m.Get("web")
	if a.Commit != "c1" || !a.Last.RolledBack || a.Last.Commit != "c2" {
		t.Errorf("app %+v / %+v", a, a.Last)
	}
	if data, _ := os.ReadFile(filepath.Join(m.Root, "web", SrcDir, DefaultCompose)); string(data) != "v1" {
		t.Error("src nao voltou pro commit anterior")
	}

	// o proximo deploy que da certo segue normal
	d.failUp["v2"] = false
	if _, err := m.Deploy(context.Background(), "web", "c2"); err != nil {
		t.Fatal(err)
	}
	if a, _ := m.Get("web"); a.Commit != "c2" {
		t.Errorf("commit %s", a.Commit)
	}
}

func TestPrimeiroDeployFalhoNaoTemPraOndeVoltar(t *testing.T) {
	m, _, d := newManager(t)
	d.failUp["v1"] = true
	m.Create(App{Name: "web", Repo: "o/web"})
	_, err := m.Deploy(context.Background(), "web", "")
	if err == nil || strings.Contains(err.Error(), "restaurada") {
		t.Fatalf("erro %v", err)
	}
	a, _ := m.Get("web")
	if a.Commit != "" || a.Last.RolledBack {
		t.Errorf("app %+v", a)
	}
}

func TestRestartEStopPedemDeploy(t *testing.T) {
	m, _, d := newManager(t)
	m.Create(App{Name: "web", Repo: "o/web"})
	if err := m.Restart(context.Background(), "web", ""); err == nil {
		t.Error("reiniciou app sem deploy")
	}
	if cs, err := m.Status(context.Background(), "web"); err != nil || len(cs) != 0 {
		t.Errorf("status antes do deploy %v %v", cs, err)
	}
	m.Deploy(context.Background(), "web", "")
	if err := m.Restart(context.Background(), "web", "worker"); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(context.Background(), "web"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(d.calls, "restart worker") || !slices.Contains(d.calls, "stop") {
		t.Errorf("chamadas %v", d.calls)
	}
}

func TestSecrets(t *testing.T) {
	m, _, _ := newManager(t)
	m.Create(App{Name: "web", Repo: "o/web"})
	if err := m.SetSecret("web", "../fora", []byte("x")); err == nil {
		t.Error("aceitou nome de secret com caminho")
	}
	if err := m.SetSecret("web", "jwt", nil); err == nil {
		t.Error("aceitou secret vazio")
	}
	m.SetSecret("web", "jwt", []byte("a"))
	m.SetSecret("web", "db_url", []byte("b"))
	list, _ := m.Secrets("web")
	if len(list) != 2 || list[0].Name != "db_url" {
		t.Errorf("secrets %v", list)
	}
	m.DeleteSecret("web", "jwt")
	if list, _ := m.Secrets("web"); len(list) != 1 {
		t.Errorf("depois de apagar %v", list)
	}
}

func TestEnvIdaEVolta(t *testing.T) {
	in := map[string]string{"A": "simples", "B": "com $ e espaco", "C": `aspa ' "dupla" \ barra`, "D": ""}
	data, err := FormatEnv(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := ParseEnv(data)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range in {
		if out[k] != v {
			t.Errorf("%s: %q virou %q", k, v, out[k])
		}
	}
	if _, err := FormatEnv(map[string]string{"A": "linha\nquebrada"}); err == nil {
		t.Error("aceitou quebra de linha")
	}
	if _, err := FormatEnv(map[string]string{"1A": "x"}); err == nil {
		t.Error("aceitou nome invalido")
	}
}

func TestWarnings(t *testing.T) {
	src, secretsDir := "/var/lib/cais/apps/web/src.next", "/var/lib/cais/apps/web/secrets"
	ctx := t.TempDir() // contexto de build sem .dockerignore
	cfg := `{"services": {
		"web": {"labels": {"traefik.enable": "true"}, "networks": {"default": null},
			"ports": [{"published": "8080", "target": 3000}],
			"build": {"context": "` + ctx + `"},
			"volumes": [{"type": "bind", "source": "/var/run/docker.sock"}, {"type": "bind", "source": "/etc"},
				{"type": "bind", "source": "` + src + `/worker"}, {"type": "bind", "source": "` + secretsDir + `/jwt"}]},
		"db": {"privileged": true, "container_name": "postgres", "network_mode": "host"}
	}}`
	got := strings.Join(Warnings([]byte(cfg), src, secretsDir, filepath.Join(ctx, ".env")), "\n")
	for _, want := range []string{"privileged", "stack do cais", "rede ou os processos", "8080:3000 em todas",
		"docker.sock", "monta /etc", "proxy_network", ".dockerignore"} {
		if !strings.Contains(got, want) {
			t.Errorf("faltou aviso sobre %q:\n%s", want, got)
		}
	}
	for _, bad := range []string{"/worker", "/jwt"} {
		if strings.Contains(got, bad) {
			t.Errorf("avisou de bind esperado %s:\n%s", bad, got)
		}
	}

	os.WriteFile(filepath.Join(ctx, ".dockerignore"), []byte("node_modules\n.env\n"), 0o644)
	if got := Warnings([]byte(cfg), src, secretsDir, filepath.Join(ctx, ".env")); slices.ContainsFunc(got, func(w string) bool { return strings.Contains(w, ".dockerignore") }) {
		t.Errorf("avisou com .env no .dockerignore: %v", got)
	}
	if got := Warnings([]byte(`{"services": {"web": {}}}`), src, secretsDir, ""); len(got) != 0 {
		t.Errorf("compose limpo com avisos: %v", got)
	}
}

func TestGuide(t *testing.T) {
	g := Guide(GuideInput{App: "web", Host: "web.exemplo.com.br", Networks: []string{"postgres_network"},
		Env: []string{"DATABASE_URL"}, Secrets: []string{"jwt"}})
	for _, want := range []string{
		"traefik.http.routers.web.rule: Host(`web.exemplo.com.br`)",
		"traefik.http.routers.web.entrypoints: websecure",
		"proxy_network:\n    external: true",
		"postgres_network:\n    external: true",
		"file: ${CAIS_SECRETS}/jwt",
		"postgres:5432",
		"env_file: .env",
	} {
		if !strings.Contains(g, want) {
			t.Errorf("guia sem %q:\n%s", want, g)
		}
	}
}
