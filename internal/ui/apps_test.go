package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/docker"
	"github.com/trdSystemDev/cais/internal/github/githubtest"
	"github.com/trdSystemDev/cais/internal/ptyio"
)

// fakeApps e o docker dos apps nos testes: todo projeto que subiu aparece
// com um container web rodando.
type fakeApps struct {
	mu      sync.Mutex
	up      map[string]bool
	stopped map[string]bool // parados: aparecem com state exited
	ups     atomic.Int32
	upErr   error
	config  string
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
	if f.stopped[p.Name] {
		return []docker.Container{{Name: p.Name + "-web-1", Service: "web", State: "exited"}}, nil
	}
	return []docker.Container{{Name: p.Name + "-web-1", Service: "web", State: "running", Health: "healthy"}}, nil
}
func (f *fakeApps) Restart(context.Context, docker.Project, string) error { return nil }
func (f *fakeApps) Stop(context.Context, docker.Project) error            { return nil }
func (*fakeApps) TagImage(context.Context, string, string) error          { return nil }
func (*fakeApps) HasImage(context.Context, string) bool                   { return false }
func (*fakeApps) ImageTags(context.Context, string) ([]string, error)     { return nil, nil }
func (*fakeApps) RemoveImages(context.Context, []string) error            { return nil }
func (*fakeApps) PruneImages(context.Context, string) error               { return nil }

func (f *fakeApps) Rollout(context.Context, docker.Project, string, string, bool) error { return nil }
func (f *fakeApps) Down(_ context.Context, name string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.up, name)
	return nil
}

