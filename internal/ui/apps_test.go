package ui

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
	mu  sync.Mutex
	up  map[string]bool
	ups atomic.Int32
}

func (f *fakeApps) Up(_ context.Context, p docker.Project) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.up[p.Name] = true
	f.ups.Add(1)
	return nil
}
func (f *fakeApps) Recreate(ctx context.Context, p docker.Project) error { return f.Up(ctx, p) }
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

// appsCore e um core com config pronta (dominio, Postgres ligado) e docker
// e banco falsos. Devolve tambem quantos SQL de criar banco rodaram.
func appsCore(t *testing.T) (*core.Core, *fakeApps, *atomic.Int32) {
	t.Helper()
	root := t.TempDir()
	fa := &fakeApps{up: map[string]bool{}}
	var sqls atomic.Int32
	c := core.New(core.Deps{
		CheckDocker: okChecker,
		ConfigPath:  filepath.Join(root, "config.yaml"),
		SecretsDir:  filepath.Join(root, "secrets"),
		StackDir:    func(n string) string { return filepath.Join(root, "stacks", n) },
		AppsDir:     filepath.Join(root, "apps"),
		AppDocker:   fa,
		ComposePS:   func(context.Context, string) ([]docker.Container, error) { return nil, nil },
		ExecInput: func(context.Context, string, []byte, ...string) (string, error) {
			sqls.Add(1)
			return "", nil
		},
		ContainerLogs: func(_ context.Context, name string) (string, error) { return "log do " + name, nil },
	})
	cfg := config.Default()
	cfg.Domain, cfg.Email, cfg.TLS = "trd.localhost", "a@trd.localhost", config.TLSSelfSigned
	cfg.Stacks = []string{"postgres", "mysql"}
	if err := config.Save(filepath.Join(root, "config.yaml"), cfg); err != nil {
		t.Fatal(err)
	}
	return c, fa, &sqls
}

func openApps(t *testing.T) (*model, *fakeApps, *atomic.Int32) {
	t.Helper()
	c, fa, sqls := appsCore(t)
	m := open(t, c)
	run(t, m, m.setTab(tabApps))
	waitFor(t, m, "+ Novo app")
	return m, fa, sqls
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

// repo cria uma pasta de repositorio com os arquivos dados.
func repo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
		os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
	}
	return dir
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

func TestAppsImportMostraPlanoEAplica(t *testing.T) {
	m, _, sqls := openApps(t)
	dir := repo(t, map[string]string{
		"Dockerfile":   "FROM node:22\nEXPOSE 3000\nVOLUME /app/uploads\n",
		"package.json": `{"dependencies": {"pg": "8"}}`,
		".env.example": "NODE_ENV=production\nDATABASE_URL=postgres://x\nJWT_SECRET=x\n",
	})

	click(t, m, "app-new")
	typeIn(t, m, "imp-name", "meu-app")
	typeIn(t, m, "imp-dir", dir)
	click(t, m, "imp-analyze")
	waitFor(t, m, "Revise o plano e aplique.")
	waitFor(t, m, "detectado: DATABASE_URL com postgres://")
	if v := m.apps.impPort.Value(); v != "3000" {
		t.Errorf("porta no campo: %q", v)
	}
	if v := m.apps.impHost.Value(); v != "meu-app.trd.localhost" {
		t.Errorf("dominio no campo: %q", v)
	}
	if !strings.Contains(screen(m), "DATABASE_URL_FILE") || !strings.Contains(screen(m), "url_file") {
		t.Errorf("mapeamento do banco nao aparece:\n%s", screen(m))
	}

	reach(t, m, "imp-apply")
	click(t, m, "imp-apply")
	waitFor(t, m, "meu-app configurado.")
	a := appState(t, m, "meu-app")
	if a.Spec == nil || a.Spec.Port != 3000 || a.Database == nil || a.Database.Engine != "postgres" {
		t.Fatalf("app aplicado: %+v", a)
	}
	if len(a.Routes) != 1 || a.Routes[0].Host != "meu-app.trd.localhost" {
		t.Errorf("rotas %+v", a.Routes)
	}
	if sqls.Load() != 1 {
		t.Errorf("banco nao foi criado (%d SQL)", sqls.Load())
	}
	// depois de aplicar, vai pros secrets: o jwt_secret detectado falta
	waitFor(t, m, "jwt_secret")
	waitFor(t, m, "falta informar")
}

