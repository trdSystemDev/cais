package charmui

import (
	"fmt"
	"image/color"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/trdSystemDev/cais/internal/bootstrap"
)

const formWidth = 50

func (m *model) View() tea.View {
	v := tea.NewView("")
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	if m.w == 0 {
		return v
	}
	header := m.viewTabs()
	footer := m.th.fg(m.th.muted).Render(" clique ou 1-6 troca de aba · tab/↑↓ campo · espaco marca · ctrl+s salva · ctrl+r executa · ctrl+q sai")
	bodyH := m.h - lipgloss.Height(header) - lipgloss.Height(footer)

	var body string
	if m.tab == setupTab {
		body = m.viewSetup(bodyH)
	} else {
		body = m.th.panel(false, m.w-2, bodyH-2).Render(
			"\n" + m.th.bold().Render(tabs[m.tab]) + "\n\n" +
				m.th.fg(m.th.muted).Render("No prototipo so a aba Setup existe."))
	}
	v.SetContent(m.zones.Scan(lipgloss.JoinVertical(lipgloss.Left, header, body, footer)))
	return v
}

func (m *model) viewTabs() string {
	var parts []string
	for i, t := range tabs {
		parts = append(parts, m.zones.Mark("tab:"+t, m.th.pill(i == m.tab).Render(t)), " ")
	}
	left := lipgloss.JoinHorizontal(lipgloss.Top, parts...)
	quit := m.zones.Mark("quit", lipgloss.NewStyle().Padding(0, 2).
		Background(m.th.danger).Foreground(m.th.accentInk).Render("Sair"))
	gap := m.w - lipgloss.Width(left) - lipgloss.Width(quit)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + quit
}

func (m *model) viewSetup(h int) string {
	form := m.viewForm()
	status := ""
	if m.status != "" {
		c := map[string]color.Color{"ok": m.th.ok, "warn": m.th.warn, "danger": m.th.danger}[m.statusC]
		status = lipgloss.NewStyle().Bold(true).Foreground(c).Width(formWidth).Render(m.status)
	}
	left := m.th.panel(true, formWidth+4, h-2).Render(
		m.th.bold().Render("Configuracao") + "\n\n" + form + "\n\n" + status)

	rightW := m.w - lipgloss.Width(left) - 1
	if rightW < 20 {
		rightW = 20
	}
	title, content := "Plano", m.viewPlan(rightW-4)
	if m.showRun {
		title, content = "Execucao", m.viewRun(rightW-4)
	}
	right := m.th.panel(false, rightW, h-2).Render(m.th.bold().Render(title) + "\n" + content)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)
}

func (m *model) viewForm() string {
	field := lipgloss.NewStyle().Background(m.th.fieldBg).Foreground(m.th.fieldFg).Width(30)
	var rows []string
	var buttons []string
	for i, it := range m.items {
		focused := i == m.focus
		marker, label := "  ", m.th.fg(m.th.text).Width(18).Render(it.label)
		if focused {
			marker = m.th.fg(m.th.accent).Bold(true).Render("› ")
			label = m.th.fg(m.th.accent).Bold(true).Width(18).Render(it.label)
		}

		var value string
		switch it.kind {
		case kindInput:
			value = field.Render(it.input.View())
		case kindSelect:
			opt := []string{"Let's Encrypt", "CA local (teste)"}[m.tls]
			value = field.Render("‹ " + opt + " ›")
		case kindCheck:
			name := strings.TrimPrefix(it.id, "stack:")
			box := m.th.fg(m.th.muted).Render("[ ]")
			if m.stacks[name] {
				box = m.th.fg(m.th.accent).Bold(true).Render("[x]")
			}
			value = box
		case kindButton:
			st := m.th.pill(focused)
			if it.id == "run" && (m.running || !m.plan.Ready()) {
				st = lipgloss.NewStyle().Padding(0, 2).Foreground(m.th.muted).Background(m.th.fieldBg).Faint(true)
			}
			buttons = append(buttons, m.zones.Mark(it.id, st.Render(it.label)))
			continue
		}
		rows = append(rows, m.zones.Mark(it.id, marker+label+value))
	}
	return strings.Join(rows, "\n") + "\n\n  " + strings.Join(buttons, "  ")
}

