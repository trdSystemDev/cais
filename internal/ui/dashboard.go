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
		traffic    core.Traffic // as requisicoes do proxy, lidas junto
	}
	sysTick uint64
)

// sysCmd pede ao core o uso da maquina (a VPS, mesmo com a TUI no notebook
// via SSH: quem le e o daemon).
func (m *model) sysCmd() tea.Cmd {
	generation := m.sysGeneration
	return func() tea.Msg {
		st, err := m.api.SystemStats(context.Background())
		tr, terr := m.api.TrafficStats(context.Background())
		if terr != nil {
			tr = core.Traffic{Rate: -1, Error: firstLine(terr.Error())}
		}
		return sysStatsMsg{generation, st, err, tr}
	}
}

// onSysStats guarda a leitura e agenda a proxima enquanto o dashboard
// estiver aberto.
func (m *model) onSysStats(msg sysStatsMsg) tea.Cmd {
	if msg.generation != m.sysGeneration {
		return nil
	}
	m.sys, m.sysErr, m.traffic = msg.stats, "", msg.traffic
	if msg.err != nil {
		m.sysErr = firstLine(msg.err.Error())
	}
	if m.tab != tabDashboard || m.disconnected {
		return nil
	}
	generation := m.sysGeneration
	return tea.Tick(sysEvery, func(time.Time) tea.Msg { return sysTick(generation) })
}

// viewDashboard e a saude geral: os avisos do que precisa de atencao, a
// visao geral (apps, servicos, backup, deploys), o uso da maquina e o
// ambiente. Nao guarda estado proprio alem da ultima leitura da maquina:
// o resto sai da foto do core.
func (m *model) viewDashboard(h int) string {
	t := m.th
	w := max(40, m.w-6)
	top := m.renderAlerts(w) + "\n" + m.renderOverview(w) + "\n" + t.rule(w) + "\n"
	env := m.renderEnvironment(m.state.Environment, w)
	// os graficos ficam com a altura que sobra (entre 3 e 10 linhas)
	body := func(chartH int) string {
		return top + m.renderSystem(w, chartH) + "\n" + t.rule(w) + "\n" +
			m.renderTraffic(w, max(3, chartH-2)) + "\n" + t.rule(w) + "\n" + env
	}
	chartH := min(10, max(3, 3+h-2-lipgloss.Height(body(3))))
	// o que nao cabe rola (roda do mouse, setas, pgup/pgdn)
	lines := strings.Split(strings.TrimSuffix(body(chartH), "\n"), "\n")
	avail := max(1, h-2)
	m.dashScroll = max(0, min(m.dashScroll, len(lines)-avail))
	visible := lines[m.dashScroll:min(len(lines), m.dashScroll+avail)]
	if m.dashScroll > 0 {
		visible[0] = t.fg(t.muted).Render("  ↑ mais acima")
	}
	if m.dashScroll+avail < len(lines) {
		visible[len(visible)-1] = t.fg(t.muted).Render("  ↓ mais abaixo (roda do mouse ou setas)")
	}
	return t.panel(false, m.w, h-2).MaxHeight(h).Render(strings.Join(visible, "\n"))
}

// dashKey rola o dashboard pelo teclado; ok diz se a tecla era de rolar.
func (m *model) dashKey(k string) bool {
	switch k {
	case "down", "j":
		m.dashScroll++
	case "up", "k":
		m.dashScroll = max(0, m.dashScroll-1)
	case "pgdown", "space":
		m.dashScroll += max(1, m.h/2)
	case "pgup":
		m.dashScroll = max(0, m.dashScroll-max(1, m.h/2))
	case "home", "g":
		m.dashScroll = 0
	case "end", "G":
		m.dashScroll = 1 << 20 // o desenho limita ao fim
	default:
		return false
	}
	return true
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

	// discos (e o swap, quando existe) em barras, na coluna do
	// processador; quem mais usa a maquina na da memoria
	var disks strings.Builder
	disks.WriteString(t.bold().Render("Disco") + "\n")
	labelW := 8
	for _, d := range n.Disks {
		labelW = max(labelW, len(d.Path))
	}
	barW := max(8, min(50, cw-labelW-36))
	line := func(label string, used, total uint64) {
		p := 100 * float64(used) / float64(max(total, 1))
		disks.WriteString("  " + muted.Render(fmt.Sprintf("%-*s", labelW, label)) + "  " + m.bar(p, barW) + "  " +
			t.fg(m.level(p)).Bold(true).Render(fmt.Sprintf("%3.0f%%", p)) + muted.Render("  "+size(used)+" de "+size(total)) + "\n")
	}
	for _, d := range n.Disks {
		line(d.Path, d.Used, d.Total)
	}
	if n.SwapTotal > 0 {
		line("swap", n.SwapUsed, n.SwapTotal)
	}
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Width(cw+gap).Render(strings.TrimSuffix(disks.String(), "\n")),
		m.renderContainers(w-cw-gap)) + "\n")
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
	return m.chartScaled(values, w, h, c, "100%", " 50%")
}

