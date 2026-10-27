package ui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/trdSystemDev/cais/internal/bootstrap"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/stacks"
)

// serviceDoneMsg e a volta do liga/desliga de um servico opcional.
type serviceDoneMsg struct {
	name string
	on   bool
	err  error
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
func (m *model) busy() bool { return m.toggling != "" || m.state.Setup.Running }

// toggle liga ou desliga um servico opcional. O core grava a config e roda
// o Setup; o andamento chega pelo estado.
func (m *model) toggle(name string) tea.Cmd {
	if m.busy() {
		return nil
	}
	on := !m.serviceOn(name)
	m.toggling = name
	verb := map[bool]string{true: "Ligando", false: "Desligando"}[on]
	m.infraMsgC, m.infraMsg = "warn", verb+" "+name+"..."
	return func() tea.Msg {
		_, err := m.api.SetService(context.Background(), name, on)
		return serviceDoneMsg{name, on, err}
	}
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
	switch {
	case msg.err != nil:
		m.infraMsgC, m.infraMsg = "danger", firstLine(msg.err.Error())
	case msg.on:
		m.infraMsgC, m.infraMsg = "ok", msg.name+" ligado."
	default:
		m.infraMsgC, m.infraMsg = "ok", msg.name+" desligado (os dados ficam no volume)."
	}
	// a aba Setup mostra o plano novo (o servico mudou a lista de stacks)
	return tea.Batch(m.stacksCmd(), m.setup.planCmd())
}

func (m *model) viewInfra(h int) string {
	t := m.th
	infra := m.state.Infra
	var b strings.Builder

	if infra.CheckedAt.IsZero() {
		b.WriteString(t.bold().Render("Infra") + "\n\n" + t.fg(t.warn).Render("Perguntando ao docker...") + "\n")
		return t.panel(false, m.w-2, h-2).Render(b.String())
	}
	if infra.Error != "" {
		b.WriteString(t.fg(t.danger).Render(infra.Error) + "\n\n")
	}

	byName := map[string]core.StackStatus{}
	for _, s := range infra.Stacks {
		byName[s.Name] = s
	}

	b.WriteString(t.bold().Render("Base") + " " + t.fg(t.muted).Render("(sobem sempre)") + "\n")
	for _, s := range stacks.All {
		if !s.Optional {
			b.WriteString("\n" + m.viewStack(byName[s.Name], false, false))
		}
	}

	b.WriteString("\n" + t.bold().Render("Servicos") + " " + t.fg(t.muted).Render("(ligue so o que for usar)") + "\n")
	for i, name := range optional() {
		b.WriteString("\n" + m.viewStack(byName[name], true, i == m.infraSel))
	}

	if m.busy() {
		b.WriteString("\n" + t.fg(t.warn).Render("Aplicando: "+runningStep(m.state.Setup)) + "\n")
	}
	if m.infraMsg != "" {
		c := map[string]lipgloss.Style{"ok": t.fg(t.ok), "warn": t.fg(t.warn), "danger": t.fg(t.danger)}[m.infraMsgC]
		b.WriteString("\n" + c.Bold(true).Width(m.w-8).Render(m.infraMsg) + "\n")
	}

	btn := t.pill(false)
	if infra.Checking {
		btn = t.disabledPill()
	}
	b.WriteString("\n" + m.zones.Mark("infra-refresh", btn.Render("Atualizar")) + "  " +
		t.fg(t.muted).Render("Ultima verificacao: "+infra.CheckedAt.Local().Format("02/01/2006 15:04:05")))
	return t.panel(false, m.w-2, h-2).Render(b.String())
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

func (m *model) viewStack(s core.StackStatus, toggleable, selected bool) string {
	t := m.th
	name := lipgloss.NewStyle().Bold(true).Width(12).Render(s.Name)
	var mark, label string
	switch {
	case !s.Enabled && !s.Installed:
		mark, label = t.fg(t.muted).Render("·"), t.fg(t.muted).Render("desligado")
	case s.Error != "":
		mark, label = t.fg(t.danger).Render("✗"), t.fg(t.danger).Render("erro: "+firstLine(s.Error))
	case s.Enabled && !s.Installed:
		mark, label = t.fg(t.warn).Render("○"), t.fg(t.warn).Render("ligado, mas ainda nao subiu")
	case !s.Enabled:
		mark, label = t.fg(t.warn).Render("○"), t.fg(t.warn).Render("desligado, ainda no ar (o proximo Setup derruba)")
	case s.Healthy():
		mark, label = t.fg(t.ok).Render("✓"), t.fg(t.ok).Render("no ar")
	case len(s.Containers) == 0:
		mark, label = t.fg(t.danger).Render("✗"), t.fg(t.danger).Render("sem containers")
	default:
		mark, label = t.fg(t.danger).Render("✗"), t.fg(t.danger).Render("com problema")
	}

	cursor := "  "
	if selected {
		cursor = t.fg(t.accent).Bold(true).Render("› ")
	}
	line := cursor + mark + " " + name + " " + label
	if toggleable {
		st := t.pill(selected)
		text := "Ligar"
		if s.Enabled {
			text = "Desligar"
		}
		if m.busy() {
			st = t.disabledPill()
		}
		line += "  " + m.zones.Mark("toggle:"+s.Name, st.Render(text))
	}
	line += "\n    " + t.fg(t.muted).Render(s.Desc) + "\n"

	for _, c := range s.Containers {
		state := t.fg(t.ok).Render(c.State)
		switch {
		case c.State != "running":
			state = t.fg(t.danger).Render(c.State)
		case c.Health == "unhealthy":
			state = t.fg(t.danger).Render("unhealthy")
		case c.Health == "starting":
			state = t.fg(t.warn).Render("subindo")
		case c.Health == "healthy":
			state = t.fg(t.ok).Render("healthy")
		}
		line += fmt.Sprintf("    %s %s %s\n",
			lipgloss.NewStyle().Width(24).Render(c.Name),
			lipgloss.NewStyle().Width(10).Render(state),
			t.fg(t.muted).Render(c.Status))
	}
	if s.Name == "garage" && s.Enabled {
		line += "    " + t.fg(t.muted).Render("S3: https://"+m.setup.plan.Config.S3Host()+" (regiao garage) · chaves e buckets: docker exec garage /garage key|bucket ...") + "\n"
	}
	return line
}

func (m *model) infraClick(msg tea.MouseMsg) tea.Cmd {
	if m.zones.Get("infra-refresh").InBounds(msg) && !m.state.Infra.Checking {
		return m.stacksCmd()
	}
	for i, name := range optional() {
		if m.zones.Get("toggle:" + name).InBounds(msg) {
			m.infraSel = i
			return m.toggle(name)
		}
	}
	return nil
}

func (m *model) infraKey(k string) tea.Cmd {
	n := len(optional())
	switch k {
	case "r":
		if !m.state.Infra.Checking {
			return m.stacksCmd()
		}
	case "down", "tab":
		m.infraSel = (m.infraSel + 1) % n
	case "up", "shift+tab":
		m.infraSel = (m.infraSel + n - 1) % n
	case "enter", "space":
		return m.toggle(optional()[m.infraSel])
	}
	return nil
}
