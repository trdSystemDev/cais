package stacks

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

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
	"github_app_key": "Chave privada (.pem) do GitHub App do cais: baixa o codigo dos repositorios privados no deploy",
	"brevo_api_key":  "Chave da API da Brevo (xkeysib-...): envia os e-mails de falha do deploy automatico e do backup",
}

// companions sao gerados junto com um secret dos templates, sem aparecer
// neles: a CA que assina o certificado local.
var companions = map[string][]string{
	secrets.TLSCert: {secrets.TLSCACert, secrets.TLSCAKey},
}

// generators dizem como gerar cada secret usado pelos templates. Todo nome
// que um template referencia precisa estar aqui ou em provided (testado).
var generators = map[string]func(secrets.Store) ([]byte, error){
	"postgres_password":   func(secrets.Store) ([]byte, error) { return secrets.Password(), nil },
	"mysql_root_password": func(secrets.Store) ([]byte, error) { return secrets.Password(), nil },
	"garage_rpc_secret":   func(secrets.Store) ([]byte, error) { return secrets.HexSecret(), nil },
	"garage_admin_token":  func(secrets.Store) ([]byte, error) { return secrets.Password(), nil },
}

// SecretsResult e o que EnsureSecrets fez.
type SecretsResult struct {
	// Created sao os secrets gerados agora (nunca os valores).
	Created []string `json:"created,omitempty"`
	// Missing sao os que o usuario ainda precisa informar.
	Missing []Provided `json:"missing,omitempty"`
	// Recreate sao as stacks que precisam ser recriadas pra enxergar um
	// secret que mudou: o bind mount continua preso no arquivo antigo.
	Recreate []string `json:"recreate,omitempty"`
}

// Needed lista os secrets que as stacks ativas usam, sem repetir, na ordem
// de subida. Sai dos proprios templates.
func Needed(c config.Config) ([]string, error) {
	enabled, err := Enabled(c)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, st := range enabled {
		// a pasta nao importa aqui, so os nomes
		r, err := Render(st.Name, c, "/")
		if err != nil {
			return nil, err
		}
		for _, n := range r.Secrets {
			if !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
	}
	return names, nil
}

// PlanSecrets diz, sem gravar nada, quais secrets EnsureSecrets criaria e
// quais o usuario ainda precisa informar.
func PlanSecrets(s secrets.Store, c config.Config) (create []string, missing []Provided, err error) {
	names, err := Needed(c)
	if err != nil {
		return nil, nil, err
	}
	for _, n := range names {
		exists, err := secrets.Exists(s, n)
		if err != nil {
			return nil, nil, err
		}
		if exists {
			continue
		}
		if desc, ok := provided[n]; ok {
			missing = append(missing, Provided{Name: n, Desc: desc})
			continue
		}
		create = append(create, n)
	}
	// secrets que saem junto com outro e nao aparecem nos templates
	for n, extras := range companions {
		if !slices.Contains(names, n) {
			continue
		}
		for _, e := range extras {
			ok, err := secrets.Exists(s, e)
			if err != nil {
				return nil, nil, err
			}
			if !ok && !slices.Contains(create, e) {
				create = append(create, e)
			}
		}
	}
	return create, missing, nil
}

// EnsureSecrets garante os secrets de todas as stacks ativas: gera os que
// faltam, nunca troca os que existem e lista os que o usuario precisa
// informar. Pode rodar quantas vezes quiser.
func EnsureSecrets(s secrets.Store, c config.Config) (SecretsResult, error) {
	var res SecretsResult
	names, err := Needed(c)
	if err != nil {
		return res, err
	}
	before, err := s.List()
	if err != nil {
		return res, err
	}
	for _, name := range names {
		if err := ensureOne(s, name, c, &res); err != nil {
			return res, err
		}
	}

	// inclui os derivados (a CA local junto com o certificado)
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

func ensureOne(s secrets.Store, name string, c config.Config, res *SecretsResult) error {
	switch name {
	case secrets.TLSCert, secrets.TLSKey:
		// os dois saem juntos; o segundo nome nao tem mais nada a fazer
		if slices.Contains(res.Recreate, "proxy") {
			return nil
		}
		changed, err := secrets.EnsureLocalTLS(s, TLSHosts(c), TLSDomains(c), time.Now())
		if err != nil {
			return err
		}
		if changed {
			res.Recreate = append(res.Recreate, "proxy")
		}
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

// TLSHosts sao os nomes do certificado da CA local: o dominio, o curinga
// dele e os nomes das stacks publicadas (que podem estar fora do dominio).
func TLSHosts(c config.Config) []string {
	hosts := []string{c.Domain, "*." + c.Domain}
	add := func(h string) {
		if !slices.Contains(hosts, h) {
			hosts = append(hosts, h)
		}
	}
	if c.HasStack("garage") {
		add(c.S3Host())
	}
	return hosts
}

// TLSDomains sao os dominios que a CA local pode assinar: o dominio base e
// cada host fora dele. Nada alem disso.
func TLSDomains(c config.Config) []string {
	domains := []string{c.Domain}
	for _, h := range TLSHosts(c) {
		if h == c.Domain || strings.HasSuffix(h, "."+c.Domain) || slices.Contains(domains, h) {
			continue
		}
		domains = append(domains, h)
	}
	return domains
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
