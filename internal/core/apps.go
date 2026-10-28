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
