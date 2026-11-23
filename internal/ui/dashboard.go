package ui

import (
	"context"
	"fmt"
	"image/color"
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/databases"
)

// sysEvery e o intervalo entre leituras do uso da maquina com o dashboard
// aberto.
const sysEvery = 2 * time.Second

type (
	sysStatsMsg struct {
		generation uint64
		stats      core.SystemStats
		err        error
	}
	sysTick uint64
)

// sysCmd pede ao core o uso da maquina (a VPS, mesmo com a TUI no notebook
// via SSH: quem le e o daemon).
func (m *model) sysCmd() tea.Cmd {
	generation := m.sysGeneration
	return func() tea.Msg {
		st, err := m.api.SystemStats(context.Background())
		return sysStatsMsg{generation, st, err}
	}
}

// onSysStats guarda a leitura e agenda a proxima enquanto o dashboard
// estiver aberto.
func (m *model) onSysStats(msg sysStatsMsg) tea.Cmd {
	if msg.generation != m.sysGeneration {
		return nil
	}
	m.sys, m.sysErr = msg.stats, ""
	if msg.err != nil {
		m.sysErr = firstLine(msg.err.Error())
	}
	if m.tab != tabDashboard || m.disconnected {
		return nil
	}
	generation := m.sysGeneration
	return tea.Tick(sysEvery, func(time.Time) tea.Msg { return sysTick(generation) })
}

// viewDashboard mostra o uso da maquina (graficos do processador e da
// memoria, discos) e o estado do ambiente. Nao guarda estado proprio alem
// da ultima leitura: o botao so dispara a acao no core.
func (m *model) viewDashboard(h int) string {
	t := m.th
	w := max(40, m.w-6)
	e := m.state.Environment

	k := btnNormal
	if e.Checking {
		k = btnOff
	}
	button := m.zones.Mark("check", t.button("Verificar novamente", k, false))
	env := t.bold().Render("Ambiente") + "\n\n" + m.renderEnvironment(e, w) + "\n\n" + button

	// os graficos ficam com a altura que sobra (entre 3 e 10 linhas)
	fixed := lipgloss.Height(env) + 9 + len(m.sys.Now.Disks)
	if m.sys.Now.SwapTotal > 0 {
		fixed++
	}
	chartH := min(10, max(3, h-2-fixed))
	body := m.renderSystem(w, chartH) + "\n" + t.rule(w) + "\n" + env
	return t.panel(false, m.w, h-2).MaxHeight(h).Render(body)
}

