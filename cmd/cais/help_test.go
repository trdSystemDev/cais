package main

import (
	"strings"
	"testing"
)

// todo comando que cria flags (newFlags) tem o proprio -h
func TestTodoComandoTemAjuda(t *testing.T) {
	names := []string{"daemon", "state", "check", "setup config", "setup secret", "service", "apps",
		"db create", "db list", "db delete", "bucket create", "bucket list", "bucket delete"}
	for _, sub := range []string{"create", "deploy", "guide", "env", "db", "bucket", "secrets", "secret", "restart", "stop", "logs", "delete"} {
		names = append(names, "app "+sub)
	}
	for _, n := range names {
		h := helpFor(n)
		if !strings.HasPrefix(h, "Uso: cais "+strings.Fields(n)[0]) {
			t.Errorf("%s sem ajuda propria:\n%s", n, h)
		}
	}
	for _, g := range []string{"app", "db", "bucket", "setup"} {
		h := helpFor(g)
		if !strings.Contains(h, "Detalhes: cais "+g+" <comando> -h") || strings.Contains(h, "Servicos opcionais") {
			t.Errorf("grupo %s:\n%s", g, h)
		}
	}
	// sem grupo nem comando: o help inteiro
	if h := helpFor("nao-existe"); !strings.HasPrefix(h, "cais -- painel da VPS") {
		t.Errorf("desconhecido:\n%s", h)
	}
}

// o help geral lista todos os comandos e nenhuma linha passa de 100 colunas
func TestOverview(t *testing.T) {
	o := overview()
	for _, s := range sections {
		for _, c := range s.cmds {
			if !strings.Contains(o, strings.TrimSpace("cais "+c.usage)) {
				t.Errorf("falta %s no help", c.name)
			}
		}
	}
	for _, s := range sections {
		for _, c := range s.cmds {
			for _, l := range strings.Split(helpFor(c.name), "\n") {
				if len(l) > 100 && !strings.HasPrefix(l, "O socket padrao") {
					t.Errorf("%s: linha longa: %s", c.name, l)
				}
			}
		}
	}
}
