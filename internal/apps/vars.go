package apps

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"

	"github.com/trdSystemDev/cais/internal/fsx"
	"github.com/trdSystemDev/cais/internal/secrets"
)

// Secret e o estado de um secret do app: so o nome, nunca o valor.
type Secret struct {
	Name string `json:"name"`
	// Infra e o banco (postgres/portal) ou bucket (s3/fotos) da infra cuja
	// credencial esta no arquivo: gravado pelo cais, desliga pelo banco ou
	// bucket, nao apagando o secret.
	Infra string `json:"infra,omitempty"`
}

// Secrets lista os secrets do app.
func (m Manager) Secrets(name string) ([]Secret, error) {
	a, err := m.Get(name)
	if err != nil {
		return nil, err
	}
	names, err := m.secrets(name).List()
	if err != nil {
		return nil, err
	}
	out := make([]Secret, 0, len(names))
	for _, n := range names {
		s := Secret{Name: n}
		s.Infra, _ = infraOfSecret(a, n)
		out = append(out, s)
	}
	return out, nil
}

// SetSecret grava um secret do app. Vale a partir do proximo deploy (ou
// Reiniciar recriando: o arquivo montado so muda num container novo).
func (m Manager) SetSecret(name, secret string, value []byte) error {
	a, err := m.Get(name)
	if err != nil {
		return err
	}
	if id, ok := infraOfSecret(a, secret); ok {
		return fmt.Errorf("%s e a credencial de %s, gravada pelo cais a cada deploy; pra trocar, desligue esse e ligue outro", secret, id)
	}
	if len(value) == 0 {
		return errors.New("valor vazio")
	}
	return m.secrets(name).Put(secret, value)
}

// DeleteSecret apaga um secret do app.
func (m Manager) DeleteSecret(name, secret string) error {
	a, err := m.Get(name)
	if err != nil {
		return err
	}
	if id, ok := infraOfSecret(a, secret); ok {
		return fmt.Errorf("%s e a credencial de %s: desligue ele do app em vez de apagar o secret", secret, id)
	}
	if err := secrets.CheckName(secret); err != nil {
		return err
	}
	return m.secrets(name).Delete(secret)
}

// Env le as variaveis do app.
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
	return ParseEnv(data)
}

// SetEnv troca as variaveis do app (valem no proximo deploy).
func (m Manager) SetEnv(name string, env map[string]string) error {
	if _, err := m.Get(name); err != nil {
		return err
	}
	data, err := FormatEnv(env)
	if err != nil {
		return err
	}
	return fsx.WriteAtomic(filepath.Join(m.dir(name), EnvFile), data, 0o600)
}

// reservedEnv sao variaveis que o cais escreve no .env de todo app.
var reservedEnv = []string{"CAIS_APP", "CAIS_COMMIT", "CAIS_SECRETS"}

// dotEnvPath e o .env do projeto: o compose le o da pasta do arquivo de
// compose (que pode nao ser a raiz do repositorio).
func (m Manager) dotEnvPath(a App, src string) string {
	return filepath.Join(m.dir(a.Name), src, filepath.FromSlash(path.Dir(a.ComposeFile())), ".env")
}

// writeDotEnv grava o .env do projeto: as variaveis do painel mais as do
// cais (CAIS_SECRETS e a pasta dos secrets, pro compose referenciar).
func (m Manager) writeDotEnv(a App, src, commit string) error {
	env, err := m.Env(a.Name)
	if err != nil {
		return err
	}
	env["CAIS_APP"] = a.Name
	env["CAIS_COMMIT"] = commit
	env["CAIS_SECRETS"] = m.SecretsPath(a.Name)
	data, err := FormatEnv(env)
	if err != nil {
		return err
	}
	return fsx.WriteAtomic(m.dotEnvPath(a, src), data, 0o600)
}

// IsReservedEnv diz se a variavel e escrita pelo cais.
func IsReservedEnv(k string) bool { return slices.Contains(reservedEnv, k) }
