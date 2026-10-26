package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

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
func startSim(t *testing.T, check DockerChecker) tcell.SimulationScreen {
	t.Helper()
	a := New(check)
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
	return sim
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
