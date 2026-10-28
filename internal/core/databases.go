package core

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

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
	return out, nil
}
