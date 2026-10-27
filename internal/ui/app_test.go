package ui

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/docker"
	"github.com/trdSystemDev/cais/internal/rpc"
)

func okChecker(context.Context) docker.Status {
	return docker.Status{ClientVersion: "29.0.1", ServerVersion: "29.0.1", ComposeVersion: "2.40.0"}
}

func missingChecker(context.Context) docker.Status {
	return docker.Status{
		ClientErr:  docker.ErrNotInstalled,
		ServerErr:  docker.ErrNotInstalled,
		ComposeErr: docker.ErrNotInstalled,
	}
}

// testCore e um core com caminhos temporarios e docker falso: o Setup roda
// de verdade (secrets, compose no disco), so os comandos do docker nao.
func testCore(t *testing.T, check func(context.Context) docker.Status) (*core.Core, *atomic.Int32) {
	t.Helper()
	root := t.TempDir()
	var ups atomic.Int32
	c := core.New(core.Deps{
		CheckDocker: check,
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

// open monta a TUI sobre uma core.API, como o Run faz, numa tela de 130x34.
// O teste faz o papel do Bubble Tea: entrega as fotos do core e executa os
// comandos.
func open(t *testing.T, api core.API) *model {
	t.Helper()
	states, unsub, err := api.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unsub)
	m := newModel(api, states)
	t.Cleanup(m.zones.Close)
	m.Update(tea.WindowSizeMsg{Width: 130, Height: 34})
	// o Subscribe entrega a foto atual na hora; o programa real a recebe
	// logo no Init
	m.Update(stateMsg(<-states))
	run(t, m, m.checkCmd())
	run(t, m, m.setup.load())
	return m
}

// run executa um comando e entrega a mensagem ao modelo, seguindo os
// comandos que ela gerar (menos a espera de estado, que o teste conduz).
func run(t *testing.T, m *model, cmd tea.Cmd) tea.Msg {
	t.Helper()
	var last tea.Msg
	for cmd != nil {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				last = run(t, m, c)
			}
			return last
		}
		if msg == nil {
			return last
		}
		last = msg
		_, cmd = m.Update(msg)
		if _, ok := msg.(stateMsg); ok {
			return last
		}
	}
	return last
}

func screen(m *model) string { return ansi.Strip(m.View().Content) }

// waitFor entrega as fotos do core ate o texto aparecer na tela.
func waitFor(t *testing.T, m *model, text string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for !strings.Contains(screen(m), text) {
		select {
		case s, ok := <-m.states:
			if !ok {
				m.Update(closedMsg{})
				if !strings.Contains(screen(m), text) {
					t.Fatalf("conexao fechou e %q nao apareceu:\n%s", text, screen(m))
				}
				return
			}
			m.Update(stateMsg(s))
		case <-deadline:
			t.Fatalf("texto %q nao apareceu na tela:\n%s", text, screen(m))
		}
	}
}

