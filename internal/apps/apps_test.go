package apps

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/trdSystemDev/cais/internal/compose"
	"github.com/trdSystemDev/cais/internal/databases"
	"github.com/trdSystemDev/cais/internal/docker"
)

var now = time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)

// fakeDocker registra os ups e guarda o compose que estava no disco em
// cada um (pra conferir o rollback).
type fakeDocker struct {
	mu      sync.Mutex
	ups     []string // conteudo do compose a cada up
	files   [][]string
	failFor string // up falha se o compose contem esse texto
}

func (f *fakeDocker) Up(_ context.Context, p docker.Project) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, err := os.ReadFile(p.Files[0])
	if err != nil {
		return err
	}
	f.ups = append(f.ups, string(data))
	f.files = append(f.files, p.Files)
	if f.failFor != "" && strings.Contains(string(data), f.failFor) {
		return errors.New("container meu-app-web-1 is unhealthy")
	}
	return nil
}
func (f *fakeDocker) PS(context.Context, docker.Project) ([]docker.Container, error) {
	return []docker.Container{{Name: "meu-app-web-1", Service: "web", State: "running"}}, nil
}
func (f *fakeDocker) Restart(context.Context, docker.Project, string) error { return nil }
func (f *fakeDocker) Recreate(ctx context.Context, p docker.Project) error  { return f.Up(ctx, p) }
func (f *fakeDocker) Stop(context.Context, docker.Project) error            { return nil }

func newManager(t *testing.T) (Manager, *fakeDocker) {
	f := &fakeDocker{}
	return Manager{Root: filepath.Join(t.TempDir(), "apps"), Docker: f, Now: func() time.Time { return now }}, f
}

func composeV(tag string) []byte {
	return []byte(`services:
  web:
    image: registry.exemplo.com.br/meu-app:` + tag + `
  worker:
    image: registry.exemplo.com.br/meu-app:` + tag + `
    networks: [default, postgres_network]
networks:
  postgres_network:
    external: true
`)
}

var reserved = []string{"ci.exemplo.com.br", "registry.exemplo.com.br", "s3.exemplo.com.br"}

func TestNomesReservadosEInvalidos(t *testing.T) {
	m, _ := newManager(t)
	for _, name := range []string{"registry", "proxy", "postgres", "woodpecker", "cais", "Meu-App", "-x", "a_b", ""} {
		if _, err := m.Create(name); err == nil {
			t.Errorf("criou app %q", name)
		}
	}
	if _, err := m.Create("meu-app"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create("meu-app"); err == nil {
		t.Error("criou app repetido")
	}
	info, err := os.Stat(filepath.Join(m.Root, "meu-app", "secrets"))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("pasta secrets: %v %v", info, err)
	}
}

