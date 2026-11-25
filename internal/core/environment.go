package core

import (
	"context"
	"errors"
	"os"
	"runtime"
	"time"

	"github.com/trdSystemDev/cais/internal/docker"
)

// CheckEnvironment checa Docker e sistema, publica o resultado no estado e
// tambem o devolve (clientes como o MCP precisam da resposta da chamada).
// Chamadas simultaneas sao permitidas; Checking fica verdadeiro ate a
// ultima terminar. Problemas no Docker nao sao erro da chamada: fazem parte
// do resultado (Environment.Docker).
func (c *Core) CheckEnvironment(ctx context.Context) (Environment, error) {
	c.update(func(s *State) {
		c.checks++
		s.Environment.Checking = true
	})

	status := c.deps.CheckDocker(ctx)
	host := currentHost()
	security := c.deps.CheckHost(ctx)
	ts := c.deps.TailscaleStatus(ctx)

	s := c.update(func(s *State) {
		c.checks--
		s.Environment.Checking = c.checks > 0
		s.Environment.CheckedAt = c.deps.Now()
		s.Environment.Host = host
		s.Environment.Docker = dockerFromStatus(status)
		s.Environment.Security = security
		s.Environment.Tailscale = ts
	})
	return s.Environment, nil
}

// RunEnvironmentChecks refaz a checagem do ambiente a cada every: uma porta
// aberta ou o firewall desligado depois do harden aparecem no painel.
func (c *Core) RunEnvironmentChecks(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.CheckEnvironment(ctx)
		}
	}
}

func currentHost() Host {
	name, _ := os.Hostname()
	return Host{Hostname: name, OS: runtime.GOOS, Arch: runtime.GOARCH}
}

func dockerFromStatus(s docker.Status) Docker {
	return Docker{
		Installed: !errors.Is(s.ClientErr, docker.ErrNotInstalled),
		CLI:       component(s.ClientVersion, s.ClientErr),
		Daemon:    component(s.ServerVersion, s.ServerErr),
		Compose:   component(s.ComposeVersion, s.ComposeErr),
	}
}

func component(version string, err error) Component {
	if err != nil {
		return Component{Error: err.Error()}
	}
	return Component{Version: version}
}
