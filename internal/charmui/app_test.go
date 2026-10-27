package charmui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/docker"
)

func newTestModel(t *testing.T) (*model, *core.Core) {
	t.Helper()
	root := t.TempDir()
	c := core.New(core.Deps{
		CheckDocker: func(context.Context) docker.Status {
			return docker.Status{ClientVersion: "29", ServerVersion: "29", ComposeVersion: "2"}
		},
		ConfigPath:  filepath.Join(root, "config.yaml"),
		SecretsDir:  filepath.Join(root, "secrets"),
		StackDir:    func(n string) string { return filepath.Join(root, "stacks", n) },
		ComposeUp:   func(context.Context, string, bool) error { return nil },
		ComposeDown: func(context.Context, string) error { return nil },
		Login:       func(context.Context, string, string, []byte) error { return nil },
	})
	states, unsub, _ := c.Subscribe(context.Background())
	t.Cleanup(unsub)
	m := newModel(c, states)
	t.Cleanup(m.zones.Close)
	m.Update(tea.WindowSizeMsg{Width: 130, Height: 34})
	run(t, m, m.loadCmd())
	return m, c
}

// run executa um comando como o Bubble Tea faria (sincrono no teste) e
// entrega a mensagem ao modelo, seguindo os comandos que ela gerar.
func run(t *testing.T, m *model, cmd tea.Cmd) tea.Msg {
	t.Helper()
	var last tea.Msg
	for cmd != nil {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				if c != nil {
					last = run(t, m, c)
				}
			}
			return last
		}
		if _, ok := msg.(stateMsg); ok {
			return msg // a assinatura do estado fica esperando pra sempre
		}
		last = msg
		_, cmd = m.Update(msg)
	}
	return last
}

// click desenha a tela, espera o bubblezone registrar a zona e clica no
// meio dela, com as coordenadas reais da tela.
func click(t *testing.T, m *model, id string) tea.Msg {
	t.Helper()
	m.View()
	deadline := time.Now().Add(2 * time.Second)
	for {
		z := m.zones.Get(id)
		if z != nil && !z.IsZero() {
			msg := tea.MouseReleaseMsg{X: (z.StartX + z.EndX) / 2, Y: z.StartY, Button: tea.MouseLeft}
			_, cmd := m.Update(msg)
			return run(t, m, cmd)
		}
		if time.Now().After(deadline) {
			t.Fatalf("zona %q nao apareceu na tela", id)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func screen(m *model) string { return ansi.Strip(m.View().Content) }

func TestCliqueMarcaCaixaEFocaCampo(t *testing.T) {
	m, _ := newTestModel(t)
	before := m.stacks["mysql"]
	click(t, m, "stack:mysql")
	if m.stacks["mysql"] == before {
		t.Fatal("clique nao trocou a caixa do mysql")
	}
	click(t, m, "email")
	if m.items[m.focus].id != "email" {
		t.Fatalf("foco em %s, esperava email", m.items[m.focus].id)
	}
}

func TestCliqueEmSalvarGravaAConfig(t *testing.T) {
	m, c := newTestModel(t)
	m.get("domain").input.SetValue("trd.localhost")
	m.get("email").input.SetValue("admin@exemplo.com.br")
	click(t, m, "tls") // CA local
	click(t, m, "stack:woodpecker")
	click(t, m, "save")

	if !strings.Contains(screen(m), "Config salva.") {
		t.Fatalf("tela depois de salvar:\n%s", screen(m))
	}
	info, _ := c.LoadConfig(context.Background())
	if !info.Exists || info.Config.Domain != "trd.localhost" || info.Config.LetsEncrypt() || info.Config.HasStack("woodpecker") {
		t.Fatalf("config gravada: %+v", info.Config)
	}
	if !strings.Contains(screen(m), "Pronto pra executar") {
		t.Fatalf("plano nao atualizou:\n%s", screen(m))
	}
}

func TestErroDeValidacaoAparece(t *testing.T) {
	m, c := newTestModel(t)
	m.get("domain").input.SetValue("trd.local")
	m.get("email").input.SetValue("admin@exemplo.com.br")
	click(t, m, "save")
	if !strings.Contains(screen(m), "reservado pro mDNS") {
		t.Fatalf("erro nao apareceu:\n%s", screen(m))
	}
	if info, _ := c.LoadConfig(context.Background()); info.Exists {
		t.Fatal("gravou config invalida")
	}
}

func TestCliqueNaAbaENoSair(t *testing.T) {
	m, _ := newTestModel(t)
	click(t, m, "tab:Apps")
	if m.tab != 1 {
		t.Fatalf("aba %d, esperava Apps", m.tab)
	}
	if _, ok := click(t, m, "quit").(tea.QuitMsg); !ok {
		t.Fatal("clique em Sair nao saiu")
	}
}
