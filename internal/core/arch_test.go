package core

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestNucleoNaoDependeDeInterface garante que so internal/ui (e o main que
// liga tudo) importa bibliotecas de tela. Se o core ou qualquer servico
// passar a depender da TUI, o MCP herdaria esse acoplamento.
func TestNucleoNaoDependeDeInterface(t *testing.T) {
	forbidden := []string{
		"github.com/rivo/tview",
		"github.com/gdamore/tcell",
		"charm.land/",
		"github.com/lrstanley/bubblezone",
		"github.com/trdSystemDev/cais/internal/ui",
		"github.com/trdSystemDev/cais/internal/charmui",
	}

	err := filepath.WalkDir("..", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "ui" || d.Name() == "charmui" || d.Name() == "templates" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			for _, bad := range forbidden {
				if strings.HasPrefix(p, bad) {
					t.Errorf("%s importa %s: so internal/ui pode depender de interface", path, p)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
