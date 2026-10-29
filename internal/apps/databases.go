package apps

import (
	"errors"
	"fmt"
	"slices"

	"github.com/trdSystemDev/cais/internal/secrets"
)

// DBLink liga o app a um database da infra (Postgres/MySQL compartilhado).
// No deploy o cais le a credencial guardada e grava os secrets
// ${CAIS_SECRETS}/<engine>_<nome>_user e <engine>_<nome>_pass; montar a
// URL e com o app.
type DBLink struct {
	Engine string `yaml:"engine" json:"engine"`
	Name   string `yaml:"name" json:"name"`
}

// ID e engine/nome, como o cais mostra o database.
func (l DBLink) ID() string { return l.Engine + "/" + l.Name }

// UserSecret e o secret com o usuario do banco (ex: postgres_portal_user).
func (l DBLink) UserSecret() string { return l.Engine + "_" + l.Name + "_user" }

// PassSecret e o secret com a senha do banco (ex: postgres_portal_pass).
func (l DBLink) PassSecret() string { return l.Engine + "_" + l.Name + "_pass" }

// Secrets sao os secrets que o cais grava pro banco ligado.
func (l DBLink) Secrets() []string { return []string{l.UserSecret(), l.PassSecret()} }

// DBCredentials devolve usuario e senha de um database da infra.
type DBCredentials func(engine, name string) (user, password string, err error)

// LinkDB liga o app a um database. O app nao pode ter os secrets do banco
// gravados a mao.
func (m Manager) LinkDB(name string, l DBLink) error {
	a, err := m.Get(name)
	if err != nil {
		return err
	}
	if m.DBCredentials == nil {
		return errors.New("bancos da infra indisponiveis")
	}
	user, pw, err := m.DBCredentials(l.Engine, l.Name)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(a.Databases, func(x DBLink) bool { return x.ID() == l.ID() }) {
		return nil
	}
	if err := m.secretsFree(name, l.Secrets()); err != nil {
		return err
	}
	a.Databases = append(a.Databases, l)
	if err := m.save(a); err != nil {
		return err
	}
	// os arquivos ja ficam prontos (e aparecem em Secrets); o deploy regrava
	return m.putSecrets(name, l.Secrets(), user, pw)
}

// UnlinkDB desliga o app do database (o database e os dados ficam) e apaga
// os secrets dele; vale no proximo deploy.
func (m Manager) UnlinkDB(name, engine, db string) error {
	a, err := m.Get(name)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(a.Databases, func(x DBLink) bool { return x.Engine == engine && x.Name == db })
	if i < 0 {
		return fmt.Errorf("o app %s nao usa o banco %s/%s", name, engine, db)
	}
	l := a.Databases[i]
	a.Databases = slices.Delete(a.Databases, i, i+1)
	if err := m.save(a); err != nil {
		return err
	}
	return m.deleteSecrets(name, l.Secrets())
}

// secretsFree recusa ligar a infra se o app ja tem algum dos secrets
// gravado a mao (o cais sobrescreveria).
func (m Manager) secretsFree(app string, names []string) error {
	for _, s := range names {
		if ok, err := secrets.Exists(m.secrets(app), s); err != nil {
			return err
		} else if ok {
			return fmt.Errorf("o app ja tem o secret %s gravado a mao; apague ele (o cais passa a gravar com a credencial da infra)", s)
		}
	}
	return nil
}

// putSecrets grava os valores nos secrets, na mesma ordem.
func (m Manager) putSecrets(app string, names []string, values ...string) error {
	for i, s := range names {
		if err := m.secrets(app).Put(s, []byte(values[i])); err != nil {
			return err
		}
	}
	return nil
}

func (m Manager) deleteSecrets(app string, names []string) error {
	for _, s := range names {
		if err := m.secrets(app).Delete(s); err != nil {
			return err
		}
	}
	return nil
}

// infraOfSecret e o banco ou bucket cuja credencial esta no secret
// (gerenciado pelo cais).
func infraOfSecret(a App, secret string) (string, bool) {
	for _, l := range a.Databases {
		if slices.Contains(l.Secrets(), secret) {
			return l.ID(), true
		}
	}
	for _, l := range a.Buckets {
		if slices.Contains(l.Secrets(), secret) {
			return l.ID(), true
		}
	}
	return "", false
}

// infraSecrets regrava os secrets dos bancos e buckets ligados (credencial
// nova no cais vale no proximo deploy).
func (m Manager) infraSecrets(a App) error {
	for _, l := range a.Databases {
		if m.DBCredentials == nil {
			return errors.New("bancos da infra indisponiveis")
		}
		user, pw, err := m.DBCredentials(l.Engine, l.Name)
		if err != nil {
			return fmt.Errorf("banco %s do app: %w", l.ID(), err)
		}
		if err := m.putSecrets(a.Name, l.Secrets(), user, pw); err != nil {
			return err
		}
	}
	for _, l := range a.Buckets {
		if m.BucketCredentials == nil {
			return errors.New("buckets do Garage indisponiveis")
		}
		id, secret, err := m.BucketCredentials(l.Name)
		if err != nil {
			return fmt.Errorf("bucket %s do app: %w", l.Name, err)
		}
		if err := m.putSecrets(a.Name, l.Secrets(), id, secret); err != nil {
			return err
		}
	}
	return nil
}
