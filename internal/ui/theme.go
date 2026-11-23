package ui

import (
	"image/color"
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// theme sao as cores da TUI, escolhidas pelo fundo do terminal (claro ou
// escuro): o Bubble Tea pergunta a cor do fundo ao terminal na partida.
// Todo texto colorido tem par certo pro fundo -- nada depende da paleta ANSI
// do tema, que pode remapear "azul" e "amarelo" pra cores ilegiveis.
type theme struct {
	text, muted, accent, accentInk color.Color
	fieldBg, fieldFg, border       color.Color
	ok, warn, danger, info         color.Color
	// violet e a segunda cor dos graficos (a primeira e o info)
	violet color.Color
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
		info:      ld(c("#1a5fb4"), c("#8ab4f8")),
		violet:    ld(c("#7b3fb4"), c("#c9a0f5")),
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

// pill e o estilo das abas: fundo e texto sempre em par.
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

// tipo de botao: a cor diz o que ele faz
type btnKind int

const (
	btnNormal    btnKind = iota // acao principal (verde)
	btnDanger                   // apaga algo (vermelho)
	btnWarn                     // interrompe, como parar (amarelo)
	btnInfo                     // acao secundaria, como reiniciar (azul)
	btnNeutral                  // alternador/opcao (cinza, como aba inativa)
	btnOff                      // desabilitado agora
	btnDangerOff                // apagar ainda desabilitado: cinza com texto vermelho
)

func (t theme) btnColor(k btnKind) color.Color {
	switch k {
	case btnDanger, btnDangerOff:
		return t.danger
	case btnWarn:
		return t.warn
	case btnInfo:
		return t.info
	case btnOff, btnNeutral:
		return t.border
	}
	return t.accent
}

// button e o botao de acao: no estilo das abas, com fundo na cor do tipo
// (verde, vermelho, amarelo, azul), pra nao se confundir com campo de texto
// (cinza). Com foco, inverte pra cor do texto.
func (t theme) button(label string, k btnKind, focused bool) string {
	s := lipgloss.NewStyle().Padding(0, 2).Bold(true)
	switch {
	case k == btnOff:
		return s.Background(t.fieldBg).Foreground(t.muted).Bold(false).Render(label)
	case k == btnDangerOff:
		return s.Background(t.fieldBg).Foreground(t.danger).Bold(false).Render(label)
	case focused:
		return s.Background(t.text).Foreground(t.accentInk).Render(label)
	case k == btnNeutral:
		return s.Background(t.fieldBg).Foreground(t.fieldFg).Bold(false).Render(label)
	}
	return s.Background(t.btnColor(k)).Foreground(t.accentInk).Render(label)
}

// chip e o botao dentro de uma linha de texto ou tabela (remover, apagar,
// desligar): uma linha so, com os colchetes na cor da borda.
func (t theme) chip(label string, k btnKind, focused bool) string {
	c := t.btnColor(k)
	if focused && k != btnOff && k != btnDangerOff {
		return lipgloss.NewStyle().Background(c).Foreground(t.accentInk).Bold(true).Render("[" + label + "]")
	}
	text := t.fg(t.text)
	if k == btnOff || k == btnDangerOff {
		text = t.fg(t.muted)
	}
	return t.fg(c).Render("[") + text.Render(label) + t.fg(c).Render("]")
}

// styleInput pinta um campo de texto: fundo cinza, cursor fixo (sem piscar:
// o textinput desenha o cursor apagado sem fundo nenhum).
func (t theme) styleInput(ti *textinput.Model) {
	s := textinput.DefaultStyles(true)
	for _, st := range []*textinput.StyleState{&s.Focused, &s.Blurred} {
		st.Text = lipgloss.NewStyle().Foreground(t.fieldFg).Background(t.fieldBg)
		st.Placeholder = lipgloss.NewStyle().Foreground(t.muted).Background(t.fieldBg).Italic(true)
	}
	s.Cursor.Color = t.accent
	s.Cursor.Blink = false
	ti.SetStyles(s)
}

// input desenha o campo com largura w. Com foco usa o textinput (cursor e
// janela visivel); sem foco desenha o comeco do valor, com o fundo inteiro --
// o View do textinput sem foco deixa a celula do cursor sem fundo.
func (t theme) input(ti *textinput.Model, w int) string {
	box := lipgloss.NewStyle().Background(t.fieldBg).Foreground(t.fieldFg).Width(w)
	if ti.Focused() {
		return box.Render(ti.View())
	}
	v := ti.Value()
	if v == "" {
		return box.Render(lipgloss.NewStyle().Foreground(t.muted).Background(t.fieldBg).Italic(true).
			Render(ansi.Truncate(ti.Placeholder, w, "…")))
	}
	if ti.EchoMode == textinput.EchoPassword {
		v = strings.Repeat(string(ti.EchoCharacter), min(len([]rune(v)), w))
	}
	return box.Render(ansi.Truncate(v, w, "…"))
}

// selector e um seletor de opcoes (‹ opcao ›): fundo de campo, sem borda, pra
// nao parecer botao. Com foco, o texto fica na cor de destaque.
func (t theme) selector(text string, focused bool) string {
	s := lipgloss.NewStyle().Background(t.fieldBg).Foreground(t.fieldFg).Padding(0, 1)
	if focused {
		s = s.Foreground(t.accent).Bold(true)
	}
	return s.Render(text)
}

// rule e a linha que separa as partes de um formulario.
func (t theme) rule(w int) string { return t.fg(t.border).Render(strings.Repeat("─", max(0, w))) }

// infoBox e o texto de ajuda de uma parte da tela, aberto pelo [i].
type infoBox struct{ title, text string }

// modal desenha o infoBox no meio de uma tela w x h.
func (t theme) modal(in infoBox, w int, close string) string {
	width := min(72, w-4)
	body := t.bold().Render(in.title) + "\n\n" +
		lipgloss.NewStyle().Foreground(t.text).Width(width-6).Render(in.text) + "\n\n" +
		close + t.fg(t.muted).Render("  esc fecha")
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(t.accent).
		Padding(1, 2).Width(width).Render(body)
}
