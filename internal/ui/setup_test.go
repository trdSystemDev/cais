package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trdSystemDev/cais/internal/apps"
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

// fill preenche o formulario do Setup como o usuario faria: e-mail e CA
// local (dispensa DNS) com o dominio do teste.
func fill(t *testing.T, m *model, domain string) {
	t.Helper()
	m.setup.get("email").input.SetValue("admin@exemplo.com.br")
	if m.setup.tls == 0 {
		click(t, m, "tls")
	}
	m.setup.get("testDomain").input.SetValue(domain)
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
	if !info.Exists || info.Config.TestDomain != "trd.localhost" || info.Config.LetsEncrypt() {
		t.Fatalf("config gravada: %+v", info)
	}
	if len(info.Config.Stacks) != 0 {
		t.Errorf("Setup ligou servico sozinho: %v", info.Config.Stacks)
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
	waitFor(t, m, "✓ Subir proxy")
	if n := ups(); n != 1 {
		t.Errorf("%d compose up, esperava 1 (proxy)", n)
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
	waitFor(t, m, "✓ Subir proxy")
}

func openSetupGitHub(t *testing.T) (*model, *githubtest.Server) {
	t.Helper()
	gh := githubtest.New(t, 5512022, map[string]map[string]string{
		"trdSystemDev/loja": {"docker-compose.yml": "services: {}\n"},
	})
	root := t.TempDir()
	c := core.New(core.Deps{CheckDocker: okChecker, ConfigPath: filepath.Join(root, "config.yaml"),
		SecretsDir: filepath.Join(root, "secrets"), AppsDir: filepath.Join(root, "apps"), GitHubURL: gh.URL,
		AppDocker: &fakeApps{up: map[string]bool{}},
		AppSettle: -1,
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
	deployWithKey(t, m)
}

// o deploy baixa o codigo pelo GitHub App com a chave gravada pela TUI
func deployWithKey(t *testing.T, m *model) {
	t.Helper()
	ctx := context.Background()
	if _, err := m.api.CreateApp(ctx, apps.App{Name: "loja", Repo: "trdSystemDev/loja"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.api.DeployApp(ctx, "loja", ""); err != nil {
		t.Fatalf("deploy com a chave gravada pela TUI: %v", err)
	}
}

func TestSetupGravaAChaveDoGitHubPeloCaminho(t *testing.T) {
	m, gh := openSetupGitHub(t)
	file := filepath.Join(t.TempDir(), "cais.private-key.pem")
	os.WriteFile(file, gh.Key, 0o600)
	m.setup.get("ghKey").input.SetValue(file)
	click(t, m, "save")
	waitFor(t, m, "chave do GitHub App gravada")
	deployWithKey(t, m)
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

// os avisos por e-mail: remetente, destinatarios e a chave da Brevo saem
// do mesmo Salvar; a chave some do campo depois de gravada
func TestSetupAvisosPorEmail(t *testing.T) {
	m, _ := openSetup(t)
	fill(t, m, "trd.localhost")
	m.setup.get("notifyFrom").input.SetValue("cais@exemplo.com.br")
	m.setup.get("notifyTo").input.SetValue("a@exemplo.com.br, b@exemplo.com.br")
	m.setup.get("brevoKey").input.SetValue("xkeysib-teste")
	click(t, m, "save")
	waitFor(t, m, "Config salva e chave da Brevo gravada.")
	if !strings.Contains(screen(m), "Testar e-mail") {
		t.Errorf("sem o botao de teste:\n%s", screen(m))
	}
	if v := m.setup.get("brevoKey").input.Value(); v != "" {
		t.Errorf("chave ficou no campo: %q", v)
	}
	info, _ := m.api.LoadConfig(context.Background())
	if n := info.Config.Notify; n.From != "cais@exemplo.com.br" || len(n.To) != 2 || n.To[1] != "b@exemplo.com.br" {
		t.Fatalf("avisos gravados: %+v", n)
	}
}

// nao ha dominio da VPS: com Let's Encrypt so o e-mail basta, e o dominio
// do teste so aparece (e so conta no Tab) com a CA local
func TestSetupSemDominio(t *testing.T) {
	m, _ := openSetup(t)
	if strings.Contains(screen(m), "Dominio (teste)") {
		t.Fatal("dominio do teste aparece com Let's Encrypt")
	}
	m.setup.setFocus(0)
	for range len(m.setup.items) {
		if m.setup.items[m.setup.focus].id == "testDomain" {
			t.Fatal("o Tab passa pelo dominio do teste escondido")
		}
		m.setup.setFocus(m.setup.focus + 1)
	}
	m.setup.get("email").input.SetValue("admin@exemplo.com.br")
	click(t, m, "save")
	waitFor(t, m, "Config salva.")
	waitFor(t, m, "Pronto pra executar")

	click(t, m, "tls")
	waitFor(t, m, "Dominio (teste)")
	click(t, m, "save")
	waitFor(t, m, "precisa do dominio do")
}
