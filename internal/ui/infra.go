package ui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/trdSystemDev/cais/internal/bootstrap"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/stacks"
)

// serviceDoneMsg e a volta do liga/desliga (ou do Aplicar) de um servico.
type serviceDoneMsg struct {
	name  string
	on    bool
	apply bool // ja estava ligado: aplicou a config salva
	err   error
}

// optional sao os servicos que a aba Infra liga e desliga, na ordem da tela.
func optional() []string {
	var names []string
	for _, s := range stacks.All {
		if s.Optional {
			names = append(names, s.Name)
		}
	}
	return names
}

// stacksCmd pede ao core o estado das stacks; o resultado chega pelo estado.
func (m *model) stacksCmd() tea.Cmd {
	return func() tea.Msg {
		m.api.CheckStacks(context.Background())
		return nil
	}
}

// busy diz se ha uma mudanca sendo aplicada (daqui, do CLI ou do MCP).
func (m *model) busy() bool { return m.toggling != "" || m.controlling != "" || m.state.Setup.Running }

// toggle liga ou desliga um servico. O core grava a config e aplica; o
// andamento chega pelo estado.
func (m *model) toggle(name string) tea.Cmd { return m.setService(name, !m.serviceOn(name), false) }

// apply aplica num servico ligado a config salva no Setup (ligar de novo o
// que ja esta ligado).
func (m *model) apply(name string) tea.Cmd {
	if !m.serviceOn(name) || !m.needsApply(name) {
		return nil
	}
	return m.setService(name, true, true)
}

func (m *model) setService(name string, on, apply bool) tea.Cmd {
	if m.busy() {
		return nil
	}
	m.toggling = name
	m.togglingOn = on
	m.infraMsgStack = name
	verb := map[bool]string{true: "Ligando", false: "Desligando"}[on]
	if apply {
		verb = "Aplicando a config no"
	}
	m.infraMsgC, m.infraMsg = "warn", verb+" "+name+"..."
	return func() tea.Msg {
		_, err := m.api.SetService(context.Background(), name, on)
		return serviceDoneMsg{name, on, apply, err}
	}
}

// needsApply diz se a config salva muda o servico ligado (ou se ele ainda
// nao subiu): o card mostra o Aplicar.
func (m *model) needsApply(name string) bool {
	for _, s := range m.setup.plan.Stacks {
		if s.Name == name {
			return s.Change != bootstrap.ChangeUnchanged
		}
	}
	return false
}

func (m *model) serviceOn(name string) bool {
	for _, s := range m.state.Infra.Stacks {
		if s.Name == name {
			return s.Enabled
		}
	}
	return false
}

func (m *model) onServiceDone(msg serviceDoneMsg) tea.Cmd {
	m.toggling = ""
	m.infraMsgStack = msg.name
	switch {
	case msg.err != nil:
		m.infraMsgC, m.infraMsg = "danger", firstLine(msg.err.Error())
	case msg.apply:
		m.infraMsgC, m.infraMsg = "ok", "config aplicada no "+msg.name+"."
	case msg.on:
		m.infraMsgC, m.infraMsg = "ok", msg.name+" ligado."
	default:
		m.infraMsgC, m.infraMsg = "ok", msg.name+" desligado (os dados ficam no volume)."
	}
	// a aba Setup mostra o plano novo (o servico mudou a lista de stacks)
	return tea.Batch(m.stacksCmd(), m.setup.planCmd(), m.syncInfraPanels())
}

// onPortsSaved volta o foco pros botoes do proxy e, com ele ligado, aplica
// as portas novas (o proxy sobe de novo nelas).
func (m *model) onPortsSaved(msg portsSavedMsg) tea.Cmd {
	m.ports.busy = false
	if msg.err != nil {
		m.ports.err = msg.err.Error()
		return nil
	}
	if m.infraInSub {
		m.setInfraPos(0)
	}
	m.ports.blur()
	m.infraMsgStack = "proxy"
	switch {
	case !m.serviceOn("proxy"):
		m.infraMsgC, m.infraMsg = "ok", "Portas salvas: valem quando o proxy ligar."
	case m.busy():
		m.infraMsgC, m.infraMsg = "warn", "Portas salvas. Outra mudanca em andamento: Aplicar quando ela terminar."
	default:
		return tea.Batch(m.setService("proxy", true, true), m.setup.planCmd())
	}
	return m.setup.planCmd()
}

