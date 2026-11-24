package apps

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
)

// DBLink liga o app a um database da infra (Postgres/MySQL compartilhado).
// No deploy o cais le a credencial guardada e entrega ao app as variaveis
// <ENGINE>_<NOME>_USER, _PASS e _URL (o database tem o nome do usuario).
type DBLink struct {
	Engine string `yaml:"engine" json:"engine"`
	Name   string `yaml:"name" json:"name"`
}

// ID e engine/nome, como o cais mostra o database.
func (l DBLink) ID() string { return l.Engine + "/" + l.Name }

func (l DBLink) prefix() string { return envPrefix(l.Engine + "_" + l.Name) }

// Vars sao os nomes das variaveis que o cais entrega pro banco ligado (ex:
// POSTGRES_PORTAL_USER, POSTGRES_PORTAL_PASS, POSTGRES_PORTAL_URL).
func (l DBLink) Vars() []string {
	p := l.prefix()
	return []string{p + "_USER", p + "_PASS", p + "_URL"}
}

// dbHosts e onde cada engine responde na rede dela.
var dbHosts = map[string]string{"postgres": "postgres:5432", "mysql": "mysql:3306"}

// env sao as variaveis do banco ligado com a credencial: usuario, senha e a
// URL pronta (usuario e senha escapados).
func (l DBLink) env(user, pass string) map[string]string {
	u := url.URL{Scheme: l.Engine, User: url.UserPassword(user, pass), Host: dbHosts[l.Engine], Path: "/" + user}
	v := l.Vars()
	return map[string]string{v[0]: user, v[1]: pass, v[2]: u.String()}
}

// envPrefix e o comeco do nome de uma variavel da infra: maiusculas, com _
// no lugar de - (postgres_meu-banco -> POSTGRES_MEU_BANCO).
func envPrefix(s string) string { return strings.ToUpper(strings.ReplaceAll(s, "-", "_")) }

// DBCredentials devolve usuario e senha de um database da infra.
type DBCredentials func(engine, name string) (user, password string, err error)

// LinkDB liga o app a um database; vale no proximo deploy. O app nao pode
// ter variavel com os nomes das do banco.
func (m Manager) LinkDB(name string, l DBLink) error {
	a, err := m.Get(name)
	if err != nil {
		return err
	}
	if m.DBCredentials == nil {
		return errors.New("bancos da infra indisponiveis")
	}
	if _, _, err := m.DBCredentials(l.Engine, l.Name); err != nil {
		return err
	}
	if slices.ContainsFunc(a.Databases, func(x DBLink) bool { return x.ID() == l.ID() }) {
		return nil
	}
	if err := m.varsFree(name, l.Vars()); err != nil {
		return err
	}
	a.Databases = append(a.Databases, l)
	return m.save(a)
}

// UnlinkDB desliga o app do database (o database e os dados ficam); as
// variaveis dele saem no proximo deploy.
func (m Manager) UnlinkDB(name, engine, db string) error {
	a, err := m.Get(name)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(a.Databases, func(x DBLink) bool { return x.Engine == engine && x.Name == db })
	if i < 0 {
		return fmt.Errorf("o app %s nao usa o banco %s/%s", name, engine, db)
	}
	a.Databases = slices.Delete(a.Databases, i, i+1)
	return m.save(a)
}

// varsFree recusa ligar a infra se o app ja tem variavel com algum dos
// nomes que ela entrega (o cais sobrescreveria).
func (m Manager) varsFree(app string, names []string) error {
	env, err := m.Env(app)
	if err != nil {
		return err
	}
	for _, n := range names {
		if _, ok := env[n]; ok {
			return fmt.Errorf("o app ja tem a variavel %s; apague ela (o cais passa a entregar com a credencial da infra)", n)
		}
	}
	return nil
}

// InfraVars sao os nomes das variaveis que os bancos e buckets ligados
// entregam ao app (o valor so existe no deploy).
func InfraVars(a App) []string {
	var out []string
	for _, l := range a.Databases {
		out = append(out, l.Vars()...)
	}
	for _, l := range a.Buckets {
		out = append(out, l.Vars()...)
	}
	return out
}

// infraOfVar e o banco ou bucket que entrega a variavel.
func infraOfVar(a App, name string) (string, bool) {
	for _, l := range a.Databases {
		if slices.Contains(l.Vars(), name) {
			return l.ID(), true
		}
	}
	for _, l := range a.Buckets {
		if slices.Contains(l.Vars(), name) {
			return l.ID(), true
		}
	}
	return "", false
}

// infraEnv le as credenciais dos bancos e buckets ligados (credencial nova
// no cais vale no proximo deploy) e devolve as variaveis deles.
func (m Manager) infraEnv(a App) (map[string]string, error) {
	env := map[string]string{}
	for _, l := range a.Databases {
		if m.DBCredentials == nil {
			return nil, errors.New("bancos da infra indisponiveis")
		}
		user, pw, err := m.DBCredentials(l.Engine, l.Name)
		if err != nil {
			return nil, fmt.Errorf("banco %s do app: %w", l.ID(), err)
		}
		maps.Copy(env, l.env(user, pw))
	}
	for _, l := range a.Buckets {
		if m.BucketCredentials == nil {
			return nil, errors.New("buckets do Garage indisponiveis")
		}
		id, secret, err := m.BucketCredentials(l.Name)
		if err != nil {
			return nil, fmt.Errorf("bucket %s do app: %w", l.Name, err)
		}
		maps.Copy(env, l.env(id, secret))
	}
	return env, nil
}
