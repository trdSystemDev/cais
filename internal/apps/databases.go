package apps

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/trdSystemDev/cais/internal/secrets"
)

// DBLink liga o app a um database da infra (Postgres/MySQL compartilhado).
// No deploy o cais le a credencial guardada e entrega a URL em Env (no
// .env) e no arquivo ${CAIS_SECRETS}/<env em minusculas>.
type DBLink struct {
	Engine string `yaml:"engine" json:"engine"`
	Name   string `yaml:"name" json:"name"`
	Env    string `yaml:"env" json:"env"`
}

// DefaultDBEnv e a variavel padrao da URL do banco.
const DefaultDBEnv = "DATABASE_URL"

// File e o secret do app com a URL (o mesmo valor da variavel).
func (l DBLink) File() string { return strings.ToLower(l.Env) }

// ID e engine/nome, como o cais mostra o database.
func (l DBLink) ID() string { return l.Engine + "/" + l.Name }

// DBCredentials devolve a URL de conexao de um database da infra.
type DBCredentials func(engine, name string) (string, error)

// LinkDB liga o app a um database. A variavel nao pode ser do cais, de
// outro banco do app nem uma variavel comum do app (o valor viria de dois
// lugares); o arquivo nao pode ser um secret que o app ja tem.
func (m Manager) LinkDB(name string, l DBLink) error {
	a, err := m.Get(name)
	if err != nil {
		return err
	}
	if l.Env == "" {
		l.Env = DefaultDBEnv
	}
	if !envName.MatchString(l.Env) || IsReservedEnv(l.Env) {
		return fmt.Errorf("variavel invalida %q pra URL do banco (ex: DATABASE_URL)", l.Env)
	}
	if m.DBCredentials == nil {
		return errors.New("bancos da infra indisponiveis")
	}
	if _, err := m.DBCredentials(l.Engine, l.Name); err != nil {
		return err
	}
	for _, x := range a.Databases {
		if x.Env == l.Env && x.ID() != l.ID() {
			return fmt.Errorf("%s ja e a URL do banco %s neste app; escolha outra variavel", l.Env, x.ID())
		}
	}
	env, err := m.Env(name)
	if err != nil {
		return err
	}
	if _, ok := env[l.Env]; ok {
		return fmt.Errorf("%s ja e uma variavel do app: apague ela em Variaveis (o cais passa a preencher com a URL do banco)", l.Env)
	}
	if ok, err := secrets.Exists(m.secrets(name), l.File()); err != nil {
		return err
	} else if ok && !slices.ContainsFunc(a.Databases, func(x DBLink) bool { return x.File() == l.File() }) {
		return fmt.Errorf("o app ja tem o secret %s; apague ele ou escolha outra variavel", l.File())
	}
	a.Databases = slices.DeleteFunc(a.Databases, func(x DBLink) bool { return x.ID() == l.ID() })
	a.Databases = append(a.Databases, l)
	if err := m.save(a); err != nil {
		return err
	}
	// o arquivo ja fica pronto (e aparece em Secrets); o deploy regrava
	url, err := m.DBCredentials(l.Engine, l.Name)
	if err != nil {
		return err
	}
	return m.secrets(name).Put(l.File(), []byte(url))
}

func linkByFile(a App, file string) (DBLink, bool) {
	for _, l := range a.Databases {
		if l.File() == file {
			return l, true
		}
	}
	return DBLink{}, false
}

// UnlinkDB desliga o app do database (o database e os dados ficam) e apaga
// o arquivo com a URL; vale no proximo deploy.
func (m Manager) UnlinkDB(name, engine, db string) error {
	a, err := m.Get(name)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(a.Databases, func(x DBLink) bool { return x.Engine == engine && x.Name == db })
	if i < 0 {
		return fmt.Errorf("o app %s nao usa o banco %s/%s", name, engine, db)
	}
	file := a.Databases[i].File()
	a.Databases = slices.Delete(a.Databases, i, i+1)
	if err := m.save(a); err != nil {
		return err
	}
	return m.secrets(name).Delete(file)
}

// dbEnv preenche as variaveis e os arquivos dos bancos ligados.
func (m Manager) dbEnv(a App, env map[string]string) error {
	for _, l := range a.Databases {
		if _, ok := env[l.Env]; ok {
			return fmt.Errorf("%s e ao mesmo tempo variavel do app e a URL do banco %s; apague a variavel", l.Env, l.ID())
		}
		if m.DBCredentials == nil {
			return errors.New("bancos da infra indisponiveis")
		}
		url, err := m.DBCredentials(l.Engine, l.Name)
		if err != nil {
			return fmt.Errorf("banco %s do app: %w", l.ID(), err)
		}
		env[l.Env] = url
		if err := m.secrets(a.Name).Put(l.File(), []byte(url)); err != nil {
			return err
		}
	}
	return nil
}