// infraOrder agrupa base e opcionais, incluindo ambos na selecao de logs.
func infraOrder() []string {
	var names []string
	for _, optional := range []bool{false, true} {
		for _, st := range stacks.All {
			if st.Optional == optional {
				names = append(names, st.Name)
			}
		}
	}
	return names
}

func (m *model) selectedStack() core.StackStatus {
	name := infraOrder()[m.infraSel]
	for _, st := range m.state.Infra.Stacks {
		if st.Name == name {
			return st
		}
	}
	st, _ := stacks.Get(name)
	return core.StackStatus{Name: name, Desc: st.Desc}
}

func (m *model) viewInfra(h int) string {
	m.logArea = logArea{}
	t := m.th
	leftW := min(52, max(30, m.w*2/5))
	if m.w < 72 {
		return t.panel(false, m.w-2, max(1, h-2)).Render("Amplie o terminal para pelo menos 72 colunas para ver cards e logs.")
	}
	innerH := max(1, h-2)
	var cards []string
	order := infraOrder()
	count := max(1, (innerH-5)/4)
	start := max(0, m.infraSel-count+1)
	start = min(start, max(0, len(order)-count))
	for i := start; i < min(len(order), start+count); i++ {
		name := order[i]
		st := core.StackStatus{Name: name}
		for _, item := range m.state.Infra.Stacks {
			if item.Name == name {
				st = item
			}
		}
		selected := i == m.infraSel
		mark, status, statusColor := m.stackStatus(st)
		title := t.bold().Render(mark+" "+name) + "  " + t.fg(statusColor).Render(status)
		role := map[string]string{"proxy": "HTTPS dos apps", "postgres": "Banco", "mysql": "Banco", "garage": "S3"}[name]
		// porta fora do padrao (teste, migracao) fica a vista no card
		if pc := m.setup.plan.Config.Proxy; name == "proxy" && pc.HTTPPort > 0 && !pc.StandardPorts() {
			role += fmt.Sprintf(" · :%d/:%d", pc.HTTPPort, pc.HTTPSPort)
		}
		if st.Enabled && m.needsApply(name) {
			role += " · " + t.fg(t.warn).Render("Aplicar")
		}
		line := t.fg(t.muted).Render(role)
		card := t.panel(selected, leftW-4, 2).Render(ansi.Truncate(title, leftW-6, "…") + "\n" + line)
		cards = append(cards, m.zones.Mark("stack:"+name, card))
	}
	header := t.bold().Render("Infra") + "  " + t.fg(t.muted).Render("Servicos: nada sobe sem ligar")
	footer := m.zones.Mark("infra-refresh", t.button("Atualizar", btnNormal, false))
	if !m.state.Infra.CheckedAt.IsZero() {
		footer += "\n" + t.fg(t.muted).Render("Ultima verificacao: "+m.state.Infra.CheckedAt.Local().Format("15:04:05"))
	}
	if m.state.Infra.Error != "" {
		footer += "\n" + t.fg(t.danger).Render(firstLine(m.state.Infra.Error))
	}
	left := lipgloss.NewStyle().Width(leftW).Height(h).Render(header + "\n" + strings.Join(cards, "\n") + "\n" + footer)
	rightW := m.w - leftW - 1
	right := m.viewInfraGeneral(rightW, innerH)
	if m.infraSection == infraLogs {
		right = m.zones.Mark("infra-logs", m.viewInfraLogs(rightW, innerH))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)
}

type infraLogsMsg struct {
	generation uint64
	text       string
	err        error
}
type infraLogsTick uint64