func (m *model) viewPlan(w int) string {
	p := m.plan
	t := m.th
	muted := t.fg(t.muted)
	wrap := lipgloss.NewStyle().Width(w)
	var b strings.Builder

	if m.saveErr != "" {
		b.WriteString("\n" + t.fg(t.danger).Bold(true).Render("Nao salvou") + "\n")
		for _, l := range strings.Split(m.saveErr, "\n") {
			b.WriteString(wrap.Foreground(t.danger).Render("  "+l) + "\n")
		}
	}
	if !p.Configured {
		b.WriteString("\n" + t.fg(t.warn).Bold(true).Render("Ainda sem configuracao.") + "\n\n")
		b.WriteString(wrap.Render("Preencha dominio e e-mail, escolha as stacks e salve (ctrl+s).") + "\n")
		return b.String()
	}

	b.WriteString("\n" + t.bold().Render("Stacks") + "\n")
	for _, s := range p.Stacks {
		changeColor := map[string]color.Color{bootstrap.ChangeNew: t.ok, bootstrap.ChangeModified: t.warn}[s.Change]
		if changeColor == nil {
			changeColor = t.muted
		}
		change := t.fg(changeColor).Render(fmt.Sprintf("%-9s", s.Change))
		b.WriteString(fmt.Sprintf("  %-11s %s %s\n", s.Name, change, muted.Render(s.Desc)))
	}
	if len(p.Disable) > 0 {
		b.WriteString("\n" + t.bold().Render("Desativadas") + " " + muted.Render("(derrubadas no Setup; os volumes ficam)") + "\n")
		for _, s := range p.Disable {
			b.WriteString(fmt.Sprintf("  %-11s %s\n", s.Name, muted.Render(s.Desc)))
		}
	}
	if len(p.CreateSecrets) > 0 {
		b.WriteString("\n" + t.bold().Render("Secrets a gerar") + "\n" + wrap.Render("  "+strings.Join(p.CreateSecrets, ", ")) + "\n")
	}
	for _, pr := range p.Problems {
		b.WriteString("\n" + wrap.Foreground(t.danger).Render(pr) + "\n")
	}
	if len(p.Missing) > 0 {
		b.WriteString("\n" + t.fg(t.danger).Bold(true).Render("Falta informar") + "\n")
		for _, x := range p.Missing {
			b.WriteString(wrap.Render("  "+t.fg(t.danger).Render(x.Name)+": "+x.Desc) + "\n")
		}
	}
	if p.Ready() {
		b.WriteString("\n" + t.fg(t.ok).Bold(true).Render("Pronto pra executar") + muted.Render(" (ctrl+r)") + "\n")
	}
	b.WriteString(m.viewAccess(w))
	return b.String()
}

func (m *model) viewRun(w int) string {
	t := m.th
	s := m.state.Setup
	var b strings.Builder
	b.WriteString("\n")
	if len(s.Steps) == 0 {
		return b.String() + t.fg(t.warn).Render("Comecando...")
	}
	for _, st := range s.Steps {
		var mark string
		switch st.Status {
		case bootstrap.Done:
			mark = t.fg(t.ok).Render("✓")
		case bootstrap.Running:
			mark = t.fg(t.warn).Render("◐")
		case bootstrap.Failed:
			mark = t.fg(t.danger).Render("✗")
		default:
			mark = t.fg(t.muted).Render("·")
		}
		line := mark + " " + st.Desc
		if st.Detail != "" && st.Status != bootstrap.Failed {
			line += t.fg(t.muted).Render(" — " + st.Detail)
		}
		b.WriteString(lipgloss.NewStyle().Width(w).Render(line) + "\n")
	}
	switch {
	case s.Running || m.running:
		b.WriteString("\n" + t.fg(t.warn).Render("Executando... pode sair: o Setup continua no daemon.") + "\n")
	case s.Error != "":
		b.WriteString("\n" + lipgloss.NewStyle().Width(w).Foreground(t.danger).Render("Parou: "+s.Error) + "\n")
	default:
		b.WriteString("\n" + t.fg(t.ok).Bold(true).Render("Setup concluido.") + "\n")
		b.WriteString(m.viewAccess(w))
	}
	return b.String()
}

func (m *model) viewAccess(w int) string {
	t, p := m.th, m.plan
	muted := t.fg(t.muted)
	wrap := lipgloss.NewStyle().Width(w)
	var b strings.Builder
	if len(p.Databases) > 0 {
		b.WriteString("\n" + t.bold().Render("Bancos") + " " + muted.Render("(so por tunel SSH)") + "\n")
		b.WriteString(wrap.Foreground(t.accent).Render("  "+p.Tunnel) + "\n")
		for _, d := range p.Databases {
			b.WriteString(wrap.Render(fmt.Sprintf("  %-8s localhost:%-5d usuario %-8s %s", d.Name, d.Port, d.User,
				muted.Render("senha: sudo cat "+d.PasswordFile))) + "\n")
		}
	}
	if p.CAFile != "" {
		b.WriteString("\n" + t.bold().Render("CA local") + " " + muted.Render("(confie nela nesta maquina)") + "\n")
		if p.Config.HasStack("registry") {
			dir := "/etc/docker/certs.d/" + p.Config.RegistryHost()
			b.WriteString(wrap.Render("  sudo mkdir -p "+dir) + "\n")
			b.WriteString(wrap.Render("  sudo cp "+p.CAFile+" "+dir+"/ca.crt") + "\n")
		}
		b.WriteString(wrap.Render("  sudo trust anchor --store "+p.CAFile) + "\n")
	}
	return b.String()
}
