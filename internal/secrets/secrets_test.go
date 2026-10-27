package secrets

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
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

func checkLogin(t *testing.T, s Store, user, password string) bool {
	t.Helper()
	b, err := s.Get(RegistryHtpasswd)
	if err != nil {
		t.Fatal(err)
	}
	h, err := parseHtpasswd(b)
	if err != nil {
		t.Fatal(err)
	}
	hash, ok := h.hashes[user]
	return ok && bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func TestEnsureRegistryCriaUsuariosDeServico(t *testing.T) {
	s := newStore(t)
	changed, err := EnsureRegistry(s)
	if err != nil || !changed {
		t.Fatalf("EnsureRegistry = %v, %v", changed, err)
	}
	for _, u := range []string{RegistryCI, RegistryVPS} {
		pw, err := s.Get(RegistryPasswordSecret(u))
		if err != nil {
			t.Fatal(err)
		}
		if !checkLogin(t, s, u, string(pw)) {
			t.Errorf("senha guardada de %s nao confere com o htpasswd", u)
		}
	}

	// segunda vez nao muda nada (o registry nao precisa ser recriado)
	before, _ := s.Get(RegistryHtpasswd)
	changed, err = EnsureRegistry(s)
	after, _ := s.Get(RegistryHtpasswd)
	if err != nil || changed || !bytes.Equal(before, after) {
		t.Fatalf("segunda chamada mexeu no htpasswd: changed=%v err=%v", changed, err)
	}
}

func TestEnsureRegistryRefazSenhaPerdida(t *testing.T) {
	s := newStore(t)
	EnsureRegistry(s)
	pw, _ := AddRegistryUser(s, "patrick")
	s.Delete(RegistryPasswordSecret(RegistryVPS))

	changed, err := EnsureRegistry(s)
	if err != nil || !changed {
		t.Fatalf("EnsureRegistry = %v, %v", changed, err)
	}
	newPw, _ := s.Get(RegistryPasswordSecret(RegistryVPS))
	if !checkLogin(t, s, RegistryVPS, string(newPw)) {
		t.Error("senha nova do vps nao entrou no htpasswd")
	}
	if !checkLogin(t, s, "patrick", pw) {
		t.Error("usuario de pessoa perdeu o acesso")
	}
}

func TestUsuariosDePessoas(t *testing.T) {
	s := newStore(t)
	EnsureRegistry(s)

	pw, err := AddRegistryUser(s, "patrick")
	if err != nil {
		t.Fatal(err)
	}
	if !checkLogin(t, s, "patrick", pw) {
		t.Fatal("senha devolvida nao confere")
	}
	// a senha da pessoa nao fica guardada em lugar nenhum
	names, _ := s.List()
	for _, n := range names {
		v, _ := s.Get(n)
		if strings.Contains(string(v), pw) {
			t.Fatalf("senha da pessoa gravada em %s", n)
		}
	}

	if _, err := AddRegistryUser(s, "patrick"); err == nil {
		t.Error("criou usuario repetido")
	}
	for _, bad := range []string{RegistryCI, RegistryVPS, "Patrick", "a:b", "", "-x"} {
		if _, err := AddRegistryUser(s, bad); err == nil {
			t.Errorf("aceitou usuario %q", bad)
		}
	}

	users, _ := RegistryUsers(s)
	if !slices.Equal(users, []string{RegistryCI, RegistryVPS, "patrick"}) {
		t.Fatalf("usuarios %v", users)
	}

	if err := RemoveRegistryUser(s, RegistryCI); err == nil {
		t.Error("removeu usuario de servico")
	}
	if err := RemoveRegistryUser(s, "patrick"); err != nil {
		t.Fatal(err)
	}
	if checkLogin(t, s, "patrick", pw) {
		t.Error("usuario removido ainda entra")
	}
	if err := RemoveRegistryUser(s, "patrick"); err == nil {
		t.Error("removeu usuario que nao existe")
	}
}