// o banco detectado e so sugestao: da pra trocar antes de aplicar
func TestAppsImportTrocaOBancoDetectado(t *testing.T) {
	m, _, sqls := openApps(t)
	dir := repo(t, map[string]string{"Dockerfile": "FROM nginx\nEXPOSE 80\n"})

	click(t, m, "app-new")
	typeIn(t, m, "imp-name", "site")
	typeIn(t, m, "imp-dir", dir)
	click(t, m, "imp-analyze")
	waitFor(t, m, "nao detectado: escolha se o app usa")

	// nada detectado: escolhe MySQL na mao e muda a variavel
	click(t, m, "imp-db-engine") // Postgres
	click(t, m, "imp-db-engine") // MySQL
	waitFor(t, m, "MySQL")
	m.apps.dbRows[0].name.SetValue("DATABASE_HOST")
	reach(t, m, "imp-apply")
	click(t, m, "imp-apply")
	waitFor(t, m, "site configurado.")

	a := appState(t, m, "site")
	if a.Database == nil || a.Database.Engine != "mysql" || a.Database.Env["DATABASE_HOST"] == "" {
		t.Fatalf("banco escolhido na mao: %+v", a.Database)
	}
	if sqls.Load() != 1 {
		t.Errorf("%d SQL", sqls.Load())
	}
}

func TestAppsBancoManualEmAppExistente(t *testing.T) {
	m, _, sqls := openApps(t)
	if _, err := m.api.CreateApp(context.Background(), "legado"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, m, "legado")
	click(t, m, "app:legado")
	click(t, m, "app-sec:Banco")
	waitFor(t, m, "Sem banco. Escolha Postgres ou MySQL")

	click(t, m, "db-engine")
	waitFor(t, m, "+ variavel")
	click(t, m, "db-add")
	n := len(m.apps.dbRows)
	m.apps.dbRows[n-1].name.SetValue("PGDATABASE")
	for m.apps.dbRows[n-1].kind != 2 { // name
		click(t, m, "db-kind:"+strconv.Itoa(n-1))
	}
	click(t, m, "db-save")
	waitFor(t, m, "Banco postgres ligado a legado.")

	a := appState(t, m, "legado")
	if a.Database == nil || a.Database.Env["PGDATABASE"] != "name" || a.Database.Env["DB_PASSWORD_FILE"] != "password_file" {
		t.Fatalf("banco: %+v", a.Database)
	}
	if sqls.Load() != 1 {
		t.Errorf("%d SQL", sqls.Load())
	}
}

