package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// a abertura mostra a logo e uma tecla pula pro painel
func TestAbertura(t *testing.T) {
	c, _, _ := testCore(t, okChecker)
	m := open(t, c)
	m.splashUntil = time.Now().Add(time.Minute)
	s := screen(m)
	if !strings.Contains(s, "painel da VPS") || !strings.Contains(s, "▀") || strings.Contains(s, "Dashboard") {
		t.Fatalf("abertura:\n%s", s)
	}
	m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if s := screen(m); !strings.Contains(s, "Dashboard") {
		t.Fatalf("a tecla nao pulou a abertura:\n%s", s)
	}
}
