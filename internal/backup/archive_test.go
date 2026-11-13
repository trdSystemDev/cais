package backup

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestArquivoIdaEVolta(t *testing.T) {
	pub, priv, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "apps", "web", "src"), 0o755)
	os.WriteFile(filepath.Join(dir, "apps", "web", "app.yaml"), []byte("repo: o/web\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "apps", "web", "src", "main.go"), []byte("codigo"), 0o644)
	os.MkdirAll(filepath.Join(dir, "secrets"), 0o700)
	os.WriteFile(filepath.Join(dir, "secrets", "jwt"), []byte("segredo"), 0o444)

	var out bytes.Buffer
	w, err := NewWriter(&out, pub, t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.AddStream(context.Background(), "postgres/loja.sql", func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, "CREATE TABLE x();\n")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	skip := func(rel string, d fs.DirEntry) bool { return d.IsDir() && strings.HasSuffix(rel, "/src") }
	if err := w.AddDir("state/data", dir, skip); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out.Bytes(), []byte("segredo")) || bytes.Contains(out.Bytes(), []byte("CREATE TABLE")) {
		t.Fatal("conteudo em texto puro no arquivo")
	}
	if !slices.Equal(w.Files(), []string{"postgres/loja.sql", "state/data/apps/web/app.yaml", "state/data/secrets/jwt"}) {
		t.Errorf("arquivos %v", w.Files())
	}

	r, err := NewReader(bytes.NewReader(out.Bytes()), priv)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	modes := map[string]int64{}
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(r)
		got[h.Name], modes[h.Name] = string(data), h.Mode
	}
	if got["postgres/loja.sql"] != "CREATE TABLE x();\n" || got["state/data/secrets/jwt"] != "segredo" {
		t.Errorf("conteudo %v", got)
	}
	if modes["state/data/secrets/jwt"]&0o777 != 0o444 {
		t.Errorf("permissao do secret %o", modes["state/data/secrets/jwt"])
	}
	if _, ok := got["state/data/apps/web/src/main.go"]; ok {
		t.Error("o codigo do app entrou no backup")
	}

	// outra chave nao abre
	_, other, _ := NewKey()
	if _, err := NewReader(bytes.NewReader(out.Bytes()), other); err == nil {
		t.Error("abriu com outra chave")
	}
}
