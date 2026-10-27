package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/docker"
)

func TestSelecionarLogsCopiaSomenteTextoVisivel(t *testing.T) {
	c, _, _ := testCore(t, okChecker)
	m := open(t, c)
	m.tab = tabInfra
	m.state.Infra.Stacks = []core.StackStatus{{Name: "proxy", Enabled: true, Installed: true, Containers: []docker.Container{{Name: "proxy", State: "running"}}}}
	m.logsText = "primeira linha\nsegunda linha"
	m.View()
	area := m.logArea
	if len(area.lines) == 0 {
		t.Fatal("area de logs ausente")
	}
	m.Update(tea.MouseClickMsg{X: area.x, Y: area.y, Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: area.x + 6, Y: area.y + 1, Button: tea.MouseLeft})
	// Uma resposta de polling nao pode mover o trecho durante a selecao.
	m.Update(infraLogsMsg{generation: m.logsGeneration, text: "novos logs"})
	if !strings.Contains(screen(m), "primeira linha") {
		t.Fatalf("polling moveu texto selecionado: selection=%+v area=%+v\n%s", m.logSelection, area, screen(m))
	}
	_, cmd := m.Update(tea.MouseReleaseMsg{X: area.x + 6, Y: area.y + 1, Button: tea.MouseLeft})
	if cmd == nil {
		t.Fatal("nao enviou copia ao terminal")
	}
	if got := clipboardText(cmd); got != "primeira linha\nsegunda" {
		t.Fatalf("copiou %q", got)
	}
	if !strings.Contains(screen(m), "Texto copiado") {
		t.Fatal("aviso de copia nao apareceu")
	}
	m.Update(copiedNoticeExpired(m.copiedGeneration - 1))
	if !m.copiedNotice {
		t.Fatal("timer antigo removeu aviso novo")
	}
	m.Update(copiedNoticeExpired(m.copiedGeneration))
	if strings.Contains(screen(m), "Texto copiado") {
		t.Fatal("aviso nao desapareceu")
	}
	if m.logSelection.dragging {
		t.Fatal("selecao continuou arrastando")
	}
	m.infraKey("pgdown")
	if len(m.logSelection.lines) > 0 {
		t.Fatal("rolagem nao limpou selecao")
	}
}

func TestSelecaoLogsReversaEUnicode(t *testing.T) {
	s := logSelection{lines: []string{"ação inicial", "última linha"}, start: logPoint{5, 1}, end: logPoint{0, 0}}
	if got := s.text(); got != "ação inicial\núltima" {
		t.Fatalf("copiou %q", got)
	}
	s.end = s.start
	if s.text() != "" {
		t.Fatal("clique simples nao deveria copiar")
	}
}

func TestSelecionarLogsComPainelCheio(t *testing.T) {
	c, _, _ := testCore(t, okChecker)
	m := open(t, c)
	m.tab = tabInfra
	m.state.Infra.Stacks = []core.StackStatus{{Name: "proxy", Enabled: true, Installed: true, Containers: []docker.Container{{Name: "proxy-docker-proxy", State: "running"}, {Name: "proxy-traefik", State: "running"}}}}
	for _, size := range []tea.WindowSizeMsg{{Width: 180, Height: 45}, {Width: 130, Height: 34}, {Width: 90, Height: 24}} {
		m.Update(size)
		m.logsText = strings.Repeat("2026-10-27 container: "+strings.Repeat("mensagem longa ", 20)+"\n", 200)
		output := screen(m)
		found := false
		for y, line := range strings.Split(output, "\n") {
			index := strings.Index(line, "2026-")
			if index < 0 {
				continue
			}
			x := ansi.StringWidth(line[:index])
			if !m.startLogSelection(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}) {
				t.Fatalf("nao selecionou logs visiveis em %dx%d (%d,%d), area=%+v", size.Width, size.Height, x, y, m.logArea)
			}
			m.Update(tea.MouseMotionMsg{X: x + 9, Y: y, Button: tea.MouseLeft})
			if !strings.Contains(m.renderLogSelection(), "\x1b[7m") {
				t.Fatal("selecao sem destaque inverso")
			}
			_, cmd := m.Update(tea.MouseReleaseMsg{X: x + 9, Y: y, Button: tea.MouseLeft})
			if cmd == nil || clipboardText(cmd) != "2026-10-27" {
				t.Fatalf("size=%+v coords=%d,%d area=%+v selection=%+v\n%s", size, x, y, m.logArea, m.logSelection, output)
			}
			found = true
			break
		}
		if !found {
			t.Fatal("fixture nao preencheu logs")
		}
	}
}

