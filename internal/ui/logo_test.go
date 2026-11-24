package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// os mapas so usam letras com cor, e a figura tem o tamanho certo
func TestLogoMapas(t *testing.T) {
	for name, m := range map[string]string{"cais": logoFull, "icone": logoIcon} {
		for _, row := range strings.Split(strings.TrimRight(m, "\n"), "\n") {
			if len(row) != 56 {
				t.Errorf("%s: linha com %d pixels", name, len(row))
			}
			for i := 0; i < len(row); i++ {
				if row[i] != '.' && logoColors[row[i]] == nil {
					t.Errorf("%s: cor desconhecida %q", name, row[i])
				}
			}
		}
	}
	for name, s := range map[string]string{"Full": Logo(), "Icon": LogoIcon()} {
		lines := strings.Split(s, "\n")
		want := map[string]int{"Full": 29, "Icon": 22}[name]
		if len(lines) != want {
			t.Errorf("%s: %d linhas, esperava %d", name, len(lines), want)
		}
		for _, l := range lines {
			if w := ansi.StringWidth(l); w != 56 {
				t.Errorf("%s: linha com %d colunas", name, w)
			}
		}
	}
}