func (m *model) renderSystem(w, chartH int) string {
	t := m.th
	muted := t.fg(t.muted)
	s := m.sys
	title := t.bold().Render("Uso da maquina")
	if s.At.IsZero() && m.sysErr == "" {
		return title + "\n\n" + t.fg(t.warn).Render("Lendo o uso da maquina...") + "\n"
	}
	if err := cmpOrStr(m.sysErr, s.Error); err != "" {
		return title + "\n\n" + t.fg(t.warn).Render("Sem o uso da maquina: "+err) + "\n"
	}
	n := s.Now
	right := fmt.Sprintf("ligada ha %s · carga %.2f %.2f %.2f · atualiza a cada %ds",
		uptime(n.Uptime), n.Load[0], n.Load[1], n.Load[2], int(sysEvery.Seconds()))
	var b strings.Builder
	b.WriteString(spread(title, muted.Render(right), w) + "\n\n")

	// processador e memoria lado a lado
	gap := 4
	cw := (w - gap) / 2
	cpuText := "…"
	if s.CPU >= 0 {
		cpuText = fmt.Sprintf("%.0f%%", s.CPU)
	}
	cpuHead := t.bold().Render("Processador ") + t.fg(m.level(s.CPU)).Bold(true).Render(cpuText) +
		muted.Render(fmt.Sprintf("  %d nucleos", n.CPUs))
	memHead := t.bold().Render("Memoria ") + t.fg(m.level(n.MemPercent())).Bold(true).Render(fmt.Sprintf("%.0f%%", n.MemPercent())) +
		muted.Render("  "+size(n.MemUsed)+" de "+size(n.MemTotal))
	span := func(h []float64) string {
		secs := min(len(h), cw-5) * int(sysEvery.Seconds())
		if secs >= 120 {
			return muted.Render(fmt.Sprintf("     ultimos %d min", secs/60))
		}
		return muted.Render(fmt.Sprintf("     ultimos %ds", secs))
	}
	left := cpuHead + "\n" + m.chart(s.CPUHistory, cw, chartH, t.info) + "\n" + span(s.CPUHistory)
	rightCol := memHead + "\n" + m.chart(s.MemHistory, cw, chartH, t.violet) + "\n" + span(s.MemHistory)
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Width(cw+gap).Render(left), rightCol) + "\n\n")

	// discos (e o swap, quando existe) em barras
	b.WriteString(t.bold().Render("Disco") + "\n")
	labelW := 8
	for _, d := range n.Disks {
		labelW = max(labelW, len(d.Path))
	}
	barW := max(10, min(50, w-labelW-30))
	line := func(label string, used, total uint64) {
		p := 100 * float64(used) / float64(max(total, 1))
		b.WriteString("  " + muted.Render(fmt.Sprintf("%-*s", labelW, label)) + "  " + m.bar(p, barW) + "  " +
			t.fg(m.level(p)).Bold(true).Render(fmt.Sprintf("%3.0f%%", p)) + muted.Render("  "+size(used)+" de "+size(total)) + "\n")
	}
	for _, d := range n.Disks {
		line(d.Path, d.Used, d.Total)
	}
	if n.SwapTotal > 0 {
		line("swap", n.SwapUsed, n.SwapTotal)
	}
	return b.String()
}

// level e a cor de um uso: verde, amarelo a partir de 70%, vermelho a
// partir de 90%.
func (m *model) level(p float64) color.Color {
	switch {
	case p >= 90:
		return m.th.danger
	case p >= 70:
		return m.th.warn
	case p < 0:
		return m.th.muted
	}
	return m.th.ok
}

// chart desenha o historico (0 a 100%, do mais velho pro mais novo) em
// colunas de blocos de w x h, com a escala na esquerda; os mais novos ficam
// na direita, todos na cor c do grafico.
func (m *model) chart(values []float64, w, h int, c color.Color) string {
	t := m.th
	axis := t.fg(t.border)
	cols := max(1, w-5)
	if len(values) > cols {
		values = values[len(values)-cols:]
	}
	pad := cols - len(values)
	blocks := []rune(" ▁▂▃▄▅▆▇█")
	rows := make([]string, h)
	for r := range h {
		// escala: 100% em cima, 0% embaixo e 50% no meio (com altura pra
		// isso); as linhas de 100% e 50% levam uma grade pontilhada
		label, tick, grid := "    ", "│", " "
		switch {
		case r == 0:
			label, tick, grid = "100%", "┤", "·"
		case r == h-1:
			label, tick = "  0%", "┤"
		case r == h/2 && h >= 5 && h%2 == 1:
			label, tick, grid = " 50%", "┤", "·"
		}
		var line strings.Builder
		line.WriteString(axis.Render(label + tick))
		line.WriteString(axis.Render(strings.Repeat(grid, pad)))
		floor := float64(h-1-r) * 8 // oitavos abaixo desta linha
		for _, v := range values {
			fill := int(math.Round(v/100*float64(h)*8 - floor))
			fill = min(8, max(0, fill))
			if fill == 0 && r == h-1 && v > 0 {
				fill = 1 // uso pequeno ainda aparece
			}
			if fill == 0 {
				line.WriteString(axis.Render(grid))
				continue
			}
			line.WriteString(t.fg(c).Render(string(blocks[fill])))
		}
		rows[r] = line.String()
	}
	return strings.Join(rows, "\n")
}

