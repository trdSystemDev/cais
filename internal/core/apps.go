package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/databases"
	"github.com/trdSystemDev/cais/internal/detect"
	"github.com/trdSystemDev/cais/internal/docker"
)

func (c *Core) appManager() apps.Manager {
	return apps.Manager{Root: c.deps.AppsDir, Docker: c.deps.AppDocker, Now: c.deps.Now}
}

// lockApp pega a trava do app sem esperar: um segundo deploy (ou restart)
// do mesmo app falha na hora, com mensagem, em vez de enfileirar.
func (c *Core) lockApp(name string) (func(), error) {
	v, _ := c.appLocks.LoadOrStore(name, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	if !mu.TryLock() {
		return nil, fmt.Errorf("ja tem uma acao em andamento no app %s; tente quando ela terminar", name)
	}
	return mu.Unlock, nil
}

// reservedHosts sao os dominios das stacks, que nenhum app pode usar.
func (c *Core) reservedHosts() []string {
	cfg, err := config.Load(c.deps.ConfigPath)
	if err != nil {
		return nil
	}
	return cfg.ReservedHosts()
}

// CheckApps le os apps do disco, pergunta ao docker pelos containers de
// cada um, publica em State.Apps e devolve. Problema num app fica no
// resultado dele.
func (c *Core) CheckApps(ctx context.Context) (Apps, error) {
	c.update(func(s *State) {
		c.appChecks++
		s.Apps.Checking = true
	})

	result := Apps{}
	m := c.appManager()
	list, err := m.List()
	if err != nil {
		result.Error = err.Error()
	}
	for _, a := range list {
		st := AppStatus{App: a}
		if st.Containers, err = m.Status(ctx, a.Name); err != nil {
			st.Error = err.Error()
		}
		if st.Secrets, err = m.Secrets(a.Name); err != nil && st.Error == "" {
			st.Error = err.Error()
		}
		result.List = append(result.List, st)
	}

	s := c.update(func(s *State) {
		c.appChecks--
		result.Checking = c.appChecks > 0
		result.CheckedAt = c.deps.Now()
		// um deploy em andamento continua marcado
		for i := range result.List {
			if prev := findApp(s.Apps.List, result.List[i].Name); prev != nil {
				result.List[i].Deploying = prev.Deploying
			}
		}
		s.Apps = result
	})
	return s.Apps, nil
}

func findApp(list []AppStatus, name string) *AppStatus {
	i := slices.IndexFunc(list, func(a AppStatus) bool { return a.Name == name })
	if i < 0 {
		return nil
	}
	return &list[i]
}

// setDeploying marca o deploy no estado (lista nova: a anterior pode estar
// publicada).
func (c *Core) setDeploying(name string, on bool) {
	c.update(func(s *State) {
		list := slices.Clone(s.Apps.List)
		if a := findApp(list, name); a != nil {
			a.Deploying = on
		} else if on {
			list = append(list, AppStatus{App: apps.App{Name: name}, Deploying: true})
		}
		s.Apps.List = list
	})
}

func (c *Core) CreateApp(ctx context.Context, name string) (apps.App, error) {
	a, err := c.appManager().Create(name)
	if err != nil {
		return a, err
	}
	c.CheckApps(ctx)
	return a, nil
}

func (c *Core) SetAppRoutes(ctx context.Context, name string, routes []apps.Route) error {
	// rotas conferem dominios entre todos os apps: uma troca por vez
	c.routes.Lock()
	defer c.routes.Unlock()
	unlock, err := c.lockApp(name)
	if err != nil {
		return err
	}
	defer unlock()

	err = c.appManager().SetRoutes(ctx, name, routes, c.reservedHosts())
	c.CheckApps(ctx)
	return err
}

// PlanApp analisa os arquivos do repositorio do app e devolve a proposta
// de configuracao (spec, rota, banco). Nao grava nada.
func (c *Core) PlanApp(_ context.Context, name string, files detect.Files) (apps.Plan, error) {
	if err := apps.CheckName(name); err != nil {
		return apps.Plan{}, err
	}
	for path, data := range files {
		if !slices.Contains(detect.Wanted, path) {
			return apps.Plan{}, fmt.Errorf("arquivo %q nao faz parte da analise", path)
		}
		if len(data) > 1<<20 {
			return apps.Plan{}, fmt.Errorf("arquivo %s grande demais pra analise", path)
		}
	}
	cfg, err := config.Load(c.deps.ConfigPath)
	if err != nil {
		return apps.Plan{}, err
	}
	return apps.PlanFromAnalysis(name, cfg.RegistryHost(), cfg.Domain, detect.Analyze(files)), nil
}

// ApplyApp aplica um plano (ja revisado pelo usuario): cria o app se
// precisar, grava a spec e as rotas e cria o banco. O deploy vem depois,
// pela tag do primeiro build.
func (c *Core) ApplyApp(ctx context.Context, p apps.Plan) error {
	if err := apps.CheckName(p.App); err != nil {
		return err
	}
	if err := apps.CheckSpec(p.Spec); err != nil {
		return err
	}
	if p.Database != nil {
		if err := apps.CheckDatabase(*p.Database); err != nil {
			return err
		}
	}
	c.routes.Lock()
	defer c.routes.Unlock()
	unlock, err := c.lockApp(p.App)
	if err != nil {
		return err
	}
	defer unlock()
	defer c.CheckApps(ctx)

	m := c.appManager()
	if _, err := m.Get(p.App); errors.Is(err, apps.ErrNotFound) {
		if _, err := m.Create(p.App); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if err := m.SetSpec(p.App, p.Spec); err != nil {
		return err
	}
	if err := m.SetRoutes(ctx, p.App, p.Routes, c.reservedHosts()); err != nil {
		return err
	}
	if p.Database != nil {
		return c.setDatabase(ctx, p.App, p.Database.Engine, p.Database.Services, p.Database.Env)
	}
	return nil
}

func (c *Core) SetAppSpec(ctx context.Context, name string, spec apps.Spec) error {
	unlock, err := c.lockApp(name)
	if err != nil {
		return err
	}
	defer unlock()
	err = c.appManager().SetSpec(name, spec)
	c.CheckApps(ctx)
	return err
}

// DeployAppTag gera o compose da spec com a tag e faz o deploy. E o que o
// CI chama: ele escolhe so a tag, nunca a configuracao do app.
func (c *Core) DeployAppTag(ctx context.Context, name, tag string) (apps.DeployResult, error) {
	if err := apps.CheckName(name); err != nil {
		return apps.DeployResult{}, err
	}
	unlock, err := c.lockApp(name)
	if err != nil {
		return apps.DeployResult{}, err
	}
	defer unlock()

	c.setDeploying(name, true)
	res, err := c.appManager().DeployTag(ctx, name, tag)
	c.setDeploying(name, false)
	c.CheckApps(ctx)
	return res, err
}

func (c *Core) AppSecrets(_ context.Context, name string) ([]apps.Secret, error) {
	return c.appManager().Secrets(name)
}

func (c *Core) SetAppSecret(ctx context.Context, name, secret, value string) error {
	unlock, err := c.lockApp(name)
	if err != nil {
		return err
	}
	defer unlock()
	err = c.appManager().SetSecret(ctx, name, secret, []byte(value))
	c.CheckApps(ctx)
	return err
}

// DeployApp valida e sobe o compose novo do app. O progresso (Deploying)
// aparece no estado; o resultado tambem e devolvido. Roda com o contexto
// do daemon: se o CI desconectar, o deploy termina mesmo assim.
func (c *Core) DeployApp(ctx context.Context, name, composeYAML string) (apps.DeployResult, error) {
	if err := apps.CheckName(name); err != nil {
		return apps.DeployResult{}, err
	}
	unlock, err := c.lockApp(name)
	if err != nil {
		return apps.DeployResult{}, err
	}
	defer unlock()

	c.setDeploying(name, true)
	res, err := c.appManager().Deploy(ctx, name, []byte(composeYAML))
	c.setDeploying(name, false)
	c.CheckApps(ctx)
	return res, err
}

func (c *Core) RestartApp(ctx context.Context, name, service string) error {
	unlock, err := c.lockApp(name)
	if err != nil {
		return err
	}
	defer unlock()
	err = c.appManager().Restart(ctx, name, service)
	c.CheckApps(ctx)
	return err
}

func (c *Core) StopApp(ctx context.Context, name string) error {
	unlock, err := c.lockApp(name)
	if err != nil {
		return err
	}
	defer unlock()
	err = c.appManager().Stop(ctx, name)
	c.CheckApps(ctx)
	return err
}

// AppLogs so aceita containers do proprio app (nunca de uma stack ou de
// outro app).
func (c *Core) AppLogs(ctx context.Context, name, container string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	containers, err := c.appManager().Status(ctx, name)
	if err != nil {
		return "", err
	}
	if !slices.ContainsFunc(containers, func(ct docker.Container) bool { return ct.Name == container }) {
		return "", fmt.Errorf("container %q nao pertence ao app %s", container, name)
	}
	return c.deps.ContainerLogs(ctx, container)
}

// SetAppDatabase cria (ou acerta) o database e o usuario do app na
// instancia compartilhada do engine e liga o app a ele pelo override. env
// vazio usa apps.DefaultDBEnv; services vazio liga todos os services.
func (c *Core) SetAppDatabase(ctx context.Context, name, engine string, services []string, env map[string]string) error {
	unlock, err := c.lockApp(name)
	if err != nil {
		return err
	}
	defer unlock()
	err = c.setDatabase(ctx, name, engine, services, env)
	c.CheckApps(ctx)
	return err
}

// setDatabase e o SetAppDatabase sem a trava (quem chama ja tem).
func (c *Core) setDatabase(ctx context.Context, name, engine string, services []string, env map[string]string) error {
	e, err := databases.Get(engine)
	if err != nil {
		return err
	}
	cfg, err := config.Load(c.deps.ConfigPath)
	if err != nil {
		return err
	}
	if !cfg.HasStack(engine) {
		return fmt.Errorf("a stack %s esta desligada; ligue no Setup antes de criar o banco", engine)
	}
	if len(env) == 0 {
		env = apps.DefaultDBEnv()
	}
	db := &apps.Database{Engine: engine, Services: services, Env: env}
	if err := apps.CheckDatabase(*db); err != nil {
		return err
	}

	m := c.appManager()
	pw, err := m.EnsureDBPassword(name)
	if err != nil {
		return err
	}
	if err := databases.Provision(ctx, c.deps.ExecInput, e, databases.Identifier(name), pw); err != nil {
		return err
	}
	return m.SetDatabase(ctx, name, db)
}

// RemoveAppDatabase desliga o app do banco. O database e os dados ficam:
// apagar dados nunca e efeito colateral.
func (c *Core) RemoveAppDatabase(ctx context.Context, name string) error {
	unlock, err := c.lockApp(name)
	if err != nil {
		return err
	}
	defer unlock()
	err = c.appManager().SetDatabase(ctx, name, nil)
	c.CheckApps(ctx)
	return err
}

// SetAppEnv troca as variaveis comuns do app. Com apply, refaz o deploy da
// tag no ar (o progresso aparece como deploy no estado).
func (c *Core) SetAppEnv(ctx context.Context, name string, env map[string]string, apply bool) error {
	unlock, err := c.lockApp(name)
	if err != nil {
		return err
	}
	defer unlock()
	if apply {
		c.setDeploying(name, true)
		defer c.setDeploying(name, false)
	}
	err = c.appManager().SetEnv(ctx, name, env, apply)
	c.CheckApps(ctx)
	return err
}
