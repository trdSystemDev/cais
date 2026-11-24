package ui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/trdSystemDev/cais/internal/core"
)

func (m *model) appsClick(msg tea.MouseMsg) tea.Cmd {
	if m.apps.info != nil {
		m.apps.info = nil // qualquer clique fecha a ajuda
		return nil
	}
	sel, before, cont := m.apps.sel, m.apps.section, m.apps.container
	cmd := m.apps.click(msg)
	force := sel != m.apps.sel || m.apps.section != before || m.apps.container != cont
	if force {
		m.logSelection = logSelection{} // a selecao era dos logs de antes
	}
	return tea.Batch(cmd, m.syncAppLogs(force), m.apps.promoRefresh(false))
}

func (m *model) appsKey(msg tea.KeyPressMsg) tea.Cmd {
	k := msg.String()
	if m.apps.info != nil {
		switch k {
		case "esc", "enter", "space", "q":
			m.apps.info = nil
		}
		return nil
	}
	if m.apps.section == secLogs && !m.apps.creating {
		switch k {
		case "pgup", "pgdown":
			m.scrollLogs(k == "pgup")
			return nil
		case "end":
			m.logsOffset = 0
			return nil
		}
	}
	sel, sec, cont := m.apps.sel, m.apps.section, m.apps.container
	cmd := m.apps.key(msg)
	force := sel != m.apps.sel || sec != m.apps.section || cont != m.apps.container
	if force {
		m.logSelection = logSelection{} // a selecao era dos logs de antes
	}
	return tea.Batch(cmd, m.syncAppLogs(force), m.apps.promoRefresh(false))
}

// scrollLogs rola os logs 10 linhas pra cima (antigos) ou pra baixo.
func (m *model) scrollLogs(up bool) {
	m.logSelection = logSelection{}
	if up {
		m.logsOffset = min(m.logsOffset+10, max(0, len(m.logArea.allLines)-m.logArea.height))
	} else {
		m.logsOffset = max(0, m.logsOffset-10)
	}
}

// syncAppLogs troca o container dos logs quando a secao Logs esta aberta.
func (m *model) syncAppLogs(force bool) tea.Cmd {
	if m.tab != tabApps || m.disconnected {
		return nil
	}
	target := ""
	if a := m.apps.selected(); a != nil && m.apps.section == secLogs && len(a.Containers) > 0 {
		target = a.Containers[min(m.apps.container, len(a.Containers)-1)].Name
	}
	if !force && target == m.logsTarget {
		return nil
	}
	m.logSelection = logSelection{}
	m.logsGeneration++
	m.logsTarget, m.logsText, m.logsError, m.logsOffset = target, "", "", 0
	if target == "" {
		return nil
	}
	return m.fetchAppLogs()
}

func (m *model) fetchAppLogs() tea.Cmd {
	a := m.apps.selected()
	if a == nil || m.logsTarget == "" {
		return nil
	}
	generation, app, target := m.logsGeneration, a.Name, m.logsTarget
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		out, err := m.api.AppLogs(ctx, app, target)
		return appLogsMsg{generation, out, err}
	}
}

func (m *model) onAppLogs(msg appLogsMsg) tea.Cmd {
	if msg.generation != m.logsGeneration || m.tab != tabApps || m.disconnected {
		return nil
	}
	m.logsText, m.logsError = msg.text, ""
	if msg.err != nil {
		m.logsError = msg.err.Error()
	} else if strings.TrimSpace(msg.text) == "" {
		m.logsText = "(o container ainda nao escreveu nada no log)"
	}
	generation := m.logsGeneration
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return appLogsTick(generation) })
}

type appLogsTick uint64

func (m *model) onAppLogsTick(g appLogsTick) tea.Cmd {
	if uint64(g) != m.logsGeneration || m.tab != tabApps || m.disconnected {
		return nil
	}
	if len(m.logSelection.lines) > 0 {
		return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return g })
	}
	return m.fetchAppLogs()
}

func (m *model) viewApps(h int) string {
	t := m.th
	if m.w < 80 {
		m.logArea = logArea{}
		return t.panel(false, m.w-2, max(1, h-2)).Render("Amplie o terminal para pelo menos 80 colunas para ver os apps.")
	}
	leftW := min(36, max(26, m.w/4))
	left := m.apps.viewList(leftW, h)
	rightW := m.w - leftW - 1
	innerH := max(1, h-2)
	var right string
	if a := m.apps.selected(); a != nil && m.apps.section == secLogs {
		right = m.zones.Mark("app-logs", m.viewAppLogs(*a, rightW, innerH))
	} else {
		m.logArea = logArea{}
		right = m.apps.viewDetail(rightW, innerH)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)
}

func (m *model) viewAppLogs(a core.AppStatus, w, h int) string {
	t := m.th
	p := m.apps
	var b strings.Builder
	mark, status, c := appStatus(a, t)
	b.WriteString(t.bold().Render(a.Name) + "  " + t.fg(c).Render(mark+" "+status) + "\n")
	b.WriteString(p.viewSections() + "\n\n")
	name := "nenhum"
	if len(a.Containers) > 0 {
		name = a.Containers[min(p.container, len(a.Containers)-1)].Name
	}
	for i, f := range p.fields {
		if f.id == "log-container" {
			b.WriteString(p.zones.Mark(f.id, "  "+t.fg(t.text).Width(18).Render("Container")+t.selector("‹ "+name+" ›", p.inForm && i == p.focus)))
			for j, sf := range p.fields {
				if sf.id == "app-shell" {
					b.WriteString("  " + p.zones.Mark(sf.id, t.button(sf.label, btnInfo, p.inForm && j == p.focus)))
				}
			}
			// linhas na tela durante a selecao, como no titulo dos logs da Infra
			if pos := m.logPosition(); pos != "" {
				b.WriteString("  " + t.bold().Render("Logs · "+pos))
			}
			b.WriteString("\n")
		}
	}
	if p.shellErr != "" {
		b.WriteString(lipgloss.NewStyle().Width(w-4).Foreground(t.danger).Render("  "+p.shellErr) + "\n")
	}
	b.WriteString("\n")
	empty := ""
	if len(a.Containers) == 0 {
		empty = "Nenhum container: o app ainda nao teve deploy ou esta parado."
	}
	m.writeLogs(&b, empty, w, h)
	return t.panel(p.inForm, w-2, h).MaxHeight(h + 2).Render(b.String())
}