// O primeiro comando escreve a area de transferencia; o segundo agenda o aviso.
func clipboardText(cmd tea.Cmd) string {
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		return fmt.Sprint(batch[0]())
	}
	return fmt.Sprint(msg)
}

func TestSelecaoRolaAteFimDosLogsNosDoisSentidos(t *testing.T) {
	for _, up := range []bool{true, false} {
		c, _, _ := testCore(t, okChecker)
		m := open(t, c)
		m.tab = tabInfra
		m.state.Infra.Stacks = []core.StackStatus{{Name: "proxy", Enabled: true, Installed: true, Containers: []docker.Container{{Name: "proxy", State: "running"}}}}
		var lines []string
		for i := 0; i < 100; i++ {
			lines = append(lines, fmt.Sprintf("linha %03d", i))
		}
		m.logsText = strings.Join(lines, "\n")
		if !up {
			m.logsOffset = 50
		}
		m.View()
		area := m.logArea
		x, y := area.x, area.y
		targetY := area.y + len(area.lines)
		expected := strings.Join(lines[area.start:], "\n")
		if up {
			x = area.x + 8
			y = area.y + len(area.lines) - 1
			targetY = area.y - 1
			expected = strings.Join(lines, "\n")
		}
		m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
		_, cmd := m.Update(tea.MouseMotionMsg{X: area.x, Y: targetY, Button: tea.MouseLeft})
		if cmd == nil {
			t.Fatal("nao iniciou rolagem no limite")
		}
		generation := m.selectionGeneration
		// O ponteiro fica parado na borda: o timer precisa continuar rolando.
		for ticks := 0; cmd != nil; ticks++ {
			if ticks > 100 {
				t.Fatal("rolagem nao parou no fim do buffer")
			}
			previous := m.logSelection.viewStart
			_, cmd = m.Update(logSelectionScrollTick(generation))
			if (up && m.logSelection.viewStart > previous) || (!up && m.logSelection.viewStart < previous) {
				t.Fatal("rolagem inverteu o sentido")
			}
			// Novos logs nao podem mudar o tamanho nem a posicao da captura.
			m.Update(infraLogsMsg{generation: m.logsGeneration, text: strings.Repeat("NOVO LOG\n", 500)})
			m.View()
			if len(m.logSelection.lines) != 100 {
				t.Fatal("historico cresceu durante selecao")
			}
		}
		if up && m.logSelection.viewStart != 0 {
			t.Fatal("nao chegou ao inicio")
		}
		if !up && m.logSelection.viewStart+m.logSelection.viewHeight != 100 {
			t.Fatal("nao chegou ao final")
		}
		boundary := m.logSelection.viewStart
		for extra := 0; extra < 20; extra++ {
			m.Update(tea.MouseMotionMsg{X: area.x, Y: targetY, Button: tea.MouseLeft})
			_, next := m.Update(logSelectionScrollTick(generation))
			m.View()
			if next != nil || m.logSelection.viewStart != boundary {
				t.Fatal("voltou a rolar depois de atingir o limite")
			}
		}
		marker := "· fim"
		if up {
			marker = "· início"
		}
		if !strings.Contains(screen(m), marker) {
			t.Fatal("limite nao aparece no painel")
		}
		_, cmd = m.Update(tea.MouseReleaseMsg{X: area.x, Y: targetY, Button: tea.MouseLeft})
		if cmd == nil || clipboardText(cmd) != expected {
			t.Fatalf("copia incompleta, subindo=%v: %q", up, m.logSelection.text())
		}
		before := m.logSelection.viewStart
		_, cmd = m.Update(logSelectionScrollTick(generation))
		if cmd != nil || m.logSelection.viewStart != before {
			t.Fatal("continuou rolando depois de soltar")
		}
	}
}

