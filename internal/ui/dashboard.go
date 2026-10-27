package ui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/trdSystemDev/cais/internal/core"
)

// viewDashboard mostra o estado do ambiente. Nao guarda estado proprio: o
// botao so dispara a acao no core, e a tela sai da foto que o core publica
// (venha a acao daqui, do MCP ou de onde for).
func (m *model) viewDashboard(h int) string {
	e := m.state.Environment
	body := m.th.bold().Render("Ambiente") + "\n" + m.renderEnvironment(e)

	btn := m.th.pill(false)
	if e.Checking {
		btn = m.th.disabledPill()
	}
	button := m.zones.Mark("check", btn.Render("Verificar novamente"))
	return m.th.panel(false, m.w-2, h-2).Render(body + "\n\n" + button)
}

func (m *model) renderEnvironment(e core.Environment) string {
	t := m.th
	muted := t.fg(t.muted)
	if e.Checking || !e.Checked() {
		return "\n" + t.fg(t.warn).Render("Verificando o ambiente...")
	}

	var b strings.Builder
	b.WriteString("\n" + t.bold().Render("Docker") + "\n")
	b.WriteString(m.component("CLI", e.Docker.CLI))
	if !e.Docker.Installed {
		// sem o CLI nao da pra checar o resto; nao repete a mesma mensagem
		b.WriteString(muted.Render("  -  Daemon   depende do CLI") + "\n")
		b.WriteString(muted.Render("  -  Compose  depende do CLI") + "\n")
	} else {
		b.WriteString(m.component("Daemon", e.Docker.Daemon))
		b.WriteString(m.component("Compose", e.Docker.Compose))
	}

	b.WriteString("\n" + t.bold().Render("Sistema") + "\n")
	b.WriteString("  " + muted.Render("Host     ") + e.Host.Hostname + "\n")
	b.WriteString("  " + muted.Render("Sistema  ") + e.Host.OS + "/" + e.Host.Arch + "\n")
	if !e.Host.Supported() {
		b.WriteString(t.fg(t.warn).Render("  ! O cais foi feito pra rodar na VPS Linux; aqui so da pra navegar na interface.") + "\n")
	}

	b.WriteString("\n")
	if e.Ready() {
		b.WriteString(t.fg(t.ok).Bold(true).Render("Pronto.") + " O Docker esta funcionando -- o proximo passo e a aba Setup.\n")
	} else {
		b.WriteString(t.fg(t.danger).Bold(true).Render("Resolva os itens em vermelho") + " antes de usar o Setup.\n")
	}
	b.WriteString("\n" + muted.Render("Ultima verificacao: "+e.CheckedAt.Local().Format("02/01/2006 15:04:05")))
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