func TestDeployGravaOverrideESobe(t *testing.T) {
	m, f := newManager(t)
	m.Create("meu-app")
	routes := []Route{
		{Host: "app.exemplo.com.br", Service: "web", Port: 3000},
		{Host: "app.exemplo.com.br", Path: "/api", Service: "worker", Port: 8080},
	}
	if err := m.SetRoutes(context.Background(), "meu-app", routes, reserved); err != nil {
		t.Fatal(err)
	}
	if len(f.ups) != 0 {
		t.Fatal("subiu antes do primeiro deploy")
	}

	res, err := m.Deploy(context.Background(), "meu-app", composeV("v1"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Images, []string{"registry.exemplo.com.br/meu-app:v1"}) {
		t.Errorf("imagens %v", res.Images)
	}
	if len(f.files) != 1 || len(f.files[0]) != 2 || !strings.HasSuffix(f.files[0][1], OverrideFile) {
		t.Fatalf("up sem o override: %v", f.files)
	}

	var ov struct {
		Services map[string]struct {
			Labels   map[string]string
			Networks []string
		}
	}
	data, _ := os.ReadFile(filepath.Join(m.Root, "meu-app", OverrideFile))
	if err := yaml.Unmarshal(data, &ov); err != nil {
		t.Fatal(err)
	}
	web := ov.Services["web"]
	if web.Labels["traefik.http.routers.meu-app-web-1.rule"] != "Host(`app.exemplo.com.br`)" {
		t.Errorf("labels do web %v", web.Labels)
	}
	if web.Labels["traefik.http.services.meu-app-web-3000.loadbalancer.server.port"] != "3000" {
		t.Errorf("porta do web %v", web.Labels)
	}
	// web nao declarou redes: o override tem que manter a default
	if !slices.Equal(web.Networks, []string{"default", "proxy_network"}) {
		t.Errorf("redes do web %v", web.Networks)
	}
	worker := ov.Services["worker"]
	if worker.Labels["traefik.http.routers.meu-app-worker-2.rule"] != "Host(`app.exemplo.com.br`) && PathPrefix(`/api`)" {
		t.Errorf("labels do worker %v", worker.Labels)
	}
	if !slices.Equal(worker.Networks, []string{"proxy_network"}) {
		t.Errorf("redes do worker %v (ja declarou as dele)", worker.Networks)
	}

	a, _ := m.Get("meu-app")
	if a.Last == nil || a.Last.Error != "" || !a.Last.At.Equal(now) {
		t.Errorf("registro do deploy %+v", a.Last)
	}
}

func TestDeployRecusaComposeERotaSemService(t *testing.T) {
	m, f := newManager(t)
	m.Create("meu-app")
	if _, err := m.Deploy(context.Background(), "meu-app", []byte("services:\n  web:\n    image: x\n    privileged: true\n")); err == nil || !strings.Contains(err.Error(), "privileged") {
		t.Fatalf("aceitou privileged: %v", err)
	}

	m.SetRoutes(context.Background(), "meu-app", []Route{{Host: "app.exemplo.com.br", Service: "api", Port: 80}}, reserved)
	if _, err := m.Deploy(context.Background(), "meu-app", composeV("v1")); err == nil || !strings.Contains(err.Error(), `"api"`) {
		t.Fatalf("aceitou rota pra service que nao existe: %v", err)
	}
	if len(f.ups) != 0 {
		t.Fatal("subiu compose recusado")
	}
	if _, err := os.Stat(filepath.Join(m.Root, "meu-app", ComposeFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("gravou compose recusado")
	}
}

func TestDeployQueFalhaVoltaAVersaoAnterior(t *testing.T) {
	m, f := newManager(t)
	m.Create("meu-app")
	if _, err := m.Deploy(context.Background(), "meu-app", composeV("v1")); err != nil {
		t.Fatal(err)
	}

	f.failFor = ":v2"
	_, err := m.Deploy(context.Background(), "meu-app", composeV("v2"))
	if err == nil || !strings.Contains(err.Error(), "unhealthy") || !strings.Contains(err.Error(), "restaurada") {
		t.Fatalf("erro %v", err)
	}
	// subiu v2 (falhou) e depois v1 de novo
	if len(f.ups) != 3 || !strings.Contains(f.ups[2], ":v1") {
		t.Fatalf("ups %d, ultimo com v1? %v", len(f.ups), f.ups[len(f.ups)-1])
	}
	cur, _ := os.ReadFile(filepath.Join(m.Root, "meu-app", ComposeFile))
	if !strings.Contains(string(cur), ":v1") {
		t.Error("compose no disco nao voltou pra v1")
	}
	a, _ := m.Get("meu-app")
	if a.Last == nil || !a.Last.RolledBack || a.Last.Error == "" {
		t.Errorf("registro do deploy %+v", a.Last)
	}
}

func TestPrimeiroDeployQueFalhaNaoTemPraOndeVoltar(t *testing.T) {
	m, f := newManager(t)
	m.Create("meu-app")
	f.failFor = ":v1"
	_, err := m.Deploy(context.Background(), "meu-app", composeV("v1"))
	if err == nil || strings.Contains(err.Error(), "restaurada") {
		t.Fatalf("erro %v", err)
	}
	if len(f.ups) != 1 {
		t.Errorf("tentou restaurar sem versao anterior: %d ups", len(f.ups))
	}
}

func TestRotasSaoDeUmAppSo(t *testing.T) {
	m, _ := newManager(t)
	m.Create("app-a")
	m.Create("app-b")
	ctx := context.Background()
	if err := m.SetRoutes(ctx, "app-a", []Route{{Host: "a.exemplo.com.br", Service: "web", Port: 80}}, reserved); err != nil {
		t.Fatal(err)
	}

	cases := map[string]Route{
		"dominio de outro app": {Host: "a.exemplo.com.br", Path: "/admin", Service: "web", Port: 80},
		"dominio do registry":  {Host: "registry.exemplo.com.br", Service: "web", Port: 80},
		"dominio invalido":     {Host: "sem-ponto", Service: "web", Port: 80},
		"dominio .local":       {Host: "b.local", Service: "web", Port: 80},
		"caminho sem barra":    {Host: "b.exemplo.com.br", Path: "api", Service: "web", Port: 80},
		"porta invalida":       {Host: "b.exemplo.com.br", Service: "web", Port: 0},
		"service com crase":    {Host: "b.exemplo.com.br", Service: "web`", Port: 80},
		"caminho com crase":    {Host: "b.exemplo.com.br", Path: "/a`) || Host(`x", Service: "web", Port: 80},
	}
	for name, r := range cases {
		if err := m.SetRoutes(ctx, "app-b", []Route{r}, reserved); err == nil {
			t.Errorf("%s: aceitou %s", name, r)
		}
	}
	dup := []Route{{Host: "b.exemplo.com.br", Service: "web", Port: 80}, {Host: "b.exemplo.com.br", Service: "api", Port: 81}}
	if err := m.SetRoutes(ctx, "app-b", dup, reserved); err == nil {
		t.Error("aceitou a mesma rota duas vezes")
	}
	// o proprio app pode regravar as rotas dele
	if err := m.SetRoutes(ctx, "app-a", []Route{{Host: "a.exemplo.com.br", Path: "/v2", Service: "web", Port: 80}}, reserved); err != nil {
		t.Errorf("app nao conseguiu trocar a propria rota: %v", err)
	}
}

func TestSetRoutesEmAppNoArSobeDeNovo(t *testing.T) {
	m, f := newManager(t)
	m.Create("meu-app")
	m.Deploy(context.Background(), "meu-app", composeV("v1"))
	if len(f.files[0]) != 1 {
		t.Fatalf("sem rotas nao devia ter override: %v", f.files[0])
	}
	if err := m.SetRoutes(context.Background(), "meu-app", []Route{{Host: "app.exemplo.com.br", Service: "web", Port: 3000}}, reserved); err != nil {
		t.Fatal(err)
	}
	if len(f.ups) != 2 || len(f.files[1]) != 2 {
		t.Fatalf("trocar rota de app no ar tem que subir com o override: %v", f.files)
	}
	if err := m.SetRoutes(context.Background(), "meu-app", []Route{{Host: "app.exemplo.com.br", Service: "nao-existe", Port: 1}}, reserved); err == nil {
		t.Error("aceitou rota pra service que nao existe em app no ar")
	}
	m.SetRoutes(context.Background(), "meu-app", nil, reserved)
	if _, err := os.Stat(filepath.Join(m.Root, "meu-app", OverrideFile)); !errors.Is(err, os.ErrNotExist) {
		t.Error("sem rotas o override tem que sumir")
	}
}

func TestListIgnoraPastaQueNaoEApp(t *testing.T) {
	m, _ := newManager(t)
	m.Create("b-app")
	m.Create("a-app")
	os.MkdirAll(filepath.Join(m.Root, "lixo"), 0o700)
	list, err := m.List()
	if err != nil || len(list) != 2 || list[0].Name != "a-app" {
		t.Fatalf("List = %+v, %v", list, err)
	}
}

// o compose do app + o override gerado precisam ser aceitos pelo docker
// compose, com a rede default mantida e os labels juntos
func TestOverrideJuntaComOComposeNoDocker(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker nao instalado")
	}
	dir := t.TempDir()
	data := composeV("v1")
	os.WriteFile(filepath.Join(dir, ComposeFile), data, 0o644)
	services, _ := compose.Services(data)
	ov, err := Override(App{Name: "meu-app", Routes: []Route{
		{Host: "app.exemplo.com.br", Service: "web", Port: 3000},
		{Host: "app.exemplo.com.br", Path: "/api", Service: "worker", Port: 8080},
	}}, services, nil)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, OverrideFile), ov, 0o644)

	out, err := exec.Command("docker", "compose", "-p", "meu-app", "-f", filepath.Join(dir, ComposeFile),
		"-f", filepath.Join(dir, OverrideFile), "config", "--format", "json").CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose recusou: %v\n%s", err, out)
	}
	var merged struct {
		Name     string
		Services map[string]struct {
			Labels   map[string]string
			Networks map[string]any
		}
	}
	if err := yaml.Unmarshal(out, &merged); err != nil {
		t.Fatal(err)
	}
	web := merged.Services["web"]
	if _, ok := web.Networks["default"]; !ok {
		t.Errorf("web saiu da rede default: %v", web.Networks)
	}
	if _, ok := web.Networks["proxy_network"]; !ok {
		t.Errorf("web sem proxy_network: %v", web.Networks)
	}
	worker := merged.Services["worker"]
	for _, n := range []string{"default", "postgres_network", "proxy_network"} {
		if _, ok := worker.Networks[n]; !ok {
			t.Errorf("worker sem %s: %v", n, worker.Networks)
		}
	}
	if worker.Labels["traefik.http.routers.meu-app-worker-2.rule"] == "" {
		t.Errorf("labels do worker nao chegaram: %v", worker.Labels)
	}
}

