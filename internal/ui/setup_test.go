package ui

import (
	"context"
	"testing"
)

// openSetup abre a TUI ja na aba Setup.
func openSetup(t *testing.T) (*model, func() int32) {
	t.Helper()
	c, ups := testCore(t, okChecker)
	m := open(t, c)
	click(t, m, "tab:Setup")
	waitFor(t, m, "Ainda sem configuracao")
	return m, ups.Load
}

// fill preenche dominio e e-mail e deixa so as stacks pedidas marcadas,
// clicando nas caixas como o usuario faria.
func fill(t *testing.T, m *model, domain string, stacks ...string) {
	t.Helper()
	m.setup.get("domain").input.SetValue(domain)
	m.setup.get("email").input.SetValue("admin@exemplo.com.br")
	if m.setup.tls == 0 {
		click(t, m, "tls") // CA local: dispensa DNS
	}
	want := map[string]bool{}
	for _, s := range stacks {
		want[s] = true
	}
	for name, on := range m.setup.on {
		if on != want[name] {
			click(t, m, "stack:"+name)
		}
	}
}

func TestSetupSemConfigVemComPadrao(t *testing.T) {
	m, _ := openSetup(t)
	// padrao: proxy, bancos, registry e woodpecker marcados
	for _, s := range []string{"proxy", "postgres", "mysql", "registry", "woodpecker"} {
		if !m.setup.on[s] {
			t.Errorf("%s desmarcado no padrao", s)
		}
	}
}

func TestSetupSalvaPeloFormularioEMostraOPlano(t *testing.T) {
	m, _ := openSetup(t)
	fill(t, m, "trd.localhost", "proxy", "postgres", "registry")
	click(t, m, "save")
	waitFor(t, m, "Config salva.")
	waitFor(t, m, "Pronto pra executar")
	waitFor(t, m, "ssh -N -L 5432:127.0.0.1:5432")

	info, err := m.api.LoadConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !info.Exists || info.Config.Domain != "trd.localhost" || info.Config.LetsEncrypt() {
		t.Fatalf("config gravada: %+v", info)
	}
	if got := info.Config.Stacks; len(got) != 3 || got[1] != "postgres" {
		t.Errorf("stacks gravadas %v", got)
	}
}

func TestSetupMostraErroDeValidacao(t *testing.T) {
	m, _ := openSetup(t)
	fill(t, m, "trd.local", "proxy", "postgres")
	click(t, m, "save")
	waitFor(t, m, "reservado pro mDNS")
	if info, _ := m.api.LoadConfig(context.Background()); info.Exists {
		t.Fatal("gravou config invalida")
	}
}

func TestSetupExecutaPeloBotao(t *testing.T) {
	m, ups := openSetup(t)
	fill(t, m, "trd.localhost", "proxy", "postgres", "registry")
	click(t, m, "save")
	waitFor(t, m, "Pronto pra executar")

	click(t, m, "run")
	waitFor(t, m, "Setup concluido.")
	waitFor(t, m, "✓ Login da VPS em registry.trd.localhost")
	if n := ups(); n != 3 {
		t.Errorf("%d compose up, esperava 3", n)
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
	fill(t, m, "trd.localhost", "proxy", "postgres")
	click(t, m, "save")
	waitFor(t, m, "Pronto pra executar")

	if _, err := m.api.RunSetup(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, m, "Execucao")
	waitFor(t, m, "✓ Subir postgres")
}