func (m *model) syncInfraLogs(force bool) tea.Cmd {
	if m.tab != tabInfra || m.disconnected {
		return nil
	}
	st := m.selectedStack()
	if m.infraContainer >= len(st.Containers) {
		m.infraContainer = 0
	}
	target := ""
	if len(st.Containers) > 0 && m.infraSection == infraLogs {
		target = st.Containers[m.infraContainer].Name
	}
	if !force && target == m.logsTarget {
		return nil
	}
	m.logSelection = logSelection{}
	m.logsGeneration++
	m.logsTarget = target
	m.logsText = ""
	m.logsError = ""
	m.logsOffset = 0
	if target == "" {
		return nil
	}
	return m.fetchInfraLogs()
}

func (m *model) fetchInfraLogs() tea.Cmd {
	generation, stack, target := m.logsGeneration, m.selectedStack().Name, m.logsTarget
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		out, err := m.api.ContainerLogs(ctx, stack, target)
		return infraLogsMsg{generation, out, err}
	}
}

func (m *model) viewInfraLogs(w, h int) string {
	t := m.th
	st := m.selectedStack()
	if on, detail, changing := m.serviceOperation(st.Name); changing {
		return m.viewServiceModal(w, h, st.Name, on, detail)
	}
	var b strings.Builder
	b.WriteString(m.viewInfraHead(st, w-6))
	title := "Logs · " + st.Name
	if pos := m.logPosition(); pos != "" {
		title = "Logs · " + pos
	}
	b.WriteString(t.bold().Render(ansi.Truncate(title, w-6, "…")) + "\n")
	if len(st.Containers) > 0 {
		columns := min(len(st.Containers), max(1, (w-6)/16))
		tabW := (w - 6 - (columns - 1)) / columns
		var row []string
		for i, c := range st.Containers {
			state, color := c.State, t.ok
			if c.Health != "" {
				state = c.Health
			}
			switch {
			case c.State != "running" || c.Health == "unhealthy":
				color = t.danger
			case c.Health == "starting":
				color = t.warn
			}
			content := t.bold().Render(ansi.Truncate(c.Name, max(1, tabW-4), "…")) + "\n" + t.fg(color).Render(ansi.Truncate("● "+state, max(1, tabW-4), "…"))
			tab := t.panel(i == m.infraContainer && (m.infraForm || len(st.Containers) == 1), tabW-2, 2).Render(content)
			row = append(row, m.zones.Mark("container:"+c.Name, tab))
			if len(row) == columns || i == len(st.Containers)-1 {
				b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, interleaveTabs(row)...) + "\n")
				row = nil
			}
		}
	}
	if m.infraMsg != "" && m.infraMsgStack == st.Name {
		color := t.warn
		if m.infraMsgC == "danger" {
			color = t.danger
		}
		if m.infraMsgC == "ok" {
			color = t.ok
		}
		b.WriteString(t.fg(color).Render(ansi.Truncate(m.infraMsg, w-6, "…")) + "\n")
	}
	b.WriteString("\n")
	empty := ""
	if len(st.Containers) == 0 {
		empty = "Nenhum container. Ligue o servico na secao Geral (ou Aplicar, se ja estiver ligado)."
	}
	m.writeLogs(&b, empty, w, h)
	return t.panel(m.infraForm, w-2, h).MaxHeight(h + 2).Render(b.String())
}

// logPosition diz quais linhas do log estao na tela durante a selecao
// ("31–60/100", com "· início" ou "· fim" nos limites); vazio sem selecao.
func (m *model) logPosition() string {
	s := m.logSelection
	if len(s.lines) == 0 {
		return ""
	}
	pos := fmt.Sprintf("%d–%d/%d", s.viewStart+1, min(len(s.lines), s.viewStart+s.viewHeight), len(s.lines))
	if s.viewStart == 0 {
		pos += " · início"
	} else if s.viewStart+s.viewHeight == len(s.lines) {
		pos += " · fim"
	}
	return pos
}

