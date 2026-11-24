package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	// Source e a string de conexao de um banco pra importar (postgres://
	// ou mysql://usuario:senha@host:porta/banco); vazio cria o database
	// vazio. Tem a senha da origem: nunca vai pra log nem pro estado.
	Source string `json:"source,omitempty"`
}

// DatabaseInfo e um database criado pelo cais. A senha fica guardada no
// cais e so chega no app, pelas variaveis que o cais run injeta.
type DatabaseInfo struct {
	Engine string `json:"engine"`
	Name   string `json:"name"`
	User   string `json:"user"`
	// Host e Port sao como o app chega no banco, pela rede Network.
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Network string `json:"network"`
	// Vars sao as variaveis que o app ligado recebe
	// (<ENGINE>_<NOME>_USER, _PASS e _URL).
	Vars []string `json:"vars"`
	// Apps sao os apps ligados ao database.
	Apps []string `json:"apps,omitempty"`
	// Size e o espaco ocupado em bytes; nil se a instancia nao respondeu.
	Size *int64 `json:"size,omitempty"`
	// Imported e a origem (sem senha) quando o database veio de uma
	// importacao, e ImportedBytes o tamanho do dump copiado.
	Imported      string `json:"imported,omitempty"`
	ImportedBytes int64  `json:"imported_bytes,omitempty"`
}

// dbCredentials e o usuario e a senha de um database criado pelo cais (o
// que o app recebe).
func (c *Core) dbCredentials(engine, name string) (string, string, error) {
	e, err := databases.Get(engine)
	if err != nil {
		return "", "", err
	}
	pw, err := c.secretStore().Get(dbSecret(e.Name, name))
	if errors.Is(err, secrets.ErrNotFound) {
		return "", "", fmt.Errorf("o database %s/%s nao existe no cais (crie na aba Infra -> Novo banco)", engine, name)
	}
	if err != nil {
		return "", "", err
	}
	return name, string(pw), nil
}

