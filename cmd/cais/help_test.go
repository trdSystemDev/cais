package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// todo comando que cria flags (newFlags) tem o proprio -h
func TestTodoComandoTemAjuda(t *testing.T) {
	names := []string{"daemon", "state", "check", "setup config", "setup secret", "service", "apps",
		"db create", "db list", "db delete", "bucket create", "bucket list", "bucket delete"}
	for _, sub := range []string{"create", "deploy", "guide", "env", "db", "bucket", "restart", "stop", "logs", "delete"} {
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

var update = flag.Bool("update", false, "regrava os arquivos golden de testdata")

// A ajuda inteira (cais help e o -h de cada comando) comparada com o
// arquivo golden: mudanca sem querer no texto aparece aqui. Mudanca de
// proposito: go test ./cmd/cais -run Golden -update
func TestAjudaGolden(t *testing.T) {
	var b strings.Builder
	b.WriteString(overview())
	for _, s := range sections {
		if s.group != "" {
			b.WriteString("\n===== cais " + s.group + " -h\n" + helpFor(s.group))
		}
		for _, c := range s.cmds {
			b.WriteString("\n===== cais " + c.name + " -h\n" + helpFor(c.name))
		}
	}
	golden := filepath.Join("testdata", "help.golden")
	if *update {
		os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(golden, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (gere com -update)", err)
	}
	if b.String() != string(want) {
		t.Fatalf("a ajuda mudou; se foi de proposito, rode com -update e confira o diff de %s", golden)
	}
}