func TestAppsSecretsDeployERotas(t *testing.T) {
	m, fa, _ := openApps(t)
	ctx := context.Background()
	m.api.CreateApp(ctx, "api")
	m.api.SetAppSpec(ctx, "api", apps.Spec{Image: "registry.trd.localhost/api", Port: 8080, Secrets: []string{"api_key"},
		Env: map[string]string{"API_KEY_FILE": "/run/secrets/api_key"}})
	waitFor(t, m, "api")
	click(t, m, "app:api")

	// deploy sem o secret: o erro aparece na tela
	typeIn(t, m, "tag", "v1")
	click(t, m, "deploy")
	waitFor(t, m, "faltam secrets do app: api_key")

	click(t, m, "app-sec:Secrets")
	waitFor(t, m, "falta informar")
	typeIn(t, m, "secret-name", "api_key")
	typeIn(t, m, "secret-value", "sk_live_123")
	click(t, m, "secret-save")
	waitFor(t, m, "api_key gravado")
	if strings.Contains(screen(m), "sk_live_123") {
		t.Fatal("o valor do secret apareceu na tela")
	}

	click(t, m, "app-sec:Geral")
	click(t, m, "deploy")
	waitFor(t, m, "api no ar com a tag v1.")
	waitFor(t, m, "1/1 no ar")
	if fa.ups.Load() == 0 {
		t.Fatal("deploy nao subiu nada")
	}

	click(t, m, "app-sec:Rotas")
	click(t, m, "route-add")
	m.apps.routes[0].host.SetValue("api.trd.localhost")
	m.apps.routes[0].port.SetValue("8080")
	click(t, m, "routes-save")
	waitFor(t, m, "Rotas de api salvas.")
	if a := appState(t, m, "api"); len(a.Routes) != 1 || a.Routes[0].Port != 8080 {
		t.Fatalf("rotas %+v", a.Routes)
	}

	// dominio de stack e recusado e o motivo aparece
	m.apps.routes[0].host.SetValue("registry.trd.localhost")
	click(t, m, "routes-save")
	waitFor(t, m, "registry.trd.localhost e de uma stack")
}

func TestAppsLogsDoApp(t *testing.T) {
	m, _, _ := openApps(t)
	ctx := context.Background()
	m.api.CreateApp(ctx, "web")
	m.api.DeployApp(ctx, "web", "services:\n  web:\n    image: nginx\n")
	waitFor(t, m, "1/1 no ar")
	click(t, m, "app:web")
	click(t, m, "app-sec:Logs")
	if m.logsTarget != "web-web-1" || !strings.Contains(m.logsText, "log do web-web-1") {
		t.Fatalf("logs do app: alvo %q texto %q", m.logsTarget, m.logsText)
	}
	waitFor(t, m, "log do web-web-1")

	// saindo da aba, para de consultar
	run(t, m, m.setTab(tabDashboard))
	if _, cmd := m.Update(appLogsTick(m.logsGeneration)); cmd != nil {
		t.Fatal("continuou consultando logs fora da aba Apps")
	}
}

// um deploy feito fora da TUI (CI, CLI) aparece na lista sem clicar em nada
func TestAppsDeployDeForaAparece(t *testing.T) {
	m, _, _ := openApps(t)
	ctx := context.Background()
	m.api.CreateApp(ctx, "fora")
	waitFor(t, m, "sem deploy")
	if _, err := m.api.DeployApp(ctx, "fora", "services:\n  web:\n    image: nginx\n"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, m, "1/1 no ar")
}

func TestAppsVariaveisSalvaEAplica(t *testing.T) {
	m, fa, _ := openApps(t)
	ctx := context.Background()
	m.api.CreateApp(ctx, "loja")
	m.api.SetAppSpec(ctx, "loja", apps.Spec{Image: "registry.trd.localhost/loja", Port: 3000,
		Env: map[string]string{"APP_URL": "http://localhost:3000"}})
	if _, err := m.api.DeployAppTag(ctx, "loja", "v3"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, m, "1/1 no ar")
	click(t, m, "app:loja")
	click(t, m, "app-sec:Variaveis")
	waitFor(t, m, "deploy da tag no ar (v3)")
	if m.apps.envRows[0].value.Value() != "http://localhost:3000" {
		t.Fatalf("valor carregado %q", m.apps.envRows[0].value.Value())
	}

	m.apps.envRows[0].value.SetValue("https://loja.trd.localhost")
	click(t, m, "env-add")
	m.apps.envRows[1].name.SetValue("LOG_LEVEL")
	m.apps.envRows[1].value.SetValue("info")
	before := fa.ups.Load()
	click(t, m, "env-apply")
	waitFor(t, m, "Variaveis de loja salvas e no ar.")
	if fa.ups.Load() != before+1 {
		t.Error("aplicar nao refez o deploy")
	}
	a := appState(t, m, "loja")
	if a.Spec.Env["APP_URL"] != "https://loja.trd.localhost" || a.Spec.Env["LOG_LEVEL"] != "info" || a.Tag != "v3" {
		t.Fatalf("spec %+v tag %s", a.Spec.Env, a.Tag)
	}

	// segredo em variavel: recusado, com o motivo na tela, e nada muda
	click(t, m, "env-add")
	m.apps.envRows[2].name.SetValue("STRIPE_SECRET")
	m.apps.envRows[2].value.SetValue("sk_live_x")
	click(t, m, "env-save")
	waitFor(t, m, "STRIPE_SECRET parece segredo")
	if _, ok := appState(t, m, "loja").Spec.Env["STRIPE_SECRET"]; ok {
		t.Fatal("gravou segredo em variavel")
	}
}