// appsCore e um core com config pronta (dominio, Postgres ligado), o GitHub
// App falso com o repositorio trdSystemDev/loja e o docker fingido.
func appsCore(t *testing.T) (*core.Core, *fakeApps, *githubtest.Server) {
	t.Helper()
	root := t.TempDir()
	gh := githubtest.New(t, 5512022, map[string]map[string]string{
		"trdSystemDev/loja": {"docker-compose.yml": "services: {web: {build: .}}\n", "Dockerfile": "FROM nginx\n"},
	})
	fa := &fakeApps{up: map[string]bool{}}
	c := core.New(core.Deps{TraefikMetrics: noTraffic,
		CheckDocker: okChecker,
		ConfigPath:  filepath.Join(root, "config.yaml"),
		SecretsDir:  filepath.Join(root, "secrets"),
		StackDir:    func(n string) string { return filepath.Join(root, "stacks", n) },
		AppsDir:     filepath.Join(root, "apps"),
		AppDocker:   fa,
		AppSettle:   -1,
		GitHubURL:   gh.URL,
		ComposePS:   func(context.Context, string) ([]docker.Container, error) { return nil, nil },
		ContainerLogs: func(_ context.Context, name string, _ docker.LogsOptions) (string, error) {
			return "log do " + name, nil
		},
		DockerStats: func(context.Context) ([]docker.ContainerStats, error) { return nil, nil },
		ExecInput:   func(context.Context, string, []byte, ...string) (string, error) { return "", nil },
		Exec:        fakeGarage,
		StartTerminal: func(container, term string, rows, cols uint16, args ...string) (*os.File, error) {
			shellsOpened.Add(1)
			return ptyio.Start(exec.Command("sh", "-c", "echo dentro do "+container), rows, cols)
		},
	})
	cfg := config.Default()
	cfg.TestDomain, cfg.Email, cfg.TLS = "trd.localhost", "a@trd.localhost", config.TLSSelfSigned
	cfg.Stacks, cfg.Hosts.S3 = []string{"proxy", "postgres", "garage"}, "s3.trd.localhost"
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
	waitFor(t, m, "ex: loja.trd.localhost")
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

// valor com varias linhas (chave PEM) colado no valor fica inteiro; o
// campo mostra quantas linhas tem e salvar nao corrompe (nem o que veio do
// CLI)
func TestAppsVariavelComVariasLinhas(t *testing.T) {
	m, _, _ := openApps(t)
	ctx := context.Background()
	m.api.CreateApp(ctx, apps.App{Name: "loja", Repo: "trdSystemDev/loja"})
	const pem = "-----BEGIN-----\nabc\n-----END-----"
	m.api.SetAppEnv(ctx, "loja", map[string]string{"DO_CLI": "linha1\nlinha2"})
	waitFor(t, m, "sem deploy")
	click(t, m, "app:loja")
	click(t, m, "app-sec:Variaveis")
	waitFor(t, m, "‹2 linhas›")

	click(t, m, "env-add")
	typeIn(t, m, "env-name:1", "CHAVE")
	click(t, m, "env-value:1")
	m.Update(tea.PasteMsg{Content: pem + "\r\n"})
	waitFor(t, m, "‹3 linhas›")
	if strings.Contains(screen(m), "abc") {
		t.Fatal("o valor de varias linhas apareceu inteiro no campo")
	}
	click(t, m, "env-save")
	waitFor(t, m, "Variaveis de loja salvas")
	env, _ := m.api.AppEnv(ctx, "loja")
	if env["CHAVE"] != pem || env["DO_CLI"] != "linha1\nlinha2" {
		t.Fatalf("env %q", env)
	}

	// digitar troca o valor de varias linhas por um de uma linha
	typeIn(t, m, "env-value:0", "uma")
	click(t, m, "env-save")
	waitFor(t, m, "Variaveis de loja salvas")
	if env, _ := m.api.AppEnv(ctx, "loja"); env["DO_CLI"] != "uma" || env["CHAVE"] != pem {
		t.Fatalf("env depois de digitar %q", env)
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

	// o Shell do container: o daemon abre e a TUI liga o terminal a ele
	if !strings.Contains(screen(m), "‹ loja-web-1 ›") || !strings.Contains(screen(m), "Shell") {
		t.Fatalf("sem o botao Shell:\n%s", screen(m))
	}
	before := shellsOpened.Load()
	click(t, m, "app-shell")
	if shellsOpened.Load() != before+1 || m.apps.shellErr != "" {
		t.Fatalf("shell: %d abertos, erro %q", shellsOpened.Load()-before, m.apps.shellErr)
	}
	var out bytes.Buffer
	f, err := m.api.(core.ShellOpener).OpenShell(ctx, core.ShellRequest{App: "loja"})
	if err != nil {
		t.Fatal(err)
	}
	sx := &shellExec{f: f, container: "loja-web-1"}
	sx.SetStdin(strings.NewReader(""))
	sx.SetStdout(&out)
	if err := sx.Run(); err != nil || !strings.Contains(out.String(), "dentro do loja-web-1") {
		t.Errorf("shell: %v %q", err, out.String())
	}

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

// excluir pede o nome do app digitado; os volumes so saem se pedir
func TestAppsExcluirComConfirmacao(t *testing.T) {
	m, _, _ := openApps(t)
	m.api.CreateApp(context.Background(), apps.App{Name: "loja", Repo: "trdSystemDev/loja"})
	waitFor(t, m, "sem deploy")
	click(t, m, "app:loja")
	reach(t, m, "delete")
	click(t, m, "delete") // sem confirmar: nada acontece
	if a := appState(t, m, "loja"); a.Name != "loja" {
		t.Fatal("excluiu sem confirmar")
	}
	reach(t, m, "delete-confirm")
	typeIn(t, m, "delete-confirm", "loja")
	reach(t, m, "delete")
	click(t, m, "delete")
	waitFor(t, m, "loja excluido")
	waitFor(t, m, "Nenhum app ainda")
}

func TestAppsParadoEVariaveisAparecem(t *testing.T) {
	m, fa, _ := openApps(t)
	ctx := context.Background()
	m.api.CreateApp(ctx, apps.App{Name: "loja", Repo: "trdSystemDev/loja"})
	m.api.SetAppEnv(ctx, "loja", map[string]string{"DOMINIO": "loja.exemplo.com"})
	if _, err := m.api.DeployApp(ctx, "loja", ""); err != nil {
		t.Fatal(err)
	}
	m.api.StopApp(ctx, "loja")
	fa.mu.Lock()
	fa.stopped = map[string]bool{"loja": true}
	fa.mu.Unlock()
	m.api.CheckApps(ctx)
	waitFor(t, m, "parado")

	click(t, m, "app:loja")
	click(t, m, "app-sec:Variaveis")
	waitFor(t, m, "loja.exemplo.com") // valor visivel sem foco
}

// Importar .env: o texto colado entra nos editores e so grava no Salvar
func TestAppsImportarEnv(t *testing.T) {
	m, _, _ := openApps(t)
	ctx := context.Background()
	m.api.CreateApp(ctx, apps.App{Name: "loja", Repo: "trdSystemDev/loja"})
	m.api.SetAppEnv(ctx, "loja", map[string]string{"DOMINIO": "velho", "FICA": "1"})
	waitFor(t, m, "sem deploy")
	click(t, m, "app:loja")
	click(t, m, "app-sec:Variaveis")
	waitFor(t, m, "velho")
	click(t, m, "env-import")
	waitFor(t, m, "aguardando o texto colado")
	m.Update(tea.PasteMsg{Content: "# comentario\nexport DOMINIO=novo\nNODE_ENV='production'\n"})
	waitFor(t, m, "troca o valor")
	click(t, m, "env-import-apply")
	waitFor(t, m, "1 novas, 1 com valor trocado")
	waitFor(t, m, "alteracoes nao salvas")
	if env, _ := m.api.AppEnv(ctx, "loja"); env["DOMINIO"] != "velho" {
		t.Fatal("gravou antes do Salvar")
	}
	click(t, m, "env-save")
	waitFor(t, m, "salvas; valem no proximo deploy")
	env, _ := m.api.AppEnv(ctx, "loja")
	if !maps.Equal(env, map[string]string{"DOMINIO": "novo", "FICA": "1", "NODE_ENV": "production"}) {
		t.Errorf("env %v", env)
	}
	if strings.Contains(screen(m), "alteracoes nao salvas") {
		t.Error("aviso ficou depois de salvar")
	}

	// texto invalido: o motivo aparece e nada muda
	click(t, m, "env-import")
	m.Update(tea.PasteMsg{Content: "isso nao e env"})
	waitFor(t, m, "linha 1 invalida")
	click(t, m, "env-import-cancel")
}

// o banco da infra liga pelo seletor em Bancos e buckets e aparece com as
// variaveis dele (tambem em Variaveis)
func TestAppsBancoLigadoAparece(t *testing.T) {
	m, _, _ := openApps(t)
	ctx := context.Background()
	c := m.api.(*core.Core)
	m.api.CreateApp(ctx, apps.App{Name: "loja", Repo: "trdSystemDev/loja"})
	if _, err := c.CreateDatabase(ctx, core.DatabaseRequest{Engine: "postgres", Name: "loja"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, m, "sem deploy")
	click(t, m, "app:loja")
	click(t, m, "app-sec:Bancos e buckets")
	waitFor(t, m, "‹ postgres/loja ›")
	click(t, m, "db-link")
	waitFor(t, m, "loja ligado a postgres/loja")
	if a := appState(t, m, "loja"); len(a.Databases) != 1 {
		t.Fatalf("bancos %+v", a.Databases)
	}
	waitFor(t, m, "POSTGRES_LOJA_USER, POSTGRES_LOJA_PASS, POSTGRES_LOJA_URL")
	click(t, m, "app-sec:Variaveis")
	waitFor(t, m, "Dos bancos e buckets ligados (no deploy): POSTGRES_LOJA_USER")
	click(t, m, "app-sec:Bancos e buckets")
	click(t, m, "db-unlink:postgres/loja")
	waitFor(t, m, "desligado de postgres/loja")
}

// o bucket do Garage liga pelo seletor em Bancos e buckets e aparece com as
// variaveis dele
func TestAppsBucketLigado(t *testing.T) {
	m, _, _ := openApps(t)
	ctx := context.Background()
	c := m.api.(*core.Core)
	m.api.CreateApp(ctx, apps.App{Name: "loja", Repo: "trdSystemDev/loja"})
	if _, err := c.CreateBucket(ctx, "loja-fotos"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, m, "sem deploy")
	click(t, m, "app:loja")
	click(t, m, "app-sec:Bancos e buckets")
	waitFor(t, m, "‹ loja-fotos ›")
	click(t, m, "bucket-link")
	waitFor(t, m, "loja ligado ao bucket loja-fotos")
	waitFor(t, m, "S3_LOJA_FOTOS_ACCESS_KEY")
	click(t, m, "bucket-unlink:loja-fotos")
	waitFor(t, m, "desligado do bucket loja-fotos")
}

// banco e bucket criados com o app ja aberto aparecem ao voltar pra aba Apps
// ou pra secao Bancos e buckets, sem reabrir a TUI
func TestAppsListaDeLigarAtualiza(t *testing.T) {
	m, _, _ := openApps(t)
	ctx := context.Background()
	c := m.api.(*core.Core)
	m.api.CreateApp(ctx, apps.App{Name: "loja", Repo: "trdSystemDev/loja"})
	waitFor(t, m, "sem deploy")
	click(t, m, "app:loja")
	click(t, m, "app-sec:Bancos e buckets")
	waitFor(t, m, "nenhum bucket criado")

	// criado na Infra: volta pra aba Apps
	if _, err := c.CreateBucket(ctx, "loja-fotos"); err != nil {
		t.Fatal(err)
	}
	click(t, m, "tab:Infra")
	click(t, m, "tab:Apps")
	waitFor(t, m, "‹ loja-fotos ›")

	// criado pelo CLI: troca de secao e volta pra Bancos e buckets
	if _, err := c.CreateDatabase(ctx, core.DatabaseRequest{Engine: "postgres", Name: "loja"}); err != nil {
		t.Fatal(err)
	}
	click(t, m, "app-sec:Geral")
	click(t, m, "app-sec:Bancos e buckets")
	waitFor(t, m, "‹ postgres/loja ›")
}

// o texto de ajuda de cada parte da secao Bancos e buckets abre no [i] e
// fecha com esc ou clique, sem ocupar a tela
func TestAppsBancosAjudaNoModal(t *testing.T) {
	m, _, _ := openApps(t)
	m.api.CreateApp(context.Background(), apps.App{Name: "loja", Repo: "trdSystemDev/loja"})
	waitFor(t, m, "sem deploy")
	click(t, m, "app:loja")
	click(t, m, "app-sec:Bancos e buckets")
	waitFor(t, m, "nenhum banco ligado")
	if strings.Contains(screen(m), "garage:3900") {
		t.Fatal("texto de ajuda dos buckets aparece fora do modal")
	}
	click(t, m, "info-buckets")
	if !strings.Contains(screen(m), "garage:3900") {
		t.Fatalf("modal dos buckets nao abriu:\n%s", screen(m))
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if strings.Contains(screen(m), "garage:3900") {
		t.Fatal("esc nao fechou o modal")
	}
	click(t, m, "info-dbs")
	if !strings.Contains(screen(m), "postgres:5432") {
		t.Fatalf("modal dos bancos nao abriu:\n%s", screen(m))
	}
	m.Update(tea.MouseReleaseMsg{X: 1, Y: 1, Button: tea.MouseLeft})
	if m.apps.info != nil {
		t.Fatal("clique nao fechou o modal")
	}
}

// os logs do app copiam ao arrastar e rolam sozinhos quando o mouse passa do
// topo, como os da Infra
func TestAppsLogsSelecionaECopia(t *testing.T) {
	m, _, _ := openApps(t)
	ctx := context.Background()
	m.api.CreateApp(ctx, apps.App{Name: "loja", Repo: "trdSystemDev/loja"})
	if _, err := m.api.DeployApp(ctx, "loja", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, m, "1/1 no ar")
	click(t, m, "app:loja")
	click(t, m, "app-sec:Logs")
	var lines []string
	for i := range 100 {
		lines = append(lines, fmt.Sprintf("linha %03d", i))
	}
	m.logsText = strings.Join(lines, "\n")
	m.View()
	area := m.logArea
	if len(area.lines) == 0 {
		t.Fatal("logs do app sem area de selecao")
	}
	// a area bate com o que esta na tela
	row := strings.Split(screen(m), "\n")[area.y]
	if got := ansi.Cut(row, area.x, area.x+9); got != area.lines[0][:9] {
		t.Fatalf("area fora do lugar: tela %q, area %q", got, area.lines[0])
	}

	// arrasta da ultima linha visivel pra cima do painel: rola ate o comeco
	m.Update(tea.MouseClickMsg{X: area.x + 8, Y: area.y + len(area.lines) - 1, Button: tea.MouseLeft})
	_, cmd := m.Update(tea.MouseMotionMsg{X: area.x, Y: area.y - 1, Button: tea.MouseLeft})
	if cmd == nil {
		t.Fatal("nao comecou a rolar ao passar do topo")
	}
	for ticks := 0; cmd != nil; ticks++ {
		if ticks > 100 {
			t.Fatal("rolagem nao parou no comeco")
		}
		_, cmd = m.Update(logSelectionScrollTick(m.selectionGeneration))
	}
	if m.logSelection.viewStart != 0 {
		t.Fatalf("nao chegou ao comeco: %d", m.logSelection.viewStart)
	}
	if want := fmt.Sprintf("Logs · 1–%d/100 · início", m.logSelection.viewHeight); !strings.Contains(screen(m), want) {
		t.Fatalf("posicao %q nao aparece:\n%s", want, screen(m))
	}
	_, cmd = m.Update(tea.MouseReleaseMsg{X: area.x, Y: area.y - 1, Button: tea.MouseLeft})
	if got := clipboardText(cmd); got != strings.Join(lines, "\n") {
		t.Fatalf("copiou %q", got)
	}
	if !strings.Contains(screen(m), "Texto copiado") {
		t.Fatal("aviso de copia nao apareceu")
	}

	// trocar de secao descarta a selecao
	click(t, m, "app-sec:Geral")
	if len(m.logSelection.lines) > 0 {
		t.Fatal("selecao sobrou ao trocar de secao")
	}
}

// homologacao pela TUI: o botao cria ligada a producao, o deploy automatico
// liga por um seletor (e o Salvar do repositorio nao o desfaz) e o commit
// validado vai pra producao pelo Promover
func TestAppsHomologacaoEPromocao(t *testing.T) {
	m, _, gh := openApps(t)
	ctx := context.Background()
	const repo = "trdSystemDev/loja"
	m.api.CreateApp(ctx, apps.App{Name: "loja", Repo: repo})
	if _, err := m.api.DeployApp(ctx, "loja", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, m, "1/1 no ar")
	click(t, m, "app:loja")
	click(t, m, "app-sec:Geral")
	waitFor(t, m, "Nenhuma homologacao")

	reach(t, m, "homolog-new")
	click(t, m, "homolog-new")
	if m.apps.newName.Value() != "loja-homolog" || m.apps.newRepo.Value() != repo {
		t.Fatalf("cadastro da homologacao: %q %q", m.apps.newName.Value(), m.apps.newRepo.Value())
	}
	click(t, m, "new-auto")
	click(t, m, "new-create")
	waitFor(t, m, "homologacao de loja")
	h := appState(t, m, "loja-homolog")
	if h.PromoteTo != "loja" || !h.AutoDeploy {
		t.Fatalf("homologacao criada: %+v", h.App)
	}

	// o Salvar do repositorio mantem o vinculo e o deploy automatico
	click(t, m, "app-sec:Geral")
	reach(t, m, "app-save")
	click(t, m, "app-save")
	waitFor(t, m, "loja-homolog salvo")
	if h := appState(t, m, "loja-homolog"); h.PromoteTo != "loja" || !h.AutoDeploy {
		t.Fatalf("Salvar desfez a homologacao: %+v", h.App)
	}

	// commit novo, validado na homologacao, promovido pra producao
	gh.SetFile(repo, "Dockerfile", "FROM nginx:2\n")
	if _, err := m.api.DeployApp(ctx, "loja-homolog", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, m, "pode promover")
	reach(t, m, "promote:loja-homolog")
	click(t, m, "promote:loja-homolog")
	waitFor(t, m, "loja no ar no commit")
	if a := appState(t, m, "loja"); a.Commit != gh.Commit(repo) || a.Last.PromotedFrom != "loja-homolog" {
		t.Fatalf("producao depois de promover: %+v", a.App)
	}
	waitFor(t, m, "a producao ja esta no commit")

	// na producao, o deploy automatico liga pelo seletor
	click(t, m, "app:loja")
	click(t, m, "auto")
	waitFor(t, m, "Deploy automatico em loja")
	if !appState(t, m, "loja").AutoDeploy {
		t.Fatal("deploy automatico nao ligou")
	}
}

// o historico aparece na aba Geral; voltar pede confirmacao e sobe o
// commit anterior
func TestAppsHistoricoEVoltar(t *testing.T) {
	m, _, gh := openApps(t)
	ctx := context.Background()
	m.api.CreateApp(ctx, apps.App{Name: "loja", Repo: "trdSystemDev/loja"})
	if _, err := m.api.DeployApp(ctx, "loja", ""); err != nil {
		t.Fatal(err)
	}
	bom := gh.Commit("trdSystemDev/loja")
	gh.SetFile("trdSystemDev/loja", "Dockerfile", "FROM nginx:2\n")
	if _, err := m.api.DeployApp(ctx, "loja", ""); err != nil {
		t.Fatal(err)
	}
	novo := gh.Commit("trdSystemDev/loja")
	waitFor(t, m, "1/1 no ar")
	click(t, m, "app:loja")
	reach(t, m, "rollback:"+bom)
	waitFor(t, m, "Historico")
	if !strings.Contains(screen(m), novo[:7]) {
		t.Fatalf("historico sem o deploy novo:\n%s", screen(m))
	}

	// o primeiro aperto so pergunta
	click(t, m, "rollback:"+bom)
	waitFor(t, m, "Aperte de novo pra confirmar")
	if a := appState(t, m, "loja"); a.Commit != novo {
		t.Fatal("voltou sem confirmar")
	}
	reach(t, m, "rollback:"+bom)
	click(t, m, "rollback:"+bom)
	waitFor(t, m, "loja voltou pro commit "+bom[:7])
	a := appState(t, m, "loja")
	if a.Commit != bom || !a.History[0].Rollback {
		t.Fatalf("depois de voltar: %+v", a.App)
	}
}

// a busca filtra a lista pelo nome: ↑↓ andam so nos que aparecem, os
// numeros viram texto (nao trocam de aba) e esc limpa
func TestAppsBuscaPeloNome(t *testing.T) {
	m, _, _ := openApps(t)
	ctx := context.Background()
	for _, n := range []string{"blog", "loja", "lojinha"} {
		if _, err := m.api.CreateApp(ctx, apps.App{Name: n, Repo: "trdSystemDev/loja"}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, m, "lojinha")
	click(t, m, "app:blog")
	sel := func() string {
		t.Helper()
		a := m.apps.selected()
		if a == nil {
			t.Fatal("nenhum app selecionado")
		}
		return a.Name
	}
	key := func(code rune) { run(t, m, m.key(tea.KeyPressMsg{Code: code})) }

	press(t, m, "/")
	for _, r := range "LOJ" {
		press(t, m, string(r))
	}
	list := func() string { return m.apps.viewList(40, 30) }
	if l := ansi.Strip(list()); strings.Contains(l, "blog") || !strings.Contains(l, "lojinha") {
		t.Fatalf("lista com a busca LOJ:\n%s", l)
	}
	if sel() != "loja" {
		t.Fatalf("selecionado %s, queria loja (o blog sumiu da lista)", sel())
	}
	key(tea.KeyDown)
	key(tea.KeyDown) // pula o blog: volta pra loja
	if sel() != "loja" {
		t.Fatalf("selecionado %s depois de descer duas vezes", sel())
	}

	press(t, m, "1")
	if m.tab != tabApps || !strings.Contains(m.apps.search.Value(), "1") {
		t.Fatalf("o 1 na busca trocou de aba (aba %d, busca %q)", m.tab, m.apps.search.Value())
	}
	if l := ansi.Strip(list()); !strings.Contains(l, "Nenhum app com") {
		t.Fatalf("busca sem resultado:\n%s", l)
	}

	key(tea.KeyEscape)
	if m.apps.searching || m.apps.search.Value() != "" || !strings.Contains(ansi.Strip(list()), "blog") {
		t.Fatalf("esc nao limpou a busca (%v %q)", m.apps.searching, m.apps.search.Value())
	}
}

// muitos apps: a lista rola atras do selecionado e a roda do mouse rola
// sem trocar a selecao
func TestAppsListaRola(t *testing.T) {
	m, _, _ := openApps(t)
	ctx := context.Background()
	const n = 15
	for i := range n {
		if _, err := m.api.CreateApp(ctx, apps.App{Name: fmt.Sprintf("app%02d", i), Repo: "trdSystemDev/loja"}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, m, "app00")
	waitFor(t, m, "abaixo")
	if strings.Contains(screen(m), fmt.Sprintf("app%02d", n-1)) {
		t.Fatalf("a lista nao devia caber na tela:\n%s", screen(m))
	}

	click(t, m, "app:app00")
	run(t, m, m.key(tea.KeyPressMsg{Code: tea.KeyUp})) // volta pro ultimo
	last := fmt.Sprintf("app%02d", n-1)
	if a := m.apps.selected(); a == nil || a.Name != last {
		t.Fatalf("selecionado %+v, queria %s", a, last)
	}
	if s := screen(m); !strings.Contains(s, "│ · "+last) || !strings.Contains(s, "acima") || strings.Contains(s, "│ · app00 ") {
		t.Fatalf("a lista nao foi atras do selecionado:\n%s", s)
	}

	// a roda sobe a lista toda e o selecionado continua o mesmo
	m.View()
	time.Sleep(15 * time.Millisecond)
	z := m.zones.Get("app-list")
	for range n {
		m.Update(tea.MouseWheelMsg{X: (z.StartX + z.EndX) / 2, Y: (z.StartY + z.EndY) / 2, Button: tea.MouseWheelUp})
	}
	if s := screen(m); !strings.Contains(s, "│ · app00 ") || strings.Contains(s, "acima") {
		t.Fatalf("a roda nao subiu a lista:\n%s", s)
	}
	if a := m.apps.selected(); a == nil || a.Name != last {
		t.Fatalf("a roda trocou a selecao: %+v", a)
	}
}

var shellsOpened atomic.Int32