func TestSelecaoNaoRolaDentroDaAreaENaoPrendeAoSoltar(t *testing.T) {
	c, _, _ := testCore(t, okChecker)
	m := open(t, c)
	m.tab = tabInfra
	m.state.Infra.Stacks = []core.StackStatus{{Name: "proxy", Enabled: true, Installed: true, Containers: []docker.Container{{Name: "proxy", State: "running"}}}}
	m.logsText = strings.Repeat("linha de log\n", 100)
	m.logsOffset = 40
	m.View()
	area := m.logArea
	for _, y := range []int{area.y, area.y + len(area.lines) - 1} {
		m.Update(tea.MouseClickMsg{X: area.x, Y: y, Button: tea.MouseLeft})
		_, cmd := m.Update(tea.MouseMotionMsg{X: area.x + 5, Y: y, Button: tea.MouseLeft})
		if cmd != nil || m.logSelection.direction != 0 {
			t.Fatal("selecionar texto na linha limite ativou rolagem")
		}
	}
	_, cmd := m.Update(tea.MouseMotionMsg{X: area.x + 5, Y: area.y - 1, Button: tea.MouseLeft})
	if cmd == nil {
		t.Fatal("nao rolou ao sair da area")
	}
	before := m.logSelection.viewStart
	m.Update(tea.MouseMotionMsg{X: area.x - 1, Y: area.y - 1, Button: tea.MouseLeft})
	_, cmd = m.Update(logSelectionScrollTick(m.selectionGeneration))
	if cmd != nil || m.logSelection.viewStart != before {
		t.Fatal("continuou rolando fora da lateral")
	}
	// Terminais com protocolo antigo soltam sem informar qual botao.
	m.Update(tea.MouseMotionMsg{X: area.x + 5, Y: area.y - 1, Button: tea.MouseLeft})
	_, cmd = m.Update(tea.MouseReleaseMsg{X: area.x + 5, Y: area.y - 1, Button: tea.MouseNone})
	if cmd == nil || m.logSelection.dragging || m.logSelection.tickPending {
		t.Fatal("soltar sem botao deixou rolagem ativa")
	}
	_, cmd = m.Update(logSelectionScrollTick(m.selectionGeneration))
	if cmd != nil || m.logSelection.viewStart != before {
		t.Fatal("timer pendente rolou apos soltar")
	}
}

func TestScrollNormalAlcancaInicioComLinhasLongas(t *testing.T) {
	c, _, _ := testCore(t, okChecker)
	m := open(t, c)
	m.tab = tabInfra
	m.state.Infra.Stacks = []core.StackStatus{{Name: "proxy", Enabled: true, Installed: true, Containers: []docker.Container{{Name: "proxy", State: "running"}}}}
	m.logsText = "INICIO DO LOG\n" + strings.Repeat(strings.Repeat("linha longa ", 30)+"\n", 100) + "FIM DO LOG"
	m.View()
	total := len(m.logArea.allLines)
	if total <= len(strings.Split(m.logsText, "\n")) {
		t.Fatal("fixture sem quebra de linhas")
	}
	for i := 0; i < total/10+2; i++ {
		m.infraKey("pgup")
		m.View()
	}
	if m.logArea.start != 0 || !strings.Contains(screen(m), "INICIO DO LOG") {
		t.Fatal("rolagem normal nao chegou ao inicio")
	}
	before := m.logsOffset
	m.infraKey("pgup")
	m.View()
	if m.logsOffset != before {
		t.Fatal("ultrapassou inicio")
	}
	for i := 0; i < total/10+2; i++ {
		m.infraKey("pgdown")
		m.View()
	}
	if m.logsOffset != 0 || !strings.Contains(screen(m), "FIM DO LOG") {
		t.Fatal("rolagem normal nao voltou ao fim")
	}
}
