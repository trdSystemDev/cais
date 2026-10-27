package stacks

import (
	"errors"
	"fmt"
	"slices"

	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/secrets"
)

// Provided e um secret que o cais nao consegue gerar: vem de fora e o
// usuario informa no Setup.
type Provided struct {
	Name string `json:"name"`
	Desc string `json:"desc"`
}

// provided sao os secrets que vem de fora, com o que mostrar no Setup.
var provided = map[string]string{
	"github_client_secret": "Client secret do OAuth app do GitHub (o mesmo app do woodpecker.github_client)",
}

// generators dizem como gerar cada secret usado pelos templates. Todo nome
// que um template referencia precisa estar aqui ou em provided (testado).
var generators = map[string]func(secrets.Store) ([]byte, error){
	"db_bunkerweb_password": func(secrets.Store) ([]byte, error) { return secrets.Password(), nil },
	// formato do BunkerWeb; a senha e A-Z2-7, entao entra na URI sem escape
	"database_uri": func(s secrets.Store) ([]byte, error) {
		pw, err := secrets.Ensure(s, "db_bunkerweb_password", secrets.GenPassword)
		if err != nil {
			return nil, err
		}
		return []byte("mariadb+pymysql://bunkerweb:" + string(pw) + "@bw-db:3306/bunkerweb"), nil
	},
	"postgres_password":   func(secrets.Store) ([]byte, error) { return secrets.Password(), nil },
	"mysql_root_password": func(secrets.Store) ([]byte, error) { return secrets.Password(), nil },
	"mysql_password":      func(secrets.Store) ([]byte, error) { return secrets.Password(), nil },
	"agent_secret":        func(secrets.Store) ([]byte, error) { return secrets.Password(), nil },
	"dbgate_login":        func(secrets.Store) ([]byte, error) { return []byte("admin"), nil },
	"dbgate_password":     func(secrets.Store) ([]byte, error) { return secrets.Password(), nil },
}

// SecretsResult e o que EnsureSecrets fez.
type SecretsResult struct {
	// Created sao os secrets gerados agora (nunca os valores).
	Created []string `json:"created,omitempty"`
	// Missing sao os que o usuario ainda precisa informar.
	Missing []Provided `json:"missing,omitempty"`
	// RegistryChanged indica que o htpasswd mudou e o registry precisa
	// ser recriado pra enxergar o arquivo novo.
	RegistryChanged bool `json:"registry_changed,omitempty"`
}

// EnsureSecrets garante os secrets de todas as stacks ativas: gera os que
// faltam, nunca troca os que existem e lista os que o usuario precisa
// informar. Pode rodar quantas vezes quiser.
func EnsureSecrets(s secrets.Store, c config.Config) (SecretsResult, error) {
	var res SecretsResult
	enabled, err := Enabled(c)
	if err != nil {
		return res, err
	}
	before, err := s.List()
	if err != nil {
		return res, err
	}

	seen := map[string]bool{}
	for _, st := range enabled {
		// a lista sai do proprio template; a pasta nao importa aqui
		r, err := Render(st.Name, c, "/")
		if err != nil {
			return res, err
		}
		for _, name := range r.Secrets {
			if seen[name] {
				continue
			}
			seen[name] = true
			if err := ensureOne(s, name, &res); err != nil {
				return res, err
			}
		}
	}

	// inclui os derivados (a senha base da database_uri) e as senhas dos
	// usuarios de servico do registry
	after, err := s.List()
	if err != nil {
		return res, err
	}
	for _, n := range after {
		if !slices.Contains(before, n) {
			res.Created = append(res.Created, n)
		}
	}
	return res, nil
}

func ensureOne(s secrets.Store, name string, res *SecretsResult) error {
	if name == secrets.RegistryHtpasswd {
		changed, err := secrets.EnsureRegistry(s)
		if err != nil {
			return err
		}
		res.RegistryChanged = changed
		return nil
	}

	if desc, ok := provided[name]; ok {
		exists, err := secrets.Exists(s, name)
		if err != nil {
			return err
		}
		if !exists {
			res.Missing = append(res.Missing, Provided{Name: name, Desc: desc})
		}
		return nil
	}

	gen, ok := generators[name]
	if !ok {
		return fmt.Errorf("secret %q sem gerador: adicione em stacks/secrets.go", name)
	}
	_, err := secrets.Ensure(s, name, func() ([]byte, error) { return gen(s) })
	return err
}

// SetProvided grava um secret informado pelo usuario.
func SetProvided(s secrets.Store, name string, value []byte) error {
	if _, ok := provided[name]; !ok {
		return fmt.Errorf("%q nao e um secret informado pelo usuario", name)
	}
	if len(value) == 0 {
		return errors.New("valor vazio")
	}
	return s.Put(name, value)
}
