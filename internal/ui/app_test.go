package ui

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/trdSystemDev/vpsctl/internal/core"
	"github.com/trdSystemDev/vpsctl/internal/docker"
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

// startSim sobe a App numa tela simulada 100x30 e devolve a tela pra
// inspecao. A App e parada no fim do teste.
func startSim(t *testing.T, check func(context.Context) docker.Status) tcell.SimulationScreen {
	t.Helper()
	sim, _ := startSimCore(t, check)
	return sim
}

// startSimCore e como startSim, mas devolve tambem o core, pra testes que
// disparam acoes por fora da interface (como o MCP vai fazer).
func startSimCore(t *testing.T, check func(context.Context) docker.Status) (tcell.SimulationScreen, *core.Core) {
	t.Helper()
	c := core.New(core.Deps{CheckDocker: check})
	a := New(c)
	sim := tcell.NewSimulationScreen("UTF-8")
	a.app.SetScreen(sim)
	sim.SetSize(100, 30)

	done := make(chan error, 1)
	go func() { done <- a.Run() }()
	t.Cleanup(func() {
		a.app.Stop()
		if err := <-done; err != nil {
			t.Errorf("Run devolveu erro: %v", err)
		}
	})
	return sim, c
}

func screenText(sim tcell.SimulationScreen) []string {
	cells, w, h := sim.GetContents()
	rows := make([]string, h)
	for y := 0; y < h; y++ {
		var b strings.Builder
		for x := 0; x < w; x++ {
			c := cells[y*w+x]
			if len(c.Runes) > 0 {
				b.WriteString(string(c.Runes))
			} else {
				b.WriteByte(' ')
			}
		}
		rows[y] = b.String()
	}
	return rows
}

// waitFor espera ate o texto aparecer em alguma linha da tela.
func waitFor(t *testing.T, sim tcell.SimulationScreen, text string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, row := range screenText(sim) {
			if strings.Contains(row, text) {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("texto %q nao apareceu na tela:\n%s", text, strings.Join(screenText(sim), "\n"))
}

// click simula um clique (aperta e solta) do botao esquerdo no texto
// informado, procurando-o na linha y.
func click(t *testing.T, sim tcell.SimulationScreen, y int, text string) {
	t.Helper()
	x := strings.Index(screenText(sim)[y], text)
	if x < 0 {
		t.Fatalf("texto %q nao encontrado na linha %d", text, y)
	}
	sim.InjectMouse(x, y, tcell.Button1, tcell.ModNone)
	sim.InjectMouse(x, y, tcell.ButtonNone, tcell.ModNone)
}

func TestDashboardMostraDockerOK(t *testing.T) {
	sim := startSim(t, okChecker)
	waitFor(t, sim, "Pronto.")
	waitFor(t, sim, "29.0.1")
	waitFor(t, sim, "2.40.0")
}

func TestDashboardMostraDockerAusente(t *testing.T) {
	sim := startSim(t, missingChecker)
	waitFor(t, sim, "docker nao encontrado")
	waitFor(t, sim, "Daemon   depende do CLI")
	waitFor(t, sim, "Resolva os itens em vermelho")
}

// a tela tem que refletir uma acao disparada fora dela -- e o caso do MCP
// publicando um app enquanto alguem esta com a TUI aberta
func TestUIReageAAcaoExternaNoCore(t *testing.T) {
	var installed atomic.Bool
	sim, c := startSimCore(t, func(ctx context.Context) docker.Status {
		if installed.Load() {
			return okChecker(ctx)
		}
		return missingChecker(ctx)
	})
	waitFor(t, sim, "docker nao encontrado")

	installed.Store(true)
	c.CheckEnvironment(context.Background()) // ninguem clicou em nada
	waitFor(t, sim, "Pronto.")
}

func TestBotaoVerificarDisparaAcao(t *testing.T) {
	var calls atomic.Int32
	sim := startSim(t, func(ctx context.Context) docker.Status {
		calls.Add(1)
		return okChecker(ctx)
	})
	waitFor(t, sim, "Pronto.")

	rows := screenText(sim)
	for y, row := range rows {
		if strings.Contains(row, "Verificar novamente") {
			click(t, sim, y, "Verificar novamente")
			break
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := calls.Load(); n < 2 {
		t.Fatalf("esperava a checagem inicial + a do clique, houve %d", n)
	}
}

func TestCliqueTrocaDeAba(t *testing.T) {
	sim := startSim(t, okChecker)
	waitFor(t, sim, "Pronto.")

	click(t, sim, 0, "Apps")
	waitFor(t, sim, "chega na fase 3")

	click(t, sim, 0, "Setup")
	waitFor(t, sim, "Assistente que prepara uma VPS vazia")
}

func TestAtalhoNumericoTrocaDeAba(t *testing.T) {
	sim := startSim(t, okChecker)
	waitFor(t, sim, "Pronto.")

	sim.InjectKey(tcell.KeyRune, '4', tcell.ModNone)
	waitFor(t, sim, "Databases e usuarios")
}