// writeLogs escreve o texto dos logs (m.logsText) no fim de b, com a
// rolagem e a selecao pra copiar, e registra a area na tela (m.logArea).
// empty, quando nao vazio, substitui o texto (nada pra mostrar). Serve as
// abas Infra e Apps: so uma fica aberta por vez.
func (m *model) writeLogs(b *strings.Builder, empty string, w, h int) {
	text := m.logsText
	switch {
	case empty != "":
		text = empty
	case m.logsError != "":
		text = "Erro ao ler logs: " + m.logsError
	case text == "":
		text = "Aguardando logs do container..."
	}
	text = strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, ansi.Strip(text))
	lines := strings.Split(ansi.Hardwrap(text, max(1, w-6), true), "\n")
	available := max(1, h-lipgloss.Height(b.String()))
	offset := min(m.logsOffset, max(0, len(lines)-available))
	end := max(1, len(lines)-offset)
	start := max(0, end-available)
	visibleLines := lines[start:end]
	visible := strings.Join(visibleLines, "\n")
	if len(m.logSelection.lines) > 0 {
		start = m.logSelection.viewStart
		end = min(len(m.logSelection.lines), start+m.logSelection.viewHeight)
		lines = m.logSelection.lines
		visibleLines = lines[start:end]
		visible = m.renderLogSelection()
	}
	m.logArea = logArea{x: m.w - w + 2, y: lipgloss.Height(m.viewTabs()) + lipgloss.Height(b.String()), width: w - 6, height: available, lines: append([]string(nil), visibleLines...), allLines: lines, start: start}
	b.WriteString(visible)
}

// runningStep e a descricao do passo do Setup em andamento.
func runningStep(s core.Setup) string {
	for _, st := range s.Steps {
		if st.Status == bootstrap.Running {
			return st.Desc
		}
	}
	return "comecando..."
}

func (m *model) infraClick(msg tea.MouseMsg) tea.Cmd {
	if m.zones.Get("infra-refresh").InBounds(msg) && !m.state.Infra.Checking {
		return tea.Batch(m.stacksCmd(), m.syncInfraLogs(true))
	}
	for i, s := range infraSections {
		if m.zones.Get("infra-sec:" + s).InBounds(msg) {
			m.leaveInfraForm()
			m.infraSection = i
			return m.syncInfraLogs(true)
		}
	}
	st := m.selectedStack()
	if m.infraSection == infraGeneral {
		for i, id := range m.serviceActions(st) {
			if m.zones.Get(id).InBounds(msg) {
				m.infraForm = true
				m.setInfraPos(i)
				return m.pressAction(id)
			}
		}
		if m.db.open() {
			if cmd, ok := m.db.click(msg); ok {
				m.infraForm, m.infraInSub = true, true
				return cmd
			}
		}
		if m.ports.shown {
			if cmd, ok := m.ports.click(msg); ok {
				m.infraForm, m.infraInSub = true, true
				return cmd
			}
		}
	}
	for i, name := range infraOrder() {
		if m.zones.Get("stack:" + name).InBounds(msg) {
			return m.selectInfra(i)
		}
	}
	if m.infraSection == infraLogs {
		for i, c := range st.Containers {
			if m.zones.Get("container:" + c.Name).InBounds(msg) {
				m.infraContainer = i
				return m.syncInfraLogs(true)
			}
		}
	}
	return nil
}

// infraKey: fora do formulario, ↑↓ troca o card e ←→ a secao; enter ou tab
// entra nos botoes do servico (Geral) ou nos containers (Logs, ←→ troca);
// esc volta pros cards.
func (m *model) infraKey(msg tea.KeyPressMsg) tea.Cmd {
	k := msg.String()
	m.logSelection = logSelection{}
	switch k {
	case "pgup":
		m.logsOffset = min(m.logsOffset+10, max(0, len(m.logArea.allLines)-m.logArea.height))
		return nil
	case "pgdown":
		m.logsOffset = max(0, m.logsOffset-10)
		return nil
	case "end":
		m.logsOffset = 0
		return nil
	}
	if m.infraForm {
		return m.infraFormKey(msg)
	}
	n := len(infraOrder())
	switch k {
	case "r":
		return tea.Batch(m.stacksCmd(), m.setup.planCmd(), m.syncInfraLogs(true))
	case "a":
		return m.apply(infraOrder()[m.infraSel])
	case "down":
		return m.selectInfra((m.infraSel + 1) % n)
	case "up":
		return m.selectInfra((m.infraSel + n - 1) % n)
	case "left", "right":
		m.infraSection = 1 - m.infraSection
		return m.syncInfraLogs(true)
	case "enter", "tab":
		m.infraForm = true
		if m.infraSection == infraGeneral {
			return m.setInfraPos(0)
		}
	}
	return nil
}

