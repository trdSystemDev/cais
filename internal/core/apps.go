package core

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/docker"
)

func (c *Core) appManager() apps.Manager {
	src := c.deps.AppSource
	if src == nil {
		src = githubSource{c}
	}
	m := apps.Manager{Root: c.deps.AppsDir, Source: src, Docker: c.deps.AppDocker, Now: c.deps.Now, Settle: c.deps.AppSettle,
		DBCredentials: c.dbCredentials, BucketCredentials: c.bucketCredentials}
	if cfg, err := config.Load(c.deps.ConfigPath); err == nil {
		m.Networks = infraNetworks(cfg)
	}
	return m
}

// infraNetworks sao as redes das stacks ligadas que um app pode usar.
func infraNetworks(cfg config.Config) []string {
	var out []string
	for _, st := range []string{"postgres", "mysql", "garage"} {
		if cfg.HasStack(st) {
			out = append(out, st+"_network")
		}
	}
	return out
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

// CheckApps le os apps do disco, pergunta ao docker pelos containers de
// cada um, publica em State.Apps e devolve. Problema num app fica no
// resultado dele.
func (c *Core) CheckApps(ctx context.Context) (Apps, error) {
	// duas checagens juntas publicariam fora de ordem
	c.appsCheck.Lock()
	defer c.appsCheck.Unlock()
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

func (c *Core) CreateApp(ctx context.Context, a apps.App) (apps.App, error) {
	a, err := c.appManager().Create(a)
	if err != nil {
		return a, err
	}
	c.CheckApps(ctx)
	return a, nil
}

func (c *Core) UpdateApp(ctx context.Context, a apps.App) error {
	unlock, err := c.lockApp(a.Name)
	if err != nil {
		return err
	}
	defer unlock()
	err = c.appManager().Update(a)
	c.CheckApps(ctx)
	return err
}

// DeployApp roda com o contexto do daemon: se a TUI cair no meio, o deploy
// termina mesmo assim. O andamento (Deploying) aparece no estado.
func (c *Core) DeployApp(ctx context.Context, name, ref string) (apps.DeployResult, error) {
	if err := apps.CheckName(name); err != nil {
		return apps.DeployResult{}, err
	}
	unlock, err := c.lockApp(name)
	if err != nil {
		return apps.DeployResult{}, err
	}
	defer unlock()

	c.setDeploying(name, true)
	res, err := c.appManager().Deploy(ctx, name, ref)
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

func (c *Core) DeleteApp(ctx context.Context, name string, volumes bool) error {
	unlock, err := c.lockApp(name)
	if err != nil {
		return err
	}
	defer unlock()
	err = c.appManager().Delete(ctx, name, volumes)
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

func (c *Core) AppEnv(_ context.Context, name string) (map[string]string, error) {
	return c.appManager().Env(name)
}

func (c *Core) SetAppEnv(ctx context.Context, name string, env map[string]string) error {
	for k := range env {
		if apps.IsReservedEnv(k) {
			return fmt.Errorf("%s e escrita pelo cais em todo deploy", k)
		}
	}
	unlock, err := c.lockApp(name)
	if err != nil {
		return err
	}
	defer unlock()
	return c.appManager().SetEnv(name, env)
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
	err = c.appManager().SetSecret(name, secret, []byte(value))
	c.CheckApps(ctx)
	return err
}

func (c *Core) DeleteAppSecret(ctx context.Context, name, secret string) error {
	unlock, err := c.lockApp(name)
	if err != nil {
		return err
	}
	defer unlock()
	err = c.appManager().DeleteSecret(name, secret)
	c.CheckApps(ctx)
	return err
}

// AppGuide monta o texto de ajuda com o dominio sugerido, as redes das
// stacks ligadas e as variaveis e secrets do app.
func (c *Core) AppGuide(_ context.Context, name string) (string, error) {
	m := c.appManager()
	a, err := m.Get(name)
	if err != nil {
		return "", err
	}
	cfg, err := config.Load(c.deps.ConfigPath)
	if err != nil {
		return "", err
	}
	in := apps.GuideInput{App: a.Name, Host: a.Name + "." + cfg.Domain, Networks: infraNetworks(cfg)}
	env, err := m.Env(name)
	if err != nil {
		return "", err
	}
	for k := range env {
		in.Env = append(in.Env, k)
	}
	slices.Sort(in.Env)
	secrets, err := m.Secrets(name)
	if err != nil {
		return "", err
	}
	for _, s := range secrets {
		in.Secrets = append(in.Secrets, s.Name)
	}
	in.Databases, in.Buckets = a.Databases, a.Buckets
	return apps.Guide(in), nil
}