func TestBancoNoOverride(t *testing.T) {
	m, f := newManager(t)
	ctx := context.Background()
	m.Create("meu-app")
	m.Deploy(ctx, "meu-app", composeV("v1"))
	if _, err := m.EnsureDBPassword("meu-app"); err != nil {
		t.Fatal(err)
	}
	pw, _ := m.secrets("meu-app").Get(DBSecret)

	env := DefaultDBEnv()
	env["DATABASE_URL_FILE"] = "url_file"
	db := &Database{Engine: "postgres", Services: []string{"web"}, Env: env}
	if err := m.SetDatabase(ctx, "meu-app", db); err != nil {
		t.Fatal(err)
	}
	if len(f.ups) != 2 {
		t.Fatal("ligar o banco em app no ar tem que subir de novo")
	}

	file := filepath.Join(m.Root, "meu-app", OverrideFile)
	data0, _ := os.ReadFile(file)
	if strings.Contains(string(data0), string(pw)) {
		t.Fatal("a senha do banco foi parar no override")
	}
	url, err := m.secrets("meu-app").Get(DBURLSecret)
	if err != nil || string(url) != "postgres://meu_app:"+string(pw)+"@postgres:5432/meu_app" {
		t.Errorf("secret da URL: %q %v", url, err)
	}
	var ov struct {
		Services map[string]struct {
			Environment map[string]string
			Secrets     []string
			Networks    []string
		}
		Secrets map[string]struct{ File string }
	}
	data, _ := os.ReadFile(file)
	yaml.Unmarshal(data, &ov)
	web := ov.Services["web"]
	want := map[string]string{
		"DB_HOST": "postgres", "DB_PORT": "5432", "DB_NAME": "meu_app", "DB_USER": "meu_app",
		"DB_PASSWORD_FILE":  "/run/secrets/" + DBSecret,
		"DATABASE_URL_FILE": "/run/secrets/" + DBURLSecret,
	}
	for k, v := range want {
		if web.Environment[k] != v {
			t.Errorf("%s = %q, esperava %q", k, web.Environment[k], v)
		}
	}
	if !slices.Equal(web.Networks, []string{"default", "postgres_network"}) {
		t.Errorf("redes do web %v", web.Networks)
	}
	if !slices.Equal(web.Secrets, []string{DBSecret, DBURLSecret}) || ov.Secrets[DBURLSecret].File != "./secrets/"+DBURLSecret {
		t.Errorf("secret do banco: %v %v", web.Secrets, ov.Secrets)
	}
	if _, ok := ov.Services["worker"]; ok {
		t.Error("worker nao foi pedido e recebeu o banco")
	}

	// desligar o banco tira tudo do override (sem rotas, o arquivo some)
	if err := m.SetDatabase(ctx, "meu-app", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Error("sem rotas nem banco o override tem que sumir")
	}
}