// chartScaled e o chart com a escala propria (topLabel e midLabel, 4
// colunas): os valores continuam de 0 a 100 (% do topo).
func (m *model) chartScaled(values []float64, w, h int, c color.Color, topLabel, midLabel string) string {
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
			label, tick, grid = topLabel, "┤", "·"
		case r == h-1:
			label, tick = map[bool]string{true: "  0%", false: "   0"}[topLabel == "100%"], "┤"
		case r == h/2 && h >= 5 && h%2 == 1:
			label, tick, grid = midLabel, "┤", "·"
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
	return m.barColor(p, w, m.level(p))
}

// barColor e a barra numa cor so (sem o alerta do uso alto).
func (m *model) barColor(p float64, w int, c color.Color) string {
	t := m.th
	n := min(w, max(0, int(math.Round(p/100*float64(w)))))
	if p > 0 && n == 0 {
		n = 1
	}
	return t.fg(c).Render(strings.Repeat("█", n)) + t.fg(t.border).Render(strings.Repeat("░", w-n))
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

// renderEnvironment: com o Docker funcionando, uma linha so; com problema,
// o detalhe de cada parte.
func (m *model) renderEnvironment(e core.Environment, w int) string {
	t := m.th
	muted := t.fg(t.muted)
	k := btnNormal
	if e.Checking {
		k = btnOff
	}
	button := m.zones.Mark("check", t.button("Verificar novamente", k, false))
	title := t.bold().Render("Ambiente") + "  "
	if e.Checking || !e.Checked() {
		return title + t.fg(t.warn).Render("Verificando o ambiente...")
	}
	when := muted.Render("Ultima verificacao: " + e.CheckedAt.Local().Format("02/01 15:04:05"))
	host := e.Host.Hostname + " " + muted.Render("("+e.Host.OS+"/"+e.Host.Arch+")")
	if e.Ready() && e.Host.Supported() {
		line := title + t.fg(t.ok).Bold(true).Render("✓ Pronto.") + " Docker " + e.Docker.Daemon.Version +
			muted.Render(" · compose ") + e.Docker.Compose.Version + muted.Render(" · ") + host
		when = muted.Render("verificado as " + e.CheckedAt.Local().Format("15:04:05"))
		if lipgloss.Width(line)+lipgloss.Width(when)+lipgloss.Width(button)+4 > w {
			return line + "\n" + when + "  " + button
		}
		return spread(line, when+"  "+button, w)
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
	var sys strings.Builder
	sys.WriteString(t.bold().Render("Sistema") + "\n")
	sys.WriteString("  " + muted.Render("Host     ") + e.Host.Hostname + "\n")
	sys.WriteString("  " + muted.Render("Sistema  ") + e.Host.OS + "/" + e.Host.Arch)

	var b strings.Builder
	b.WriteString(t.bold().Render("Ambiente") + "\n")
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Width(min(48, w/2)).Render(docker.String()), sys.String()) + "\n")
	if !e.Host.Supported() {
		b.WriteString(t.fg(t.warn).Render("! O cais foi feito pra rodar na VPS Linux; aqui so da pra navegar na interface.") + "\n")
	}
	if e.Ready() {
		b.WriteString(t.fg(t.ok).Bold(true).Render("Pronto.") + " O Docker esta funcionando.\n")
	} else {
		b.WriteString(t.fg(t.danger).Bold(true).Render("Resolva os itens em vermelho") + " antes de usar o Setup.\n")
	}
	b.WriteString(when + "  " + button)
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