func (m *model) infraFormKey(msg tea.KeyPressMsg) tea.Cmd {
	k := msg.String()
	if m.infraSection == infraLogs {
		switch k {
		case "esc", "tab", "enter":
			m.infraForm = false
		case "left", "right":
			if count := len(m.selectedStack().Containers); count > 0 {
				d := 1
				if k == "left" {
					d = -1
				}
				m.infraContainer = (m.infraContainer + d + count) % count
				return m.syncInfraLogs(true)
			}
		}
		return nil
	}
	switch k {
	case "esc":
		if m.infraInSub && m.db.open() && m.db.delName != "" {
			m.db.delName = ""
			return m.db.setFocus(0)
		}
		m.leaveInfraForm()
		return nil
	case "tab", "down":
		return m.setInfraPos(m.infraPos() + 1)
	case "shift+tab", "up":
		return m.setInfraPos(m.infraPos() - 1)
	}
	if !m.infraInSub {
		if k == "enter" || k == "space" {
			actions := m.serviceActions(m.selectedStack())
			return m.pressAction(actions[min(m.infraFocus, len(actions)-1)])
		}
		return nil
	}
	if m.ports.shown {
		return m.ports.key(msg)
	}
	if m.db.open() {
		return m.db.key(msg)
	}
	return nil
}

// serviceOperation vincula o progresso ao servico, inclusive quando a acao
// foi iniciada por outro cliente do daemon.
func (m *model) serviceOperation(name string) (on bool, detail string, changing bool) {
	if m.toggling == name {
		return m.togglingOn, runningStep(m.state.Setup), true
	}
	setup := m.state.Setup
	if !setup.Running {
		return m.containerStarting(name)
	}
	if setup.Service != "" {
		if setup.Service == name {
			return setup.ServiceOn, runningStep(setup), true
		}
		return false, "", false
	}
	for _, step := range setup.Steps {
		if step.Status != bootstrap.Running {
			continue
		}
		if step.ID == "stack:"+name || (name == "garage" && step.ID == "garage-layout") {
			return true, step.Desc, true
		}
		if step.ID == "stop:"+name {
			return false, step.Desc, true
		}
	}
	return m.containerStarting(name)
}

func (m *model) serviceChanging(name string) bool {
	_, _, changing := m.serviceOperation(name)
	return changing
}

func (m *model) viewServiceModal(w, h int, name string, on bool, detail string) string {
	t := m.th
	title := "Derrubando container"
	if on {
		title = "Iniciando container"
	}
	width := max(16, min(48, w-8))
	text := t.fg(t.warn).Bold(true).Render("◌ "+title) + "\n" + t.bold().Render(name) + "\n\n" + detail
	modal := t.panel(true, width, 0).MaxHeight(h).Render(text)
	centered := lipgloss.Place(w-4, h, lipgloss.Center, lipgloss.Center, modal)
	return t.panel(false, w-2, h).MaxHeight(h + 2).Render(centered)
}

func (m *model) containerStarting(name string) (bool, string, bool) {
	for _, st := range m.state.Infra.Stacks {
		if st.Name != name {
			continue
		}
		for _, c := range st.Containers {
			if c.Health == "starting" || c.State == "created" || c.State == "restarting" {
				return true, "Aguardando " + c.Name + " ficar pronto...", true
			}
		}
	}
	return false, "", false
}

func interleaveTabs(tabs []string) []string {
	var parts []string
	for i, tab := range tabs {
		if i > 0 {
			parts = append(parts, " ")
		}
		parts = append(parts, tab)
	}
	return parts
}