// click clica no meio da zona, com as coordenadas reais da tela. O
// bubblezone registra as zonas numa goroutine depois do View.
func click(t *testing.T, m *model, id string) tea.Msg {
	t.Helper()
	m.View()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if z := m.zones.Get(id); z != nil && !z.IsZero() {
			_, cmd := m.Update(tea.MouseReleaseMsg{X: (z.StartX + z.EndX) / 2, Y: z.StartY, Button: tea.MouseLeft})
			return run(t, m, cmd)
		}
		if time.Now().After(deadline) {
			t.Fatalf("zona %q nao esta na tela:\n%s", id, screen(m))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func press(t *testing.T, m *model, text string) tea.Msg {
	t.Helper()
	_, cmd := m.Update(tea.KeyPressMsg{Code: rune(text[0]), Text: text})
	return run(t, m, cmd)
}

func TestDashboardMostraDockerOK(t *testing.T) {
	c, _ := testCore(t, okChecker)
	m := open(t, c)
	waitFor(t, m, "Pronto.")
	waitFor(t, m, "29.0.1")
	waitFor(t, m, "2.40.0")
}

func TestDashboardMostraDockerAusente(t *testing.T) {
	c, _ := testCore(t, missingChecker)
	m := open(t, c)
	waitFor(t, m, "docker nao encontrado")
	waitFor(t, m, "Daemon   depende do CLI")
	waitFor(t, m, "Resolva os itens em vermelho")
}

// a tela tem que refletir uma acao disparada fora dela -- e o caso do MCP
// publicando um app enquanto alguem esta com a TUI aberta
func TestUIReageAAcaoExternaNoCore(t *testing.T) {
	var installed atomic.Bool
	c, _ := testCore(t, func(ctx context.Context) docker.Status {
		if installed.Load() {
			return okChecker(ctx)
		}
		return missingChecker(ctx)
	})
	m := open(t, c)
	waitFor(t, m, "docker nao encontrado")

	installed.Store(true)
	c.CheckEnvironment(context.Background()) // ninguem clicou em nada
	waitFor(t, m, "Pronto.")
}

func TestBotaoVerificarDisparaAcao(t *testing.T) {
	var calls atomic.Int32
	c, _ := testCore(t, func(ctx context.Context) docker.Status {
		calls.Add(1)
		return okChecker(ctx)
	})
	m := open(t, c)
	waitFor(t, m, "Pronto.")
	click(t, m, "check")
	if n := calls.Load(); n < 2 {
		t.Fatalf("esperava a checagem inicial + a do clique, houve %d", n)
	}
}

// ponta a ponta: daemon real no socket, TUI como um cliente e um segundo
// cliente fazendo o papel do MCP
func TestTUIViaDaemonReageAOutroCliente(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cais.sock")
	ln, err := rpc.Listen(path, "")
	if err != nil {
		t.Fatal(err)
	}
	var installed atomic.Bool
	daemonCore, _ := testCore(t, func(ctx context.Context) docker.Status {
		if installed.Load() {
			return okChecker(ctx)
		}
		return missingChecker(ctx)
	})
	ctx, stopDaemon := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- rpc.Serve(ctx, ln, daemonCore) }()
	defer func() { stopDaemon(); <-served }()

	tuiClient, err := rpc.Dial(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer tuiClient.Close()
	m := open(t, tuiClient)
	waitFor(t, m, "docker nao encontrado")

	mcp, err := rpc.Dial(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer mcp.Close()
	installed.Store(true)
	if _, err := mcp.CheckEnvironment(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, m, "Pronto.")

	// daemon cai com a TUI aberta: ela tem que avisar em vez de mostrar
	// dados velhos como se estivessem certos
	stopDaemon()
	waitFor(t, m, "A conexao com o daemon do cais caiu.")
	waitFor(t, m, "Veja: systemctl status cais")
	if _, ok := click(t, m, "quit").(tea.QuitMsg); !ok {
		t.Fatal("Sair do aviso nao saiu")
	}
}

func TestCliqueTrocaDeAba(t *testing.T) {
	c, _ := testCore(t, okChecker)
	m := open(t, c)
	waitFor(t, m, "Pronto.")

	click(t, m, "tab:Apps")
	waitFor(t, m, "chega na fase 3")

	click(t, m, "tab:Setup")
	waitFor(t, m, "Configuracao")
}

func TestAtalhoNumericoTrocaDeAba(t *testing.T) {
	c, _ := testCore(t, okChecker)
	m := open(t, c)
	waitFor(t, m, "Pronto.")

	press(t, m, "4")
	waitFor(t, m, "Databases e usuarios")
}

// num campo de texto, numero e digitacao, nao troca de aba
func TestNumeroNoCampoNaoTrocaDeAba(t *testing.T) {
	c, _ := testCore(t, okChecker)
	m := open(t, c)
	press(t, m, "6")
	click(t, m, "domain")
	press(t, m, "4")
	if m.tab != tabSetup {
		t.Fatalf("trocou pra aba %d digitando no campo", m.tab)
	}
	if v := m.setup.get("domain").input.Value(); !strings.HasSuffix(v, "4") {
		t.Fatalf("campo = %q, o 4 tinha que entrar nele", v)
	}
}

func TestSairPeloBotaoEPeloAtalho(t *testing.T) {
	c, _ := testCore(t, okChecker)
	m := open(t, c)
	if _, ok := click(t, m, "quit").(tea.QuitMsg); !ok {
		t.Fatal("clique em Sair nao saiu")
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl})
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("ctrl+q nao saiu")
	}
}
