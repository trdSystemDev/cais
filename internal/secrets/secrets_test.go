package secrets

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

func newStore(t *testing.T) FileStore {
	return FileStore{Dir: filepath.Join(t.TempDir(), "secrets")}
}

func TestFileStorePermissoesEIdaEVolta(t *testing.T) {
	s := newStore(t)
	if err := s.Put("postgres_password", []byte("abc")); err != nil {
		t.Fatal(err)
	}
	// regravar um arquivo 0444 tem que funcionar (rename por cima)
	if err := s.Put("postgres_password", []byte("def")); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("postgres_password")
	if err != nil || string(got) != "def" {
		t.Fatalf("Get = %q, %v", got, err)
	}

	dir, _ := os.Stat(s.Dir)
	if perm := dir.Mode().Perm(); perm != 0o700 {
		t.Errorf("pasta %o, esperava 700", perm)
	}
	file, _ := os.Stat(filepath.Join(s.Dir, "postgres_password"))
	if perm := file.Mode().Perm(); perm != 0o444 {
		t.Errorf("arquivo %o, esperava 444", perm)
	}

	names, err := s.List()
	if err != nil || !slices.Equal(names, []string{"postgres_password"}) {
		t.Fatalf("List = %v, %v (temporario nao pode sobrar)", names, err)
	}

	if err := s.Delete("postgres_password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("postgres_password"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("depois do Delete, Get = %v", err)
	}
	if err := s.Delete("postgres_password"); err != nil {
		t.Fatalf("apagar o que nao existe tem que ser no-op: %v", err)
	}
}

func TestFileStoreCorrigePermissaoDaPasta(t *testing.T) {
	s := newStore(t)
	os.MkdirAll(s.Dir, 0o755)
	if err := s.Put("x", []byte("1")); err != nil {
		t.Fatal(err)
	}
	dir, _ := os.Stat(s.Dir)
	if perm := dir.Mode().Perm(); perm != 0o700 {
		t.Errorf("pasta ficou %o", perm)
	}
}

func TestFileStoreRecusaNomeInvalido(t *testing.T) {
	s := newStore(t)
	for _, name := range []string{"", "../etc/passwd", "a/b", "Senha", ".oculto", "a-b"} {
		if err := s.Put(name, []byte("x")); err == nil {
			t.Errorf("aceitou nome %q", name)
		}
	}
}

func TestEnsureNaoTrocaValorExistente(t *testing.T) {
	s := newStore(t)
	first, err := Ensure(s, "mysql_password", GenPassword)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Ensure(s, "mysql_password", func() ([]byte, error) {
		t.Fatal("gerou de novo um secret que ja existia")
		return nil, nil
	})
	if err != nil || !bytes.Equal(first, again) {
		t.Fatalf("valor mudou: %q -> %q (%v)", first, again, err)
	}
}

func TestPasswordSeguraEmURL(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		p := string(Password())
		if !regexp.MustCompile(`^[A-Z2-7]{26}$`).MatchString(p) {
			t.Fatalf("senha com formato inesperado: %q", p)
		}
		if seen[p] {
			t.Fatal("senha repetida")
		}
		seen[p] = true
	}
}
