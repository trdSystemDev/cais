package core

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/databases"
	"github.com/trdSystemDev/cais/internal/secrets"
)

// DatabaseRequest pede um database novo numa instancia da infra.
type DatabaseRequest struct {
	Engine string `json:"engine"` // postgres ou mysql
	// Name e o nome do database e do usuario (minusculas, numeros e _).
	Name string `json:"name"`
	// Extensions sao extensoes do Postgres criadas no database (postgis).
	Extensions []string `json:"extensions,omitempty"`
	// App, se informado, ja fica ligado ao database: recebe a URL em Env
	// (padrao DATABASE_URL) no proximo deploy.
	App string `json:"app,omitempty"`
	Env string `json:"env,omitempty"`
}

// DatabaseInfo e a credencial de um database criado pelo cais.
type DatabaseInfo struct {
	Engine   string `json:"engine"`
	Name     string `json:"name"`
	User     string `json:"user"`
	Password string `json:"password"`
	// Host e Port sao como o app chega no banco, pela rede Network.
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Network string `json:"network"`
	// URL e a URL de conexao completa (postgres://... ou mysql://...).
	URL string `json:"url"`
	// Apps sao os apps ligados ao database (variavel de cada um).
	Apps []DatabaseUse `json:"apps,omitempty"`
}

// DatabaseUse e um app que usa o database, com a variavel da URL.
type DatabaseUse struct {
	App string `json:"app"`
	Env string `json:"env"`
}

// dbURL e a URL de um database criado pelo cais (o que o app recebe).
func (c *Core) dbURL(engine, name string) (string, error) {
	e, err := databases.Get(engine)
	if err != nil {
		return "", err
	}
	pw, err := c.secretStore().Get(dbSecret(e.Name, name))
	if errors.Is(err, secrets.ErrNotFound) {
		return "", fmt.Errorf("o database %s/%s nao existe no cais (crie na aba Infra -> Novo banco)", engine, name)
	}
	if err != nil {
		return "", err
	}
	return e.URL(name, pw), nil
}

// LinkAppDatabase liga o app a um database da infra: o deploy entrega a URL
// em env (vazio = DATABASE_URL) e em ${CAIS_SECRETS}/<env em minusculas>.
func (c *Core) LinkAppDatabase(ctx context.Context, app, engine, name, env string) error {
	unlock, err := c.lockApp(app)
	if err != nil {
		return err
	}
	defer unlock()
	err = c.appManager().LinkDB(app, apps.DBLink{Engine: engine, Name: name, Env: env})
	c.CheckApps(ctx)
	return err
}

// UnlinkAppDatabase desliga o app do database; o database e os dados ficam.
func (c *Core) UnlinkAppDatabase(ctx context.Context, app, engine, name string) error {
	unlock, err := c.lockApp(app)
	if err != nil {
		return err
	}
	defer unlock()
	err = c.appManager().UnlinkDB(app, engine, name)
	c.CheckApps(ctx)
	return err
}

// a senha de cada database fica num secret central: db_<engine>_<nome>
func dbSecret(engine, name string) string { return "db_" + engine + "_" + name }

func dbInfo(e databases.Engine, name string, pw []byte) DatabaseInfo {
	return DatabaseInfo{Engine: e.Name, Name: name, User: name, Password: string(pw),
		Host: e.Host, Port: e.Port, Network: e.Network, URL: e.URL(name, pw)}
}

// CreateDatabase cria database e usuario com senha nova (o usuario so
// acessa o proprio database). Nome que ja existe e recusado: nunca troca a
// senha de um banco em uso sem pedir.
func (c *Core) CreateDatabase(ctx context.Context, req DatabaseRequest) (DatabaseInfo, error) {
	if req.App != "" {
		// o app tem que existir antes de criar o banco pra ele
		if _, err := c.appManager().Get(req.App); err != nil {
			return DatabaseInfo{}, err
		}
	}
	info, err := c.createDatabase(ctx, req)
	if err != nil || req.App == "" {
		return info, err
	}
	if err := c.LinkAppDatabase(ctx, req.App, info.Engine, info.Name, req.Env); err != nil {
		return info, fmt.Errorf("database criado, mas nao ligou no app %s: %w (ligue em Apps -> Secrets)", req.App, err)
	}
	info.Apps = []DatabaseUse{{App: req.App, Env: cmpOr(req.Env, apps.DefaultDBEnv)}}
	return info, nil
}

func (c *Core) createDatabase(ctx context.Context, req DatabaseRequest) (DatabaseInfo, error) {
	e, err := databases.Get(req.Engine)
	if err != nil {
		return DatabaseInfo{}, err
	}
	name := strings.TrimSpace(req.Name)
	if err := databases.CheckIdentifier(name); err != nil {
		return DatabaseInfo{}, err
	}
	cfg, err := config.Load(c.deps.ConfigPath)
	if err != nil {
		return DatabaseInfo{}, err
	}
	if !cfg.HasStack(req.Engine) {
		return DatabaseInfo{}, fmt.Errorf("a stack %s esta desligada: ligue na aba Infra", req.Engine)
	}
	if !c.infra.TryLock() {
		return DatabaseInfo{}, ErrBusy
	}
	defer c.infra.Unlock()

	store := c.secretStore()
	secret := dbSecret(e.Name, name)
	if ok, err := secrets.Exists(store, secret); err != nil {
		return DatabaseInfo{}, err
	} else if ok {
		return DatabaseInfo{}, fmt.Errorf("o database %s ja existe no %s (a credencial esta na lista)", name, e.Name)
	}
	pw := secrets.Password()
	if err := databases.Provision(ctx, c.deps.ExecInput, e, name, pw, req.Extensions); err != nil {
		if strings.Contains(err.Error(), "is not available") {
			err = fmt.Errorf("%w\n\no Postgres no ar nao tem a extensao: a stack usa a imagem com PostGIS; aplique o Setup (cais setup run) pra trocar", err)
		}
		return DatabaseInfo{}, err
	}
	if err := store.Put(secret, pw); err != nil {
		return DatabaseInfo{}, fmt.Errorf("database criado, mas a senha nao foi guardada: %w", err)
	}
	return dbInfo(e, name, pw), nil
}

// Databases lista os databases criados pelo cais, com a credencial.
func (c *Core) Databases(context.Context) ([]DatabaseInfo, error) {
	names, err := c.secretStore().List()
	if err != nil {
		return nil, err
	}
	var out []DatabaseInfo
	for _, n := range names {
		for _, engine := range []string{databases.Postgres, databases.MySQL} {
			name, ok := strings.CutPrefix(n, "db_"+engine+"_")
			if !ok {
				continue
			}
			pw, err := c.secretStore().Get(n)
			if errors.Is(err, secrets.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			e, _ := databases.Get(engine)
			out = append(out, dbInfo(e, name, pw))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Engine+out[i].Name < out[j].Engine+out[j].Name })
	list, err := c.appManager().List()
	if err != nil {
		return nil, err
	}
	for i := range out {
		for _, a := range list {
			for _, l := range a.Databases {
				if l.Engine == out[i].Engine && l.Name == out[i].Name {
					out[i].Apps = append(out[i].Apps, DatabaseUse{App: a.Name, Env: l.Env})
				}
			}
		}
	}
	return out, nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