// LinkAppDatabase liga o app a um database da infra: o deploy entrega as
// variaveis <ENGINE>_<NOME>_USER, _PASS e _URL.
func (c *Core) LinkAppDatabase(ctx context.Context, app, engine, name string) error {
	unlock, err := c.lockApp(app)
	if err != nil {
		return err
	}
	defer unlock()
	err = c.appManager().LinkDB(app, apps.DBLink{Engine: engine, Name: name})
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

func dbInfo(e databases.Engine, name string) DatabaseInfo {
	return DatabaseInfo{Engine: e.Name, Name: name, User: name, Host: e.Host, Port: e.Port, Network: e.Network,
		Vars: apps.DBLink{Engine: e.Name, Name: name}.Vars()}
}

// CreateDatabase cria database e usuario com senha nova (o usuario so
// acessa o proprio database). Nome que ja existe e recusado: nunca troca a
// senha de um banco em uso sem pedir. Com req.Source, copia o banco de
// origem pra dentro do database novo; se a copia falha, o database novo e
// apagado (nada fica pela metade).
func (c *Core) CreateDatabase(ctx context.Context, req DatabaseRequest) (DatabaseInfo, error) {
	e, err := databases.Get(req.Engine)
	if err != nil {
		return DatabaseInfo{}, err
	}
	name := strings.TrimSpace(req.Name)
	if err := databases.CheckIdentifier(name); err != nil {
		return DatabaseInfo{}, err
	}
	var src *databases.Source
	if strings.TrimSpace(req.Source) != "" {
		s, err := databases.ParseSource(e.Name, req.Source)
		if err != nil {
			return DatabaseInfo{}, err
		}
		src = &s
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
	if src != nil {
		// database com esse nome que o cais nao conhece (criado na mao): a
		// importacao misturaria os dois
		if sizes, err := databases.Sizes(ctx, c.deps.ExecInput, e); err != nil {
			return DatabaseInfo{}, err
		} else if _, ok := sizes[name]; ok {
			return DatabaseInfo{}, fmt.Errorf("ja existe um database %s no %s que nao foi criado pelo cais: use outro nome", name, e.Name)
		}
	}
	pw := secrets.Password()
	if err := databases.Provision(ctx, c.deps.ExecInput, e, name, pw, req.Extensions); err != nil {
		if strings.Contains(err.Error(), "is not available") {
			err = fmt.Errorf("%w\n\no Postgres no ar nao tem a extensao: a stack usa a imagem com PostGIS; aplique o postgres na aba Infra (ou cais service postgres on) pra trocar", err)
		}
		return DatabaseInfo{}, err
	}
	info := dbInfo(e, name)
	if src != nil {
		n, err := c.importDB(ctx, e, name, *src)
		if err != nil {
			// o database novo fica vazio ou pela metade: melhor nao existir
			if derr := databases.Drop(context.WithoutCancel(ctx), c.deps.ExecInput, e, name); derr != nil {
				err = fmt.Errorf("%w\n\n(e o database %s, que ficou pela metade, nao foi apagado: %v)", err, name, derr)
			}
			return DatabaseInfo{}, err
		}
		info.Imported, info.ImportedBytes = src.String(), n
	}
	if err := store.Put(secret, pw); err != nil {
		return DatabaseInfo{}, fmt.Errorf("database criado, mas a senha nao foi guardada: %w", err)
	}
	return info, nil
}

// DeleteDatabase apaga o database, o usuario e a senha guardada. Recusa se
// algum app ainda usa o database (desligue em Apps -> Bancos e buckets antes).
func (c *Core) DeleteDatabase(ctx context.Context, engine, name string) error {
	e, err := databases.Get(engine)
	if err != nil {
		return err
	}
	store := c.secretStore()
	secret := dbSecret(e.Name, name)
	if ok, err := secrets.Exists(store, secret); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("o database %s/%s nao existe no cais", engine, name)
	}
	users, err := c.appsUsing(func(a apps.App) bool {
		return slices.ContainsFunc(a.Databases, func(l apps.DBLink) bool { return l.Engine == engine && l.Name == name })
	})
	if err != nil {
		return err
	}
	if len(users) > 0 {
		return fmt.Errorf("o database %s/%s esta ligado a %s: desligue em Apps -> Bancos e buckets antes de excluir", engine, name, strings.Join(users, ", "))
	}
	if !c.infra.TryLock() {
		return ErrBusy
	}
	defer c.infra.Unlock()
	if err := databases.Drop(ctx, c.deps.ExecInput, e, name); err != nil {
		return err
	}
	return store.Delete(secret)
}

// appsUsing sao os apps em que use(app) e verdade.
func (c *Core) appsUsing(use func(apps.App) bool) ([]string, error) {
	list, err := c.appManager().List()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, a := range list {
		if use(a) {
			out = append(out, a.Name)
		}
	}
	return out, nil
}

// Databases lista os databases criados pelo cais, com os apps que usam
// cada um e o espaco ocupado.
func (c *Core) Databases(ctx context.Context) ([]DatabaseInfo, error) {
	names, err := c.secretStore().List()
	if err != nil {
		return nil, err
	}
	var out []DatabaseInfo
	for _, n := range names {
		for _, engine := range []string{databases.Postgres, databases.MySQL} {
			if name, ok := strings.CutPrefix(n, "db_"+engine+"_"); ok {
				e, _ := databases.Get(engine)
				out = append(out, dbInfo(e, name))
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Engine+out[i].Name < out[j].Engine+out[j].Name })
	list, err := c.appManager().List()
	if err != nil {
		return nil, err
	}
	sizes := map[string]map[string]int64{}
	for i := range out {
		for _, a := range list {
			for _, l := range a.Databases {
				if l.Engine == out[i].Engine && l.Name == out[i].Name {
					out[i].Apps = append(out[i].Apps, a.Name)
				}
			}
		}
		e := out[i].Engine
		if _, ok := sizes[e]; !ok {
			// instancia fora do ar: lista sem o tamanho
			eng, _ := databases.Get(e)
			sizes[e], _ = databases.Sizes(ctx, c.deps.ExecInput, eng)
		}
		if sizes[e] != nil {
			n := sizes[e][out[i].Name]
			out[i].Size = &n
		}
	}
	return out, nil
}
