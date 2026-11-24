package ui

// A logo do cais (guindaste, conteineres, pier e mar) em meio-blocos
// coloridos: cada caractere sao dois pixels, o de cima na cor do texto e o
// de baixo no fundo. Os mapas (logo/*.txt) saem do docs/logo/gen.py, a
// mesma fonte do SVG.

import (
	_ "embed"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
)

//go:embed logo/cais.txt
var logoFull string

//go:embed logo/cais-icone.txt
var logoIcon string

// colors: a letra de cada pixel no mapa (maiuscula = o tom escuro).
var logoColors = map[byte]color.Color{
	'o': lipgloss.Color("#FFA500"), 'O': lipgloss.Color("#E07B00"),
	'y': lipgloss.Color("#FBBF24"), 'Y': lipgloss.Color("#8A6508"),
	'g': lipgloss.Color("#3DB559"), 'G': lipgloss.Color("#1D7A36"),
	'b': lipgloss.Color("#1E90F0"), 'B': lipgloss.Color("#0B5AA6"),
	'p': lipgloss.Color("#A3ACB9"), 'P': lipgloss.Color("#7C8696"),
	'w': lipgloss.Color("#1E90F0"),
	't': lipgloss.Color("#FFFFFF"),
}

// Logo e a figura com o nome CAIS embaixo (56x29).
func Logo() string { return renderLogo(logoFull) }

// LogoIcon e so a figura (56x22).
func LogoIcon() string { return renderLogo(logoIcon) }

func renderLogo(m string) string {
	rows := strings.Split(strings.TrimRight(m, "\n"), "\n")
	var b strings.Builder
	for j := 0; j < len(rows); j += 2 {
		top := rows[j]
		bot := strings.Repeat(".", len(top))
		if j+1 < len(rows) {
			bot = rows[j+1]
		}
		var line strings.Builder
		for i := 0; i < len(top); i++ {
			t, u := logoColors[top[i]], logoColors[bot[i]]
			s := lipgloss.NewStyle()
			switch {
			case t == nil && u == nil:
				line.WriteByte(' ')
				continue
			case u == nil:
				line.WriteString(s.Foreground(t).Render("▀"))
			case t == nil:
				line.WriteString(s.Foreground(u).Render("▄"))
			default:
				line.WriteString(s.Foreground(t).Background(u).Render("▀"))
			}
		}
		// sem cortar os espacos do fim: todas as linhas com a mesma largura,
		// senao centralizar (JoinVertical, Place) desloca cada uma
		b.WriteString(line.String())
		if j+2 < len(rows) {
			b.WriteByte('\n')
		}
	}
	return b.String()
}
