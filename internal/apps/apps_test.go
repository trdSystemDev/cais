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
	ov, err := Override("meu-app", []Route{
		{Host: "app.exemplo.com.br", Service: "web", Port: 3000},
		{Host: "app.exemplo.com.br", Path: "/api", Service: "worker", Port: 8080},
	}, services)
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
