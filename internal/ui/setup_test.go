package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/docker"
	"github.com/trdSystemDev/cais/internal/github/githubtest"
)

// openSetup abre a TUI ja na aba Setup.
func openSetup(t *testing.T) (*model, func() int32) {
	t.Helper()
	c, ups, _ := testCore(t, okChecker)
	m := open(t, c)
	click(t, m, "tab:Setup")
	waitFor(t, m, "Ainda sem configuracao")
	return m, ups.Load
}

// fill preenche o formulario do Setup como o usuario faria: dominio,
// e-mail, CA local (dispensa DNS) e o OAuth app do GitHub (o CI e fixo).
func fill(t *testing.T, m *model, domain string) {
	t.Helper()
	m.setup.get("domain").input.SetValue(domain)
	m.setup.get("email").input.SetValue("admin@exemplo.com.br")
	m.setup.get("ghClient").input.SetValue("Ov23liAbc")
	m.setup.get("ghAdmin").input.SetValue("fulano")
	m.setup.get("ghSecret").input.SetValue("segredo")
	if m.setup.tls == 0 {
		click(t, m, "tls")
	}
}

// o Setup so tem configuracoes: quais servicos ligar fica na aba Infra
func TestSetupNaoTemServicos(t *testing.T) {
	m, _ := openSetup(t)
	m.View()
	time.Sleep(50 * time.Millisecond) // o bubblezone registra as zonas depois
	for _, name := range optional() {
		if z := m.zones.Get("stack:" + name); z != nil && !z.IsZero() {
			t.Errorf("caixa de %s no Setup", name)
		}
	}
	if !strings.Contains(screen(m), "aba Infra") {
		t.Errorf("Setup nao aponta pra aba Infra:\n%s", screen(m))
	}
}

func TestSetupSalvaPeloFormularioEMostraOPlano(t *testing.T) {
	m, _ := openSetup(t)
	fill(t, m, "trd.localhost")
	click(t, m, "save")
	waitFor(t, m, "Config salva.")
	waitFor(t, m, "Pronto pra executar")

	info, err := m.api.LoadConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !info.Exists || info.Config.Domain != "trd.localhost" || info.Config.LetsEncrypt() {
		t.Fatalf("config gravada: %+v", info)
	}
	if len(info.Config.Stacks) != 0 {
		t.Errorf("Setup ligou servico sozinho: %v", info.Config.Stacks)
	}
	if info.Config.Woodpecker.GitHubClient != "Ov23liAbc" {
		t.Errorf("GitHub nao gravou: %+v", info.Config.Woodpecker)
	}
}

func TestSetupMostraErroDeValidacao(t *testing.T) {
	m, _ := openSetup(t)
	fill(t, m, "trd.local")
	click(t, m, "save")
	waitFor(t, m, "reservado pro mDNS")
	if info, _ := m.api.LoadConfig(context.Background()); info.Exists {
		t.Fatal("gravou config invalida")
	}
}

func TestSetupExecutaPeloBotao(t *testing.T) {
	m, ups := openSetup(t)
	fill(t, m, "trd.localhost")
	click(t, m, "save")
	waitFor(t, m, "Pronto pra executar")

	click(t, m, "run")
	waitFor(t, m, "Setup concluido.")
	waitFor(t, m, "✓ Login da VPS em registry.trd.localhost")
	if n := ups(); n != 3 {
		t.Errorf("%d compose up, esperava 3 (proxy, registry, woodpecker)", n)
	}
}

func TestSetupSoExecutaComPlanoPronto(t *testing.T) {
	m, ups := openSetup(t)
	click(t, m, "run") // sem config
	if ups() != 0 || m.setup.showRun {
		t.Fatal("executou sem plano pronto")
	}
}

// um Setup disparado fora da TUI (CLI, MCP) aparece na aba aberta
func TestSetupDisparadoPorForaApareceNaAba(t *testing.T) {
	m, _ := openSetup(t)
	fill(t, m, "trd.localhost")
	click(t, m, "save")
	waitFor(t, m, "Pronto pra executar")

	if _, err := m.api.RunSetup(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, m, "Execucao")
	waitFor(t, m, "✓ Subir woodpecker")
}

func openSetupGitHub(t *testing.T) (*model, *githubtest.Server) {
	t.Helper()
	gh := githubtest.New(t, 5512022, map[string]map[string]string{
		"trdSystemDev/loja": {"Dockerfile": "FROM nginx\nEXPOSE 80\n"},
	})
	root := t.TempDir()
	c := core.New(core.Deps{CheckDocker: okChecker, ConfigPath: filepath.Join(root, "config.yaml"),
		SecretsDir: filepath.Join(root, "secrets"), AppsDir: filepath.Join(root, "apps"), GitHubURL: gh.URL,
		ComposePS: func(context.Context, string) ([]docker.Container, error) { return nil, nil }})
	m := open(t, c)
	click(t, m, "tab:Setup")
	waitFor(t, m, "Ainda sem configuracao")
	fill(t, m, "trd.localhost")
	m.setup.get("ghAppID").input.SetValue("5512022")
	return m, gh
}

// a chave colada no campo de uma linha (quebras viram espaco) e refeita e
// gravada; o import pelo GitHub passa a funcionar
func TestSetupGravaAChaveDoGitHubColada(t *testing.T) {
	m, gh := openSetupGitHub(t)
	m.setup.get("ghKey").input.SetValue(strings.ReplaceAll(string(gh.Key), "\n", " "))
	click(t, m, "save")
	waitFor(t, m, "chave do GitHub App gravada")
	if v := m.setup.get("ghKey").input.Value(); v != "" {
		t.Error("o campo da chave nao foi limpo depois de gravar")
	}
	if strings.Contains(screen(m), "PRIVATE KEY") {
		t.Fatal("a chave apareceu na tela")
	}
	plan, err := m.api.PlanAppFromGitHub(context.Background(), "trdSystemDev/loja")
	if err != nil || plan.App != "loja" {
		t.Fatalf("import com a chave gravada pela TUI: %+v %v", plan, err)
	}
}

func TestSetupGravaAChaveDoGitHubPeloCaminho(t *testing.T) {
	m, gh := openSetupGitHub(t)
	file := filepath.Join(t.TempDir(), "cais.private-key.pem")
	os.WriteFile(file, gh.Key, 0o600)
	m.setup.get("ghKey").input.SetValue(file)
	click(t, m, "save")
	waitFor(t, m, "chave do GitHub App gravada")
	if _, err := m.api.PlanAppFromGitHub(context.Background(), "trdSystemDev/loja"); err != nil {
		t.Fatal(err)
	}
}

func TestSetupRecusaChaveInvalida(t *testing.T) {
	m, _ := openSetupGitHub(t)
	m.setup.get("ghKey").input.SetValue("-----BEGIN RSA PRIVATE KEY----- nao e base64 -----END RSA PRIVATE KEY-----")
	click(t, m, "save")
	waitFor(t, m, "chave do GitHub App invalida")
	// nada foi gravado: nem a config
	if info, _ := m.api.LoadConfig(context.Background()); info.Exists {
		t.Fatal("gravou a config com a chave invalida")
	}
}