// bar e uma barra de uso de w colunas.
func (m *model) bar(p float64, w int) string {
	t := m.th
	n := min(w, max(0, int(math.Round(p/100*float64(w)))))
	if p > 0 && n == 0 {
		n = 1
	}
	return t.fg(m.level(p)).Render(strings.Repeat("█", n)) + t.fg(t.border).Render(strings.Repeat("░", w-n))
}

// spread poe a e b nas pontas de uma linha de w colunas.
func spread(a, b string, w int) string {
	gap := w - lipgloss.Width(a) - lipgloss.Width(b)
	if gap < 2 {
		return a + "  " + b
	}
	return a + strings.Repeat(" ", gap) + b
}

func size(n uint64) string { return databases.HumanSize(int64(min(n, math.MaxInt64))) }

// uptime em dias e horas (ou horas e minutos).
func uptime(d time.Duration) string {
	days, hours, mins := int(d.Hours())/24, int(d.Hours())%24, int(d.Minutes())%60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dmin", hours, mins)
	}
	return fmt.Sprintf("%dmin", mins)
}

func (m *model) renderEnvironment(e core.Environment, w int) string {
	t := m.th
	muted := t.fg(t.muted)
	if e.Checking || !e.Checked() {
		return t.fg(t.warn).Render("Verificando o ambiente...")
	}

	// Docker e o sistema lado a lado
	var docker strings.Builder
	docker.WriteString(t.bold().Render("Docker") + "\n")
	docker.WriteString(m.component("CLI", e.Docker.CLI))
	if !e.Docker.Installed {
		// sem o CLI nao da pra checar o resto; nao repete a mesma mensagem
		docker.WriteString(muted.Render("  -  Daemon   depende do CLI") + "\n")
		docker.WriteString(muted.Render("  -  Compose  depende do CLI"))
	} else {
		docker.WriteString(m.component("Daemon", e.Docker.Daemon))
		docker.WriteString(strings.TrimSuffix(m.component("Compose", e.Docker.Compose), "\n"))
	}

	n := m.sys.Now
	var sys strings.Builder
	sys.WriteString(t.bold().Render("Sistema") + "\n")
	sys.WriteString("  " + muted.Render("Host     ") + e.Host.Hostname + "\n")
	sys.WriteString("  " + muted.Render("Sistema  ") + e.Host.OS + "/" + e.Host.Arch)
	if n.CPUs > 0 {
		sys.WriteString("\n  " + muted.Render("Maquina  ") + fmt.Sprintf("%d nucleos, %s de memoria", n.CPUs, size(n.MemTotal)))
	}

	var b strings.Builder
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Width(min(48, w/2)).Render(docker.String()), sys.String()) + "\n")
	if !e.Host.Supported() {
		b.WriteString(t.fg(t.warn).Render("! O cais foi feito pra rodar na VPS Linux; aqui so da pra navegar na interface.") + "\n")
	}
	b.WriteString("\n")
	if e.Ready() {
		b.WriteString(t.fg(t.ok).Bold(true).Render("Pronto.") + " O Docker esta funcionando -- o proximo passo e a aba Setup.\n")
	} else {
		b.WriteString(t.fg(t.danger).Bold(true).Render("Resolva os itens em vermelho") + " antes de usar o Setup.\n")
	}
	b.WriteString(muted.Render("Ultima verificacao: " + e.CheckedAt.Local().Format("02/01/2006 15:04:05")))
	return b.String()
}

func (m *model) component(label string, c core.Component) string {
	t := m.th
	name := t.fg(t.muted).Render(lipgloss.NewStyle().Width(8).Render(label))
	if !c.OK() {
		return "  " + t.fg(t.danger).Render("✗") + " " + name + " " + t.fg(t.danger).Render(firstLine(c.Error)) + "\n"
	}
	return "  " + t.fg(t.ok).Render("✓") + " " + name + " " + c.Version + "\n"
}
