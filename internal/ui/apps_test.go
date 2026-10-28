package ui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/docker"
	"github.com/trdSystemDev/cais/internal/github/githubtest"
)

// fakeApps e o docker dos apps nos testes: todo projeto que subiu aparece
// com um container web rodando.
type fakeApps struct {
	mu     sync.Mutex
	up     map[string]bool
	ups    atomic.Int32
	upErr  error
	config string
}

func (f *fakeApps) Config(context.Context, docker.Project) ([]byte, error) {
	if f.config != "" {
		return []byte(f.config), nil
	}
	return []byte(`{"services": {}}`), nil
}
func (f *fakeApps) Build(context.Context, docker.Project) error { return nil }
func (f *fakeApps) Up(_ context.Context, p docker.Project) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.upErr != nil {
		return f.upErr
	}
	f.up[p.Name] = true
	f.ups.Add(1)
	return nil
}
func (f *fakeApps) PS(_ context.Context, p docker.Project) ([]docker.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.up[p.Name] {
		return nil, nil
	}
	return []docker.Container{{Name: p.Name + "-web-1", Service: "web", State: "running", Health: "healthy"}}, nil
}
func (f *fakeApps) Restart(context.Context, docker.Project, string) error { return nil }
func (f *fakeApps) Stop(context.Context, docker.Project) error            { return nil }

// appsCore e um core com config pronta (dominio, Postgres ligado), o GitHub
// App falso com o repositorio trdSystemDev/loja e o docker fingido.
func appsCore(t *testing.T) (*core.Core, *fakeApps, *githubtest.Server) {
	t.Helper()
	root := t.TempDir()
	gh := githubtest.New(t, 5512022, map[string]map[string]string{
		"trdSystemDev/loja": {"docker-compose.yml": "services: {web: {build: .}}\n", "Dockerfile": "FROM nginx\n"},
	})
	fa := &fakeApps{up: map[string]bool{}}
	c := core.New(core.Deps{
		CheckDocker:   okChecker,
		ConfigPath:    filepath.Join(root, "config.yaml"),
		SecretsDir:    filepath.Join(root, "secrets"),
		StackDir:      func(n string) string { return filepath.Join(root, "stacks", n) },
		AppsDir:       filepath.Join(root, "apps"),
		AppDocker:     fa,
		AppSettle:     -1,
		GitHubURL:     gh.URL,
		ComposePS:     func(context.Context, string) ([]docker.Container, error) { return nil, nil },
		ContainerLogs: func(_ context.Context, name string) (string, error) { return "log do " + name, nil },
	})
	cfg := config.Default()
	cfg.Domain, cfg.Email, cfg.TLS = "trd.localhost", "a@trd.localhost", config.TLSSelfSigned
	cfg.Stacks = []string{"postgres"}
	cfg.GitHub.AppID = gh.AppID
	if err := c.SaveConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if err := c.SetSecret(context.Background(), core.GitHubKeySecret, string(gh.Key)); err != nil {
		t.Fatal(err)
	}
	return c, fa, gh
}

func openApps(t *testing.T) (*model, *fakeApps, *githubtest.Server) {
	t.Helper()
	c, fa, gh := appsCore(t)
	m := open(t, c)
	run(t, m, m.setTab(tabApps))
	waitFor(t, m, "+ Novo app")
	return m, fa, gh
}

// reach rola o painel com a roda do mouse ate a zona aparecer, como o
// usuario faria pra chegar num botao no fim do formulario.
func reach(t *testing.T, m *model, id string) {
	t.Helper()
	for range 30 {
		m.View()
		time.Sleep(10 * time.Millisecond)
		if z := m.zones.Get(id); z != nil && !z.IsZero() {
			return
		}
		d := m.zones.Get("app-detail")
		if d == nil || d.IsZero() {
			continue
		}
		m.Update(tea.MouseWheelMsg{X: (d.StartX + d.EndX) / 2, Y: (d.StartY + d.EndY) / 2, Button: tea.MouseWheelDown})
	}
	t.Fatalf("zona %q nao apareceu rolando o painel:\n%s", id, screen(m))
}

