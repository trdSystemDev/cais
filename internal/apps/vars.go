package apps

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trdSystemDev/cais/internal/fsx"
)

// RunEnvFile e o arquivo com as variaveis que o cais run injeta no processo
// do app: fica em Root/<app>, fora do codigo (um COPY . . nao o leva pra
// imagem), e o compose monta em RunEnvTarget.
const (
	RunEnvFile   = "run.env"
	RunEnvTarget = "/run/cais/env"
)

// Env le as variaveis do app (JSON, aceita qualquer valor: varias linhas,
// aspas, $).
func (m Manager) Env(name string) (map[string]string, error) {
	if _, err := m.Get(name); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(m.dir(name), EnvFile))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("variaveis do app %s ilegiveis: %w", name, err)
	}
	return env, nil
}

// SetEnv troca as variaveis do app (valem no proximo deploy). Recusa os
// nomes do cais e os que um banco ou bucket ligado entrega.
func (m Manager) SetEnv(name string, env map[string]string) error {
	a, err := m.Get(name)
	if err != nil {
		return err
	}
	for _, k := range slices.Sorted(maps.Keys(env)) {
		if err := CheckEnv(k, env[k]); err != nil {
			return err
		}
		if id, ok := infraOfVar(a, k); ok {
			return fmt.Errorf("%s e entregue pelo %s ligado ao app; pra trocar, desligue ele", k, id)
		}
	}
	data, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return err
	}
	return fsx.WriteAtomic(filepath.Join(m.dir(name), EnvFile), append(data, '\n'), 0o600)
}

// CheckEnv recusa nome que nao serve de variavel, os nomes do cais e valor
// que o ambiente de um processo nao aceita (byte zero).
func CheckEnv(k, v string) error {
	switch {
	case !envName.MatchString(k):
		return fmt.Errorf("nome de variavel invalido %q (letras, numeros e _, sem comecar por numero)", k)
	case IsReservedEnv(k):
		return fmt.Errorf("%s e do cais", k)
	case strings.ContainsRune(v, 0):
		return fmt.Errorf("%s: valor com byte zero", k)
	}
	return nil
}

// reservedEnv sao variaveis que o cais escreve no .env de todo app.
var reservedEnv = []string{"CAIS_APP", "CAIS_COMMIT", "CAIS_BIN", "CAIS_ENV"}

// dotEnvPath e o .env do projeto: o compose le o da pasta do arquivo de
// compose (que pode nao ser a raiz do repositorio).
func (m Manager) dotEnvPath(a App, src string) string {
	return filepath.Join(m.dir(a.Name), src, filepath.FromSlash(path.Dir(a.ComposeFile())), ".env")
}

// RunEnvPath e o arquivo que o compose do app monta em RunEnvTarget.
func (m Manager) RunEnvPath(name string) string { return filepath.Join(m.dir(name), RunEnvFile) }

// writeEnv grava as variaveis do deploy: o run.env com todas (as do app e
// as dos bancos e buckets ligados), que o cais run injeta no processo, e o
// .env do projeto so pra interpolacao no compose: as do cais (CAIS_BIN e
// CAIS_ENV pros volumes do cais run) e as que o compose cita pelo nome
// (DOMINIO nos labels, o build secret). O .env fica no contexto do build:
// quanto menos nele, menos um COPY . . leva pra imagem.
func (m Manager) writeEnv(a App, src, commit string) error {
	env, err := m.Env(a.Name)
	if err != nil {
		return err
	}
	infra, err := m.infraEnv(a)
	if err != nil {
		return err
	}
	maps.Copy(env, infra)
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	// 0444: o container pode rodar sem ser root e precisa ler; a pasta do
	// app e 0700 e o /var/lib/cais e do root
	if err := fsx.WriteAtomic(m.RunEnvPath(a.Name), data, 0o444); err != nil {
		return err
	}

	compose, err := os.ReadFile(filepath.Join(m.dir(a.Name), src, filepath.FromSlash(a.ComposeFile())))
	if err != nil {
		return err
	}
	dot := map[string]string{}
	for k, v := range env {
		if mentions(compose, k) {
			dot[k] = v
		}
	}
	dot["CAIS_APP"] = a.Name
	dot["CAIS_COMMIT"] = commit
	dot["CAIS_BIN"] = m.Bin
	dot["CAIS_ENV"] = m.RunEnvPath(a.Name)
	out, err := FormatEnv(dot)
	if err != nil {
		return fmt.Errorf("variavel citada no compose: %w", err)
	}
	return fsx.WriteAtomic(m.dotEnvPath(a, src), out, 0o600)
}

// mentions diz se o compose cita a variavel pelo nome (palavra inteira:
// ${DOMINIO}, $DOMINIO, environment: NPM_TOKEN).
func mentions(compose []byte, name string) bool {
	for i := 0; ; {
		j := bytes.Index(compose[i:], []byte(name))
		if j < 0 {
			return false
		}
		j += i
		end := j + len(name)
		if (j == 0 || !isNameByte(compose[j-1])) && (end == len(compose) || !isNameByte(compose[end])) {
			return true
		}
		i = j + 1
	}
}

func isNameByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// IsReservedEnv diz se a variavel e escrita pelo cais.
func IsReservedEnv(k string) bool { return slices.Contains(reservedEnv, k) }
