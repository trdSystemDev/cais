package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

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
	if !strings.Contains(screen(m), "ligue o proxy") {
		t.Errorf("Setup nao aponta pra aba Infra:\n%s", screen(m))
	}
}

func TestSetupSalvaPeloFormularioEMostraOPlano(t *testing.T) {
	m, _ := openSetup(t)
	fill(t, m, "trd.localhost")
	click(t, m, "save")
	waitFor(t, m, "Config salva.")
	waitFor(t, m, "Nenhum servico ligado")

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

// Salvar so grava a config: nada sobe, nem o proxy, e nao ha botao de
// executar; os servicos se ligam na aba Infra
func TestSetupSalvarNaoSobeNada(t *testing.T) {
	m, ups := openSetup(t)
	fill(t, m, "trd.localhost")
	click(t, m, "save")
	waitFor(t, m, "Config salva.")
	waitFor(t, m, "Nenhum servico ligado")
	if n := ups(); n != 0 {
		t.Fatalf("salvar subiu %d stacks", n)
	}
	if z := m.zones.Get("run"); z != nil && !z.IsZero() {
		t.Fatal("a aba Setup ainda tem o botao de executar")
	}
	if strings.Contains(screen(m), "Executar") {
		t.Fatalf("a aba Setup ainda fala em executar:\n%s", screen(m))
	}
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
	m.setup.get("brevoKey").input.SetValue("xkeysib-teste")
	// os avisos tem sub-aba propria; o Salvar dela grava o formulario todo
	click(t, m, "setup-sec:Avisos")
	waitFor(t, m, "nenhum destinatario")
	// os destinatarios sao uma lista, como as variaveis de um app: cada
	// "+ Novo e-mail" abre uma linha com o excluir ao lado
	for _, addr := range []string{"a@exemplo.com.br", "x@exemplo.com.br", "b@exemplo.com.br"} {
		click(t, m, "to-add")
		if !m.setup.typing() {
			t.Fatal("o + Novo e-mail nao levou o foco pra linha nova")
		}
		m.setup.to[len(m.setup.to)-1].SetValue(addr)
	}
	waitFor(t, m, "✕ excluir")
	click(t, m, "to-del:1")
	if len(m.setup.to) != 2 || m.setup.to[1].Value() != "b@exemplo.com.br" {
		t.Fatalf("excluir tirou a linha errada: %d linhas", len(m.setup.to))
	}
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
	m.setup.enter(false)
	for range len(m.setup.items) {
		if m.setup.items[m.setup.focus].id == "testDomain" {
			t.Fatal("o Tab passa pelo dominio do teste escondido")
		}
		m.setup.move(1)
	}
	m.setup.get("email").input.SetValue("admin@exemplo.com.br")
	click(t, m, "save")
	waitFor(t, m, "Config salva.")
	waitFor(t, m, "Nenhum servico ligado")

	click(t, m, "tls")
	waitFor(t, m, "Dominio (teste)")
	click(t, m, "save")
	waitFor(t, m, "precisa do dominio do")
}

// trocar a porta do proxy: Infra -> Portas no card do proxy -> Salvar e
// aplicar (o proxy sobe de novo, com o compose nas portas novas)
func TestInfraTrocaAPortaDoProxy(t *testing.T) {
	c, ups, _ := testCore(t, okChecker)
	setupDone(t, c, "proxy")
	before := ups.Load()
	m := open(t, c)
	click(t, m, "tab:Setup")
	waitFor(t, m, "Certificados")
	if strings.Contains(screen(m), "Porta HTTP") {
		t.Fatal("as portas do proxy ainda estao no Setup")
	}

	click(t, m, "tab:Infra")
	waitFor(t, m, "✓ proxy")
	// o proxy e o primeiro card: as portas ficam no Geral dele
	waitFor(t, m, "Portas do proxy")
	waitFor(t, m, "Salvar e aplicar")
	if v := m.ports.https.Value(); v != "443" {
		t.Fatalf("porta HTTPS no painel = %q", v)
	}
	click(t, m, "ports-http")
	m.ports.http.SetValue("8080")
	click(t, m, "ports-https")
	m.ports.https.SetValue("8443")
	click(t, m, "ports-save")
	waitFor(t, m, "config aplicada no proxy.")
	waitFor(t, m, ":8080/:8443")
	if ups.Load() != before+1 {
		t.Fatalf("salvar nao subiu o proxy de novo (%d -> %d)", before, ups.Load())
	}
	info, _ := c.LoadConfig(context.Background())
	if info.Config.Proxy.HTTPPort != 8080 || info.Config.Proxy.HTTPSPort != 8443 || !info.Config.HasStack("proxy") {
		t.Fatalf("config: %+v", info.Config)
	}
	plan, _ := c.PlanSetup(context.Background())
	compose, _ := os.ReadFile(plan.Stacks[0].File)
	if !strings.Contains(string(compose), `"8080:80"`) || !strings.Contains(string(compose), "entrypoint.to=:8443") {
		t.Errorf("compose do proxy sem as portas novas:\n%s", compose)
	}

	// porta invalida: nao grava e diz por que
	click(t, m, "ports-https")
	m.ports.https.SetValue("8080")
	click(t, m, "ports-save")
	waitFor(t, m, "nao podem ser iguais")
	if ups.Load() != before+1 {
		t.Fatal("porta invalida subiu o proxy")
	}

	// atras do nginx: so no loopback, e o aviso diz o que o nginx repassa
	m.ports.https.SetValue("8443")
	click(t, m, "ports-behind")
	waitFor(t, m, "[x] atras do nginx desta VPS")
	click(t, m, "ports-save")
	waitFor(t, m, ":8080/:8443 atras do nginx")
	info, _ = c.LoadConfig(context.Background())
	if !info.Config.Proxy.BehindProxy {
		t.Fatalf("nao gravou o atras do nginx: %+v", info.Config.Proxy)
	}
	compose, _ = os.ReadFile(plan.Stacks[0].File)
	if !strings.Contains(string(compose), `"127.0.0.1:8443:443"`) {
		t.Errorf("compose do proxy fora do loopback:\n%s", compose)
	}
}

// o Setup e dividido em sub-abas de tela cheia: ←→ trocam de sub-aba, tab
// entra no formulario dela, esc volta; fora dos campos o numero troca de aba
func TestSetupSubAbasPeloTeclado(t *testing.T) {
	m, _ := openSetup(t)
	key := func(code rune) { m.Update(tea.KeyPressMsg{Code: code}) }
	// o GitHub App fica no Geral; avisos e backups nas sub-abas deles
	waitFor(t, m, "GitHub App")
	if s := screen(m); strings.Contains(s, "Chave da Brevo") || strings.Contains(s, "OAuth client") || strings.Contains(s, "Acesso") {
		t.Fatalf("a sub-aba Geral mostra outras secoes:\n%s", s)
	}
	key(tea.KeyRight)
	waitFor(t, m, "Chave da Brevo")
	if strings.Contains(screen(m), "Certificados") {
		t.Fatal("Avisos ainda mostra o Geral")
	}
	key(tea.KeyTab)
	if !m.setup.typing() || m.setup.items[m.setup.focus].id != "notifyFrom" {
		t.Fatalf("tab nao entrou no Remetente: foco %q", m.setup.items[m.setup.focus].id)
	}
	// o Tab da a volta so na sub-aba: 3 campos, Salvar e Testar e-mail
	for range 5 {
		key(tea.KeyTab)
	}
	if id := m.setup.items[m.setup.focus].id; id != "notifyFrom" {
		t.Fatalf("o Tab saiu da sub-aba: foco %q", id)
	}
	key(tea.KeyEscape)
	if m.setup.typing() {
		t.Fatal("esc nao saiu do formulario")
	}
	key(tea.KeyRight)
	waitFor(t, m, "OAuth client do Google")
	key(tea.KeyRight)
	waitFor(t, m, "Certificados")
	press(t, m, "1")
	if m.tab != tabDashboard {
		t.Fatalf("fora dos campos o 1 nao trocou de aba: %d", m.tab)
	}
}

// o Testar do GitHub App confere o que esta no formulario, sem salvar: App
// ID e chave aceitos pelo GitHub, e as contas em que o app esta instalado
func TestSetupTestaOGitHubApp(t *testing.T) {
	m, gh := openSetupGitHub(t)
	click(t, m, "ghTest")
	waitFor(t, m, "falta a chave do GitHub App")

	m.setup.get("ghKey").input.SetValue(strings.ReplaceAll(string(gh.Key), "\n", " "))
	m.setup.get("ghAppID").input.SetValue("999")
	click(t, m, "ghTest")
	waitFor(t, m, "o GitHub recusou o App ID 999")

	m.setup.get("ghAppID").input.SetValue("5512022")
	click(t, m, "ghTest")
	waitFor(t, m, `Conectou no app "cais-teste", instalado em: trdSystemDev.`)
	if info, _ := m.api.LoadConfig(context.Background()); info.Exists {
		t.Fatal("o Testar gravou a config")
	}

	// salvo, o campo da chave fica vazio e o Testar usa a gravada
	click(t, m, "save")
	waitFor(t, m, "chave do GitHub App gravada")
	m.setup.ghMsg = ""
	click(t, m, "ghTest")
	waitFor(t, m, "instalado em: trdSystemDev.")
}