func TestAppsSecretNovoPelaTelaEntraNaSpec(t *testing.T) {
	m, _, _ := openApps(t)
	ctx := context.Background()
	m.api.CreateApp(ctx, "loja")
	m.api.SetAppSpec(ctx, "loja", apps.Spec{Image: "registry.trd.localhost/loja"})
	waitFor(t, m, "loja")
	click(t, m, "app:loja")
	click(t, m, "app-sec:Secrets")
	typeIn(t, m, "secret-name", "stripe_key")
	typeIn(t, m, "secret-value", "sk_live_y")
	click(t, m, "secret-save")
	waitFor(t, m, "stripe_key gravado")
	if a := appState(t, m, "loja"); !slices.Contains(a.Spec.Secrets, "stripe_key") {
		t.Fatalf("secret fora da spec: %+v", a.Spec)
	}
}

func TestAppsImportPeloLinkDoGitHub(t *testing.T) {
	gh := githubtest.New(t, 5512022, map[string]map[string]string{
		"trdSystemDev/loja": {"Dockerfile": "FROM node:22\nEXPOSE 3000\n", ".env.example": "NODE_ENV=production\n"},
	})
	root := t.TempDir()
	c := core.New(core.Deps{CheckDocker: okChecker, ConfigPath: filepath.Join(root, "config.yaml"),
		SecretsDir: filepath.Join(root, "secrets"), AppsDir: filepath.Join(root, "apps"),
		AppDocker: &fakeApps{up: map[string]bool{}}, GitHubURL: gh.URL,
		ComposePS: func(context.Context, string) ([]docker.Container, error) { return nil, nil }})
	cfg := config.Default()
	cfg.Domain, cfg.Email, cfg.TLS = "trd.localhost", "a@trd.localhost", config.TLSSelfSigned
	cfg.GitHub.AppID = 5512022
	config.Save(filepath.Join(root, "config.yaml"), cfg)
	c.SetSecret(context.Background(), core.GitHubKeySecret, string(gh.Key))

	m := open(t, c)
	run(t, m, m.setTab(tabApps))
	click(t, m, "app-new")
	typeIn(t, m, "imp-dir", "https://github.com/trdSystemDev/loja")
	click(t, m, "imp-analyze")
	waitFor(t, m, "Revise o plano e aplique.")
	if m.apps.impName.Value() != "loja" || m.apps.plan.Repo != "trdSystemDev/loja" {
		t.Fatalf("nome %q repo %q", m.apps.impName.Value(), m.apps.plan.Repo)
	}
	reach(t, m, "imp-apply")
	click(t, m, "imp-apply")
	waitFor(t, m, "loja configurado.")

	waitFor(t, m, "sem deploy") // o card do app novo na lista
	click(t, m, "app:loja")
	click(t, m, "app-sec:Geral")
	waitFor(t, m, "github.com/trdSystemDev/loja")
	click(t, m, "ci-pr")
	waitFor(t, m, "PR aberto: https://github.com/trdSystemDev/loja/pull/1")
	if gh.PR("trdSystemDev/loja", ".woodpecker.yml") == "" {
		t.Fatal("o PR nao tem o .woodpecker.yml")
	}
}