func TestCheckDatabaseRecusa(t *testing.T) {
	cases := map[string]Database{
		"engine desconhecido": {Engine: "mongo", Env: DefaultDBEnv()},
		"sem variaveis":       {Engine: "postgres"},
		"valor invalido":      {Engine: "postgres", Env: map[string]string{"X": "senha"}},
		"senha em variavel":   {Engine: "postgres", Env: map[string]string{"DB_PASSWORD": "password"}},
		"url em variavel":     {Engine: "postgres", Env: map[string]string{"DATABASE_URL": "url"}},
		"variavel invalida":   {Engine: "postgres", Env: map[string]string{"1X": "host"}},
		"service invalido":    {Engine: "postgres", Env: DefaultDBEnv(), Services: []string{"a b"}},
	}
	for name, db := range cases {
		if err := CheckDatabase(db); err == nil {
			t.Errorf("%s: aceitou %+v", name, db)
		}
	}
	m, _ := newManager(t)
	m.Create("meu-app")
	m.Deploy(context.Background(), "meu-app", composeV("v1"))
	m.EnsureDBPassword("meu-app")
	err := m.SetDatabase(context.Background(), "meu-app", &Database{Engine: "postgres", Services: []string{"api"}, Env: DefaultDBEnv()})
	if err == nil || !strings.Contains(err.Error(), `"api"`) {
		t.Errorf("aceitou banco pra service que nao existe: %v", err)
	}
}

