package fsx

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func names(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func TestWriteAtomicGravaConteudoEPermissao(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := WriteAtomic(p, []byte("a: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil || string(b) != "a: 1\n" {
		t.Fatalf("conteudo %q, erro %v", b, err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("permissao %v; esperado 0600", st.Mode().Perm())
	}
}

// Sobrescrever deixa so o arquivo final na pasta: nenhum temporario sobra.
func TestWriteAtomicSobrescreveSemSobrarTemporario(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "meta.yml")
	for _, v := range []string{"um", "dois", "tres"} {
		if err := WriteAtomic(p, []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if b, _ := os.ReadFile(p); string(b) != "tres" {
		t.Fatalf("conteudo %q", b)
	}
	if n := names(t, dir); len(n) != 1 || n[0] != "meta.yml" {
		t.Fatalf("pasta com %v; esperado so meta.yml", n)
	}
}

// Pasta que nao existe: erro, e nada fica pra tras.
func TestWriteAtomicPastaInexistente(t *testing.T) {
	dir := t.TempDir()
	if err := WriteAtomic(filepath.Join(dir, "nao", "existe"), []byte("x"), 0o644); err == nil {
		t.Fatal("gravou numa pasta que nao existe")
	}
	if n := names(t, dir); len(n) != 0 {
		t.Fatalf("sobrou %v", n)
	}
}

// Destino que e uma pasta: o rename falha e o temporario e apagado.
func TestWriteAtomicFalhaNoRenameNaoDeixaTemporario(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "alvo", "cheia"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomic(filepath.Join(dir, "alvo"), []byte("x"), 0o644); err == nil {
		t.Fatal("sobrescreveu uma pasta com arquivo")
	}
	if n := names(t, dir); len(n) != 1 || n[0] != "alvo" {
		t.Fatalf("pasta com %v; esperado so alvo", n)
	}
}

// Rename de pasta (a troca src -> src.prev do deploy) entre pastas
// diferentes.
func TestRenamePastaEntrePastas(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a", "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(src, "compose.yml"), []byte("services: {}"), 0o644)
	os.MkdirAll(filepath.Join(dir, "b"), 0o755)

	dst := filepath.Join(dir, "b", "src.prev")
	if err := Rename(src, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "compose.yml")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("origem ainda existe: %v", err)
	}
}

func TestRenameOrigemInexistente(t *testing.T) {
	dir := t.TempDir()
	if err := Rename(filepath.Join(dir, "nao"), filepath.Join(dir, "sim")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("erro %v; esperado ErrNotExist", err)
	}
}

// Remove de arquivo que nao existe devolve o erro do os.Remove (quem chama
// decide se ignora, como antes).
func TestRemove(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "swap.yml")
	os.WriteFile(p, []byte("x"), 0o644)
	if err := Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := Remove(p); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("erro %v; esperado ErrNotExist", err)
	}
}

func TestSyncDirInexistente(t *testing.T) {
	if err := SyncDir(filepath.Join(t.TempDir(), "nao")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("erro %v", err)
	}
}

// Prova de que a pasta passa pelo fsync: numa pasta sem leitura (0300) o
// rename funciona, mas abrir a pasta pro fsync falha. Se a funcao devolve
// erro ali, ela tentou o fsync (root le qualquer pasta: pula).
func semLeitura(t *testing.T) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignora a permissao da pasta")
	}
	dir := filepath.Join(t.TempDir(), "sem-leitura")
	if err := os.Mkdir(dir, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	return dir
}

func TestWriteAtomicFazFsyncDaPasta(t *testing.T) {
	dir := semLeitura(t)
	err := WriteAtomic(filepath.Join(dir, "meta.yml"), []byte("x"), 0o644)
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("erro %v; esperado a falha do fsync da pasta", err)
	}
}

func TestRenameFazFsyncDaPastaDeDestino(t *testing.T) {
	dir := semLeitura(t)
	src := filepath.Join(t.TempDir(), "src")
	os.Mkdir(src, 0o755)
	if err := Rename(src, filepath.Join(dir, "src")); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("erro %v; esperado a falha do fsync do destino", err)
	}
}

func TestRenameFazFsyncDaPastaDeOrigem(t *testing.T) {
	dir := semLeitura(t)
	if err := os.Mkdir(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Rename(filepath.Join(dir, "src"), filepath.Join(t.TempDir(), "src")); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("erro %v; esperado a falha do fsync da origem", err)
	}
}

func TestRemoveFazFsyncDaPasta(t *testing.T) {
	dir := semLeitura(t)
	p := filepath.Join(dir, "swap.yml")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Remove(p); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("erro %v; esperado a falha do fsync da pasta", err)
	}
}
