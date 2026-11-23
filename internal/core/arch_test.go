package core

import (
	"go/ast"
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
		"charm.land/",
		"github.com/lrstanley/bubblezone",
		"github.com/trdSystemDev/cais/internal/ui",
	}

	err := filepath.WalkDir("..", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "ui" || d.Name() == "templates" {
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

// TestSemHTTPDefaultClient garante que nenhum codigo do cais (fora dos
// testes) usa o cliente HTTP padrao, que nao tem prazo: uma chamada presa
// seguraria o deploy, o backup ou o desligamento do daemon. Cada pacote
// tem um http.Client com Timeout.
func TestSemHTTPDefaultClient(t *testing.T) {
	semPrazo := map[string]bool{"DefaultClient": true, "Get": true, "Head": true, "Post": true, "PostForm": true}
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "testdata", "dist", "vendor", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		http := ""
		for _, imp := range f.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == "net/http" {
				http = "http"
				if imp.Name != nil {
					http = imp.Name.Name
				}
			}
		}
		if http == "" {
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == http && semPrazo[sel.Sel.Name] {
				t.Errorf("%s: %s.%s usa o cliente HTTP sem prazo; use um http.Client com Timeout",
					fset.Position(sel.Pos()), http, sel.Sel.Name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
