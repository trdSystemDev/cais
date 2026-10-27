package ui

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trdSystemDev/cais/internal/core"
)

// setupCore e um core com caminhos temporarios e docker falso: o Setup roda
// de verdade (secrets, compose no disco), so os comandos do docker nao.
func setupCore(t *testing.T) (*core.Core, *atomic.Int32) {
	t.Helper()
	root := t.TempDir()
	var ups atomic.Int32
	c := core.New(core.Deps{
		CheckDocker: okChecker,
		ConfigPath:  filepath.Join(root, "config.yaml"),
		SecretsDir:  filepath.Join(root, "secrets"),
		StackDir:    func(n string) string { return filepath.Join(root, "stacks", n) },
		ComposeUp: func(context.Context, string, bool) error {
			ups.Add(1)
			return nil
		},
		ComposeDown: func(context.Context, string) error { return nil },
		Login:       func(context.Context, string, string, []byte) error { return nil },
	})
	return c, &ups
}

// openSetup sobe a TUI e abre a aba Setup.
func openSetup(t *testing.T, c core.API) (*simScreen, *App) {
	t.Helper()
	sim, a := startSimApp(t, c, 130, 34)
	waitFor(t, sim, "Pronto.") // dashboard carregou
	click(t, sim, 0, "Setup")
	return sim, a
}

// fillForm preenche o formulario como o usuario faria, na goroutine da UI.
func fillForm(a *App, domain string, stacks ...string) {
	a.app.QueueUpdateDraw(func() {
		p := a.setup
		p.domain.SetText(domain)
		p.email.SetText("admin@exemplo.com.br")
		p.tls.SetCurrentOption(1) // CA local: dispensa DNS
		for name, cb := range p.stacks {
			cb.SetChecked(false)
			for _, s := range stacks {
				if s == name {
					cb.SetChecked(true)
				}
			}
		}
	})
}

func TestSetupSemConfigPedePraPreencher(t *testing.T) {
	c, _ := setupCore(t)
	sim, _ := openSetup(t, c)
	waitFor(t, sim, "Ainda sem configuracao")
	// padrao ja vem marcado no form: proxy, bancos, registry, woodpecker
	waitFor(t, sim, "postgres         [x]")
}

func TestSetupSalvaPeloFormularioEMostraOPlano(t *testing.T) {
	c, _ := setupCore(t)
	sim, a := openSetup(t, c)
	waitFor(t, sim, "Ainda sem configuracao")

	fillForm(a, "trd.localhost", "proxy", "postgres", "registry")
	clickText(t, sim, "Salvar     Executar") // o botao, nao o "Salvar" do texto do plano
	waitFor(t, sim, "Config salva.")
	waitFor(t, sim, "Pronto pra executar.")
	waitFor(t, sim, "ssh -N -L 5432:127.0.0.1:5432")

	info, err := c.LoadConfig(context.Background())
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
	c, _ := setupCore(t)
	sim, a := openSetup(t, c)
	waitFor(t, sim, "Ainda sem configuracao")

	fillForm(a, "trd.local", "proxy", "postgres")
	clickText(t, sim, "Salvar     Executar") // o botao, nao o "Salvar" do texto do plano
	waitFor(t, sim, "reservado pro mDNS")

	if info, _ := c.LoadConfig(context.Background()); info.Exists {
		t.Fatal("gravou config invalida")
	}
}

func TestSetupExecutaPeloBotao(t *testing.T) {
	c, ups := setupCore(t)
	sim, a := openSetup(t, c)
	waitFor(t, sim, "Ainda sem configuracao")

	fillForm(a, "trd.localhost", "proxy", "postgres", "registry")
	clickText(t, sim, "Salvar     Executar") // o botao, nao o "Salvar" do texto do plano
	waitFor(t, sim, "Pronto pra executar.")

	clickText(t, sim, "Executar setup")
	waitFor(t, sim, "Setup concluido.")
	waitFor(t, sim, "ok Login da VPS em registry.trd.localhost")
	if n := ups.Load(); n != 3 {
		t.Errorf("%d compose up, esperava 3", n)
	}
}

// um Setup disparado fora da TUI (CLI, MCP) aparece na aba aberta
func TestSetupDisparadoPorForaApareceNaAba(t *testing.T) {
	c, _ := setupCore(t)
	sim, a := openSetup(t, c)
	waitFor(t, sim, "Ainda sem configuracao")
	fillForm(a, "trd.localhost", "proxy", "postgres")
	clickText(t, sim, "Salvar     Executar") // o botao, nao o "Salvar" do texto do plano
	waitFor(t, sim, "Pronto pra executar.")

	done := make(chan error, 1)
	go func() {
		_, err := c.RunSetup(context.Background())
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Setup nao terminou")
	}
	waitFor(t, sim, " Execucao ")
	waitFor(t, sim, "ok Subir postgres")
}
