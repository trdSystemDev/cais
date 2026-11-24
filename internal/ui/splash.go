package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// A abertura: a logo do cais no meio da tela por um instante, enquanto a
// primeira foto do daemon chega. Uma tecla ou um clique pula.

const splashFor = 1200 * time.Millisecond

type splashDoneMsg struct{}

func (m *model) splashing() bool { return time.Now().Before(m.splashUntil) }

func (m *model) splashCmd() tea.Cmd {
	if m.splashUntil.IsZero() {
		return nil
	}
	return tea.Tick(time.Until(m.splashUntil), func(time.Time) tea.Msg { return splashDoneMsg{} })
}

func (m *model) viewSplash() string {
	art := Logo()
	// terminal pequeno: so a figura, ou nada
	switch {
	case m.w < 58 || m.h < 24:
		art = ""
	case m.h < 33:
		art = LogoIcon()
	}
	below := m.th.fg(m.th.muted).Render("painel da VPS")
	content := lipgloss.JoinVertical(lipgloss.Center, art, "", below)
	return lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center, content)
}
