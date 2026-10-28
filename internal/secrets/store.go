package secrets

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/trdSystemDev/cais/internal/fsx"
)

// ErrNotFound indica que o secret ainda nao existe.
var ErrNotFound = errors.New("secret nao encontrado")

// Store guarda secrets por nome. O valor nunca sai do daemon por estado ou
// log. A unica excecao e a credencial de um banco criado pelo cais, que a
// pessoa pede explicitamente (cais db list, aba Infra) pra por no app.
type Store interface {
	Get(name string) ([]byte, error)
	Put(name string, value []byte) error
	Delete(name string) error
	List() ([]string, error)
}

var validName = regexp.MustCompile(`^[a-z0-9_]+$`)

// CheckName recusa nomes que nao sirvam de arquivo nem de secret do compose.
func CheckName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("nome de secret invalido %q (so minusculas, numeros e _)", name)
	}
	return nil
}

// FileStore guarda cada secret num arquivo da pasta Dir, em texto puro
// (motivo em doc.go). A pasta e 0700 e os arquivos 0444: o container que
// monta o arquivo pode rodar sem ser root e precisa ler.
type FileStore struct {
	Dir string
}

func (s FileStore) path(name string) (string, error) {
	if err := CheckName(name); err != nil {
		return "", err
	}
	return filepath.Join(s.Dir, name), nil
}

func (s FileStore) Get(name string) ([]byte, error) {
	p, err := s.path(name)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	return b, err
}

// Put grava de forma atomica (temporario + rename). Um container que ja
// monta o arquivo continua vendo o valor antigo ate ser recriado.
func (s FileStore) Put(name string, value []byte) error {
	p, err := s.path(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	// MkdirAll nao corrige uma pasta que ja existia com outra permissao
	if err := os.Chmod(s.Dir, 0o700); err != nil {
		return err
	}
	return fsx.WriteAtomic(p, value, 0o444)
}

func (s FileStore) Delete(name string) error {
	p, err := s.path(name)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// List devolve os nomes dos secrets em ordem alfabetica.
func (s FileStore) List() ([]string, error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && !strings.HasPrefix(e.Name(), ".") && CheckName(e.Name()) == nil {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	return names, nil
}

// Exists diz se o secret ja foi gravado.
func Exists(s Store, name string) (bool, error) {
	_, err := s.Get(name)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// Ensure devolve o secret, gerando e gravando com gen se ainda nao existir.
// Nunca troca um valor existente: senha de banco ja esta gravada no volume.
func Ensure(s Store, name string, gen func() ([]byte, error)) ([]byte, error) {
	v, err := s.Get(name)
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if v, err = gen(); err != nil {
		return nil, fmt.Errorf("gerando %s: %w", name, err)
	}
	if err := s.Put(name, v); err != nil {
		return nil, err
	}
	return v, nil
}

// Password gera uma senha aleatoria de 26 caracteres (A-Z e 2-7, 130 bits).
// So caracteres seguros em URL, YAML e linha de comando: entra direto numa
// DATABASE_URI sem escape.
func Password() []byte {
	return []byte(rand.Text())
}

// HexSecret gera 32 bytes aleatorios em hex (64 caracteres), o formato que
// o Garage exige no rpc_secret.
func HexSecret() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand nao falha no Linux
	}
	return []byte(hex.EncodeToString(b))
}

// GenPassword e Password no formato que Ensure espera.
func GenPassword() ([]byte, error) { return Password(), nil }
