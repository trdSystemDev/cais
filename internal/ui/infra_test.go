package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/docker"
)

func TestInfraSelecionaContainerEIgnoraLogsAntigos(t *testing.T) {
	c, _, _ := testCore(t, okChecker)
	m := open(t, c)
	m.tab = tabInfra
	m.state.Infra = core.Infra{CheckedAt: time.Now(), Stacks: []core.StackStatus{
		{Name: "proxy", Containers: []docker.Container{{Name: "proxy"}, {Name: "socket-proxy"}}},
	}}
	run(t, m, m.syncInfraLogs(true))
	if !strings.Contains(m.logsText, "container iniciado") {
		t.Fatal("logs nao carregaram")
	}
	old := m.logsGeneration
	run(t, m, m.infraKey("right"))
	if m.logsTarget != "socket-proxy" {
		t.Fatal("nao trocou container")
	}
	m.Update(infraLogsMsg{generation: old, text: "resposta antiga"})
	if strings.Contains(m.logsText, "antiga") {
		t.Fatal("resposta antiga sobrescreveu selecao")
	}
	run(t, m, m.setTab(tabDashboard))
	if _, cmd := m.Update(infraLogsTick(m.logsGeneration)); cmd != nil {
		t.Fatal("continuou consultando fora da Infra")
	}
}

func TestInfraRespeitaTamanhoDoTerminal(t *testing.T) {
	c, _, _ := testCore(t, okChecker)
	m := open(t, c)
	m.tab = tabInfra
	m.logsText = strings.Repeat("log longo ", 200) + "\x1b[2J\n" + strings.Repeat("mais logs\n", 200)
	for _, size := range []tea.WindowSizeMsg{{Width: 130, Height: 34}, {Width: 90, Height: 24}, {Width: 72, Height: 18}} {
		m.Update(size)
		out := m.viewInfra(size.Height - 4)
		if lipgloss.Width(out) > size.Width || lipgloss.Height(out) > size.Height-4 {
			t.Fatalf("terminal %dx%d, painel %dx%d", size.Width, size.Height, lipgloss.Width(out), lipgloss.Height(out))
		}
	}
}

func TestInfraOperacaoFicaRestritaAoServico(t *testing.T) {
	for _, on := range []bool{true, false} {
		for _, remote := range []bool{true, false} {
			c, _, _ := testCore(t, okChecker)
			m := open(t, c)
			m.tab = tabInfra
			m.state.Infra = core.Infra{CheckedAt: time.Now(), Stacks: []core.StackStatus{
				{Name: "proxy", Enabled: true, Installed: true, Containers: []docker.Container{{Name: "proxy", State: "running"}}},
				{Name: "mysql", Enabled: true, Installed: true, Containers: []docker.Container{{Name: "mysql", State: "running"}}},
			}}
			m.state.Setup = core.Setup{Running: true, Service: "mysql", ServiceOn: on}
			if !remote {
				m.toggling = "mysql"
				m.togglingOn = on
			}
			m.infraMsgStack = "mysql"
			m.infraMsg = "aviso exclusivo do mysql"
			m.logsText = "logs normais do proxy"
			out := screen(m)
			if !strings.Contains(out, "logs normais do proxy") || strings.Contains(out, "aviso exclusivo") || strings.Contains(out, "Iniciando container") || strings.Contains(out, "Derrubando container") {
				t.Fatalf("operacao vazou no proxy: %s", out)
			}
			if m.toggle("postgres") != nil {
				t.Fatal("permitiu outra operacao enquanto ocupado")
			}
			for i, name := range infraOrder() {
				if name == "mysql" {
					m.infraSel = i
				}
			}
			out = screen(m)
			title, status := "Derrubando container", "desligando..."
			if on {
				title, status = "Iniciando container", "ligando..."
			}
			if !strings.Contains(out, title) || !strings.Contains(out, status) || strings.Contains(out, "logs normais") {
				t.Fatalf("sem modal/card de progresso: %s", out)
			}
			for _, size := range []tea.WindowSizeMsg{{Width: 130, Height: 34}, {Width: 90, Height: 24}, {Width: 72, Height: 18}} {
				m.Update(size)
				panel := m.viewInfra(size.Height - 4)
				if lipgloss.Width(panel) > size.Width || lipgloss.Height(panel) > size.Height-4 {
					t.Fatal("modal ultrapassou painel")
				}
			}
			m.toggling = ""
			m.state.Setup.Running = false
			m.infraSel = 0
			if strings.Contains(screen(m), "aviso exclusivo") {
				t.Fatal("aviso final vazou para outro card")
			}
		}
	}
}

func TestInfraContainerAguardandoHealthcheck(t *testing.T) {
	c, _, _ := testCore(t, okChecker)
	m := open(t, c)
	m.tab = tabInfra
	m.state.Infra.Stacks = []core.StackStatus{{Name: "proxy", Enabled: true, Installed: true, Containers: []docker.Container{{Name: "traefik", State: "running", Health: "starting"}}}}
	out := screen(m)
	if !strings.Contains(out, "ligando...") || !strings.Contains(out, "Iniciando container") || !strings.Contains(out, "Aguardando traefik") {
		t.Fatalf("nao reconheceu healthcheck: %s", out)
	}
	m.state.Infra.Stacks[0].Containers[0].Health = "healthy"
	if strings.Contains(screen(m), "Iniciando container") {
		t.Fatal("modal nao saiu apos ficar saudavel")
	}
}
