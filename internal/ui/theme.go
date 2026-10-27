package ui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// theme sao as cores da TUI, escolhidas pelo fundo do terminal (claro ou
// escuro): o Bubble Tea pergunta a cor do fundo ao terminal na partida.
// Todo texto colorido tem par certo pro fundo -- nada depende da paleta ANSI
// do tema, que pode remapear "azul" e "amarelo" pra cores ilegiveis.
type theme struct {
	text, muted, accent, accentInk color.Color
	fieldBg, fieldFg, border       color.Color
	ok, warn, danger               color.Color
}

func newTheme(isDark bool) theme {
	ld := lipgloss.LightDark(isDark)
	c := lipgloss.Color
	return theme{
		text:      ld(c("#1b1f27"), c("#e6e9ef")),
		muted:     ld(c("#5b6472"), c("#9aa3b2")),
		accent:    ld(c("#13795b"), c("#7fd1ae")),
		accentInk: ld(c("#ffffff"), c("#10241c")),
		fieldBg:   ld(c("#eceff4"), c("#2b3240")),
		fieldFg:   ld(c("#1b1f27"), c("#f2f4f8")),
		border:    ld(c("#c3c9d2"), c("#3b4556")),
		ok:        ld(c("#13795b"), c("#7fd1ae")),
		warn:      ld(c("#9a6700"), c("#f0c674")),
		danger:    ld(c("#b42318"), c("#ff8a80")),
	}
}

func (t theme) fg(c color.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }
func (t theme) bold() lipgloss.Style            { return lipgloss.NewStyle().Foreground(t.text).Bold(true) }

func (t theme) panel(focused bool, w, h int) lipgloss.Style {
	b := t.border
	if focused {
		b = t.accent
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(b).
		Padding(0, 1).Width(w).Height(h)
}

// pill e o estilo de aba e botao: fundo e texto sempre em par.
func (t theme) pill(active bool) lipgloss.Style {
	s := lipgloss.NewStyle().Padding(0, 2)
	if active {
		return s.Background(t.accent).Foreground(t.accentInk).Bold(true)
	}
	return s.Background(t.fieldBg).Foreground(t.fieldFg)
}

func (t theme) dangerPill() lipgloss.Style {
	return lipgloss.NewStyle().Padding(0, 2).Background(t.danger).Foreground(t.accentInk).Bold(true)
}

func (t theme) disabledPill() lipgloss.Style {
	return lipgloss.NewStyle().Padding(0, 2).Background(t.fieldBg).Foreground(t.muted).Faint(true)
}