// rotas + banco no mesmo service: o docker compose aceita e junta tudo
func TestOverrideComBancoNoDocker(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker nao instalado")
	}
	dir := t.TempDir()
	data := composeV("v1")
	os.WriteFile(filepath.Join(dir, ComposeFile), data, 0o644)
	os.Mkdir(filepath.Join(dir, "secrets"), 0o700)
	os.WriteFile(filepath.Join(dir, "secrets", DBSecret), []byte("SENHA"), 0o444)
	os.WriteFile(filepath.Join(dir, "secrets", DBURLSecret), []byte("postgres://x"), 0o444)
	services, _ := compose.Services(data)
	e, _ := databases.Get("postgres")
	a := App{Name: "meu-app",
		Routes:   []Route{{Host: "app.exemplo.com.br", Service: "web", Port: 3000}},
		Database: &Database{Engine: "postgres", Env: DefaultDBEnv()}}
	ov, err := Override(a, services, &DBConn{Engine: e, Name: "meu_app"})
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, OverrideFile), ov, 0o600)
	out, err := exec.Command("docker", "compose", "-p", "meu-app", "-f", filepath.Join(dir, ComposeFile),
		"-f", filepath.Join(dir, OverrideFile), "config", "--format", "json").CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose recusou: %v\n%s", err, out)
	}
	var merged struct {
		Services map[string]struct {
			Environment map[string]string
			Networks    map[string]any
			Secrets     []struct{ Source string }
		}
	}
	yaml.Unmarshal(out, &merged)
	for _, svc := range []string{"web", "worker"} {
		s := merged.Services[svc]
		if s.Environment["DB_HOST"] != "postgres" {
			t.Errorf("%s sem DB_HOST: %v", svc, s.Environment)
		}
		if _, ok := s.Networks["postgres_network"]; !ok {
			t.Errorf("%s sem postgres_network: %v", svc, s.Networks)
		}
		if _, ok := s.Networks["default"]; !ok {
			t.Errorf("%s saiu da default: %v", svc, s.Networks)
		}
		if len(s.Secrets) != 2 || s.Secrets[0].Source != DBSecret || s.Secrets[1].Source != DBURLSecret {
			t.Errorf("%s secrets %v", svc, s.Secrets)
		}
	}
	if _, ok := merged.Services["web"].Networks["proxy_network"]; !ok {
		t.Error("web perdeu a proxy_network da rota")
	}
}

func TestNameFromRepo(t *testing.T) {
	cases := map[string]string{
		"meu-app":                       "meu-app",
		"TRDSmartPodCast":               "trdsmartpodcast",
		"Meu_App":                       "meu-app",
		"meu_app.web":                   "meu-app-web",
		"__x--y..z__":                   "x-y-z",
		"-.-":                           "",
		strings.Repeat("a", 39) + "_bc": strings.Repeat("a", 39),
	}
	for in, want := range cases {
		if got := NameFromRepo(in); got != want {
			t.Errorf("NameFromRepo(%q) = %q, esperava %q", in, got, want)
		}
	}
	if got := ImageFromRepo("registry.exemplo.com.br", "TRDSmartPodCast"); got != "registry.exemplo.com.br/trdsmartpodcast" {
		t.Errorf("imagem %s", got)
	}
}
