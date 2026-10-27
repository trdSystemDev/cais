package secrets

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// Usuarios do registry. O htpasswd e a fonte da verdade de quem pode
// entrar. Os usuarios de servico tem a senha guardada (o CI e a VPS
// precisam fazer login); usuarios de pessoas so tem o hash: a senha e
// mostrada uma vez, na criacao.
//
// Limite do htpasswd: todo usuario pode push e pull em tudo. Por isso um
// usuario por pessoa/maquina, pra revogar um sem trocar os outros.
const (
	RegistryHtpasswd = "registry_htpasswd"
	RegistryCI       = "ci"  // push pelo Woodpecker
	RegistryVPS      = "vps" // pull do daemon (docker compose pull)
)

var (
	serviceUsers = []string{RegistryCI, RegistryVPS}
	registryUser = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
)

// as senhas sao aleatorias de 130 bits, entao custo alto do bcrypt nao
// protege nada e so atrasa: o registry confere o hash em todo request.
const bcryptCost = 5

// RegistryPasswordSecret e o secret que guarda a senha de um usuario de servico.
func RegistryPasswordSecret(user string) string { return "registry_" + user + "_password" }

// IsServiceUser diz se o usuario e do proprio cais (ci, vps).
func IsServiceUser(user string) bool { return slices.Contains(serviceUsers, user) }

// htpasswd e o arquivo em memoria: usuario -> hash, na ordem do arquivo.
type htpasswd struct {
	users  []string
	hashes map[string]string
}

func parseHtpasswd(b []byte) (htpasswd, error) {
	h := htpasswd{hashes: map[string]string{}}
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		user, hash, ok := strings.Cut(line, ":")
		if !ok || user == "" || hash == "" {
			return h, fmt.Errorf("%s: linha %d invalida", RegistryHtpasswd, i+1)
		}
		if _, dup := h.hashes[user]; !dup {
			h.users = append(h.users, user)
		}
		h.hashes[user] = hash
	}
	return h, nil
}

func (h htpasswd) bytes() []byte {
	var b bytes.Buffer
	for _, u := range h.users {
		fmt.Fprintf(&b, "%s:%s\n", u, h.hashes[u])
	}
	return b.Bytes()
}

func (h *htpasswd) set(user string, password []byte) error {
	hash, err := bcrypt.GenerateFromPassword(password, bcryptCost)
	if err != nil {
		return err
	}
	if _, ok := h.hashes[user]; !ok {
		h.users = append(h.users, user)
	}
	h.hashes[user] = string(hash)
	return nil
}

func loadHtpasswd(s Store) (htpasswd, error) {
	b, err := s.Get(RegistryHtpasswd)
	if errors.Is(err, ErrNotFound) {
		return htpasswd{hashes: map[string]string{}}, nil
	}
	if err != nil {
		return htpasswd{}, err
	}
	return parseHtpasswd(b)
}

// EnsureRegistry garante o htpasswd com os usuarios de servico, gerando a
// senha de quem ainda nao tem. Usuarios de pessoas nao sao tocados.
// Devolve true se o htpasswd mudou (o container do registry precisa ser
// recriado pra ler o arquivo novo).
func EnsureRegistry(s Store) (changed bool, err error) {
	h, err := loadHtpasswd(s)
	if err != nil {
		return false, err
	}
	for _, u := range serviceUsers {
		secret := RegistryPasswordSecret(u)
		pw, err := s.Get(secret)
		switch {
		case errors.Is(err, ErrNotFound):
			// senha perdida ou primeira vez: gera outra e refaz o hash
			pw = Password()
			if err := s.Put(secret, pw); err != nil {
				return false, err
			}
		case err != nil:
			return false, err
		case h.hashes[u] != "" && bcrypt.CompareHashAndPassword([]byte(h.hashes[u]), pw) == nil:
			continue
		}
		if err := h.set(u, pw); err != nil {
			return false, err
		}
		changed = true
	}
	if changed {
		return true, s.Put(RegistryHtpasswd, h.bytes())
	}
	return false, nil
}

// AddRegistryUser cria o usuario de uma pessoa e devolve a senha, que nao
// fica guardada em lugar nenhum: so o hash vai pro htpasswd.
func AddRegistryUser(s Store, user string) (password string, err error) {
	if !registryUser.MatchString(user) {
		return "", fmt.Errorf("usuario invalido %q (minusculas, numeros, - e _, ate 32)", user)
	}
	if IsServiceUser(user) {
		return "", fmt.Errorf("%q e usuario de servico do cais", user)
	}
	h, err := loadHtpasswd(s)
	if err != nil {
		return "", err
	}
	if _, ok := h.hashes[user]; ok {
		return "", fmt.Errorf("usuario %q ja existe (remova antes pra trocar a senha)", user)
	}
	pw := Password()
	if err := h.set(user, pw); err != nil {
		return "", err
	}
	if err := s.Put(RegistryHtpasswd, h.bytes()); err != nil {
		return "", err
	}
	return string(pw), nil
}

// RemoveRegistryUser tira o acesso de uma pessoa.
func RemoveRegistryUser(s Store, user string) error {
	if IsServiceUser(user) {
		return fmt.Errorf("%q e usuario de servico do cais e nao pode ser removido", user)
	}
	h, err := loadHtpasswd(s)
	if err != nil {
		return err
	}
	if _, ok := h.hashes[user]; !ok {
		return fmt.Errorf("usuario %q nao existe", user)
	}
	delete(h.hashes, user)
	h.users = slices.DeleteFunc(h.users, func(u string) bool { return u == user })
	return s.Put(RegistryHtpasswd, h.bytes())
}

// RegistryUsers lista os usuarios do registry na ordem do arquivo.
func RegistryUsers(s Store) ([]string, error) {
	h, err := loadHtpasswd(s)
	if err != nil {
		return nil, err
	}
	return h.users, nil
}