// typeIn foca o campo clicando e digita como o usuario.
func typeIn(t *testing.T, m *model, id, text string) {
	t.Helper()
	click(t, m, id)
	for _, r := range text {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func appState(t *testing.T, m *model, name string) core.AppStatus {
	t.Helper()
	list, err := m.api.CheckApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range list.List {
		if a.Name == name {
			return a
		}
	}
	t.Fatalf("app %s nao existe", name)
	return core.AppStatus{}
}

// o app nasce do link do repositorio (colado) e a Ajuda mostra o compose
func TestAppsCriaPeloLinkEMostraAjuda(t *testing.T) {
	m, _, _ := openApps(t)
	click(t, m, "app-new")
	waitFor(t, m, "Novo app")
	click(t, m, "new-repo")
	m.Update(tea.PasteMsg{Content: "https://github.com/trdSystemDev/loja"})
	click(t, m, "new-create")
	waitFor(t, m, "loja criado")
	a := appState(t, m, "loja")
	if a.Repo != "trdSystemDev/loja" || a.ComposeFile() != apps.DefaultCompose {
		t.Fatalf("app %+v", a.App)
	}
	waitFor(t, m, "Host(`loja.trd.localhost`)")
	if !strings.Contains(m.apps.guide, "postgres_network:\n    external: true") {
		t.Errorf("ajuda sem a rede do Postgres:\n%s", m.apps.guide)
	}

	// repetido: o erro aparece
	click(t, m, "app-new")
	typeIn(t, m, "new-repo", "trdSystemDev/loja")
	click(t, m, "new-create")
	waitFor(t, m, "ja existe")
}

func TestAppsDeployPeloBotao(t *testing.T) {
	m, fa, gh := openApps(t)
	m.api.CreateApp(context.Background(), apps.App{Name: "loja", Repo: "trdSystemDev/loja"})
	waitFor(t, m, "sem deploy")
	click(t, m, "app:loja")
	click(t, m, "deploy")
	commit := gh.Commit("trdSystemDev/loja")
	waitFor(t, m, "loja no ar no commit "+commit[:7])
	waitFor(t, m, "1/1 no ar")
	if fa.ups.Load() != 1 {
		t.Fatalf("%d up", fa.ups.Load())
	}

	// deploy que falha: o motivo aparece e a versao anterior continua
	fa.upErr = errors.New("container loja-web-1 is unhealthy")
	gh.SetFile("trdSystemDev/loja", "Dockerfile", "FROM nginx:2\n")
	click(t, m, "deploy")
	waitFor(t, m, "is unhealthy")
	if a := appState(t, m, "loja"); a.Commit != commit || a.Last == nil || a.Last.Error == "" {
		t.Fatalf("app depois da falha %+v %+v", a.App, a.Last)
	}
}

// compose fora do formato: o deploy e recusado e a tela diz o motivo e o
// jeito certo
func TestAppsDeployRecusadoMostraOMotivo(t *testing.T) {
	m, fa, _ := openApps(t)
	fa.config = `{"services": {"web": {"ports": [{"published": "8080", "target": 3000}]}}}`
	m.api.CreateApp(context.Background(), apps.App{Name: "loja", Repo: "trdSystemDev/loja"})
	waitFor(t, m, "sem deploy")
	click(t, m, "app:loja")
	click(t, m, "deploy")
	waitFor(t, m, "nao segue o formato do cais")
	waitFor(t, m, "publica a porta 3000")
	waitFor(t, m, "cais app guide loja")
	if fa.ups.Load() != 0 {
		t.Fatal("subiu o compose recusado")
	}
}

func TestAppsVariaveis(t *testing.T) {
	m, _, _ := openApps(t)
	ctx := context.Background()
	m.api.CreateApp(ctx, apps.App{Name: "loja", Repo: "trdSystemDev/loja"})
	m.api.SetAppEnv(ctx, "loja", map[string]string{"NODE_ENV": "production"})
	waitFor(t, m, "sem deploy")
	click(t, m, "app:loja")
	click(t, m, "app-sec:Variaveis")
	if len(m.apps.envRows) != 1 || m.apps.envRows[0].value.Value() != "production" {
		t.Fatalf("variaveis carregadas %+v", m.apps.envRows)
	}
	click(t, m, "env-add")
	typeIn(t, m, "env-name:1", "API_URL")
	typeIn(t, m, "env-value:1", "https://api.trd.localhost")
	click(t, m, "env-save")
	waitFor(t, m, "Variaveis de loja salvas")
	env, _ := m.api.AppEnv(ctx, "loja")
	if env["API_URL"] != "https://api.trd.localhost" || env["NODE_ENV"] != "production" {
		t.Fatalf("env %v", env)
	}

	// variavel do cais e recusada
	m.apps.envRows[0].name.SetValue("CAIS_COMMIT")
	click(t, m, "env-save")
	waitFor(t, m, "CAIS_COMMIT e escrita pelo cais")
}

func TestAppsSecrets(t *testing.T) {
	m, _, _ := openApps(t)
	m.api.CreateApp(context.Background(), apps.App{Name: "loja", Repo: "trdSystemDev/loja"})
	waitFor(t, m, "sem deploy")
	click(t, m, "app:loja")
	click(t, m, "app-sec:Secrets")
	waitFor(t, m, "nenhum secret")
	typeIn(t, m, "secret-name", "api_key")
	typeIn(t, m, "secret-value", "sk_live_123")
	click(t, m, "secret-save")
	waitFor(t, m, "api_key gravado")
	waitFor(t, m, "apagar") // a lista chega pelo estado
	if strings.Contains(screen(m), "sk_live_123") {
		t.Fatal("o valor do secret apareceu na tela")
	}
	click(t, m, "secret-del:api_key")
	waitFor(t, m, "api_key apagado")
	if a := appState(t, m, "loja"); len(a.Secrets) != 0 {
		t.Fatalf("secrets %v", a.Secrets)
	}
}

func TestAppsLogsDoApp(t *testing.T) {
	m, _, _ := openApps(t)
	ctx := context.Background()
	m.api.CreateApp(ctx, apps.App{Name: "loja", Repo: "trdSystemDev/loja"})
	if _, err := m.api.DeployApp(ctx, "loja", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, m, "1/1 no ar")
	click(t, m, "app:loja")
	click(t, m, "app-sec:Logs")
	if m.logsTarget != "loja-web-1" || !strings.Contains(m.logsText, "log do loja-web-1") {
		t.Fatalf("logs do app: alvo %q texto %q", m.logsTarget, m.logsText)
	}
	waitFor(t, m, "log do loja-web-1")

	// saindo da aba, para de consultar
	run(t, m, m.setTab(tabDashboard))
	if _, cmd := m.Update(appLogsTick(m.logsGeneration)); cmd != nil {
		t.Fatal("continuou consultando logs fora da aba Apps")
	}
}

// um deploy feito fora da TUI (CLI, MCP) aparece na lista sem clicar em nada
func TestAppsDeployDeForaAparece(t *testing.T) {
	m, _, _ := openApps(t)
	ctx := context.Background()
	m.api.CreateApp(ctx, apps.App{Name: "loja", Repo: "trdSystemDev/loja"})
	waitFor(t, m, "sem deploy")
	if _, err := m.api.DeployApp(ctx, "loja", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, m, "1/1 no ar")
}

func TestAppsTrocaRepositorioECompose(t *testing.T) {
	m, _, _ := openApps(t)
	m.api.CreateApp(context.Background(), apps.App{Name: "loja", Repo: "trdSystemDev/loja"})
	waitFor(t, m, "sem deploy")
	click(t, m, "app:loja")
	reach(t, m, "compose")
	m.apps.compose.SetValue("docker-compose.producao.yaml")
	reach(t, m, "app-save")
	click(t, m, "app-save")
	waitFor(t, m, "loja salvo")
	if a := appState(t, m, "loja"); a.Compose != "docker-compose.producao.yaml" {
		t.Fatalf("compose %q", a.Compose)
	}
}
