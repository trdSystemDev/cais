package core

import (
	"context"
	"errors"
	"os"

	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/stacks"
)

// CheckStacks pergunta ao docker como estao as stacks base, publica no
// estado (State.Infra) e devolve. Como o CheckEnvironment, problema numa
// stack nao e erro da chamada: fica no resultado dela.
func (c *Core) CheckStacks(ctx context.Context) (Infra, error) {
	c.update(func(s *State) {
		c.infraChecks++
		s.Infra.Checking = true
	})

	infra := c.inspectStacks(ctx)

	s := c.update(func(s *State) {
		c.infraChecks--
		infra.Checking = c.infraChecks > 0
		infra.CheckedAt = c.deps.Now()
		s.Infra = infra
	})
	return s.Infra, nil
}

func (c *Core) inspectStacks(ctx context.Context) Infra {
	var infra Infra
	cfg, err := config.Load(c.deps.ConfigPath)
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		infra.Error = err.Error()
	}
	files := c.setupDeps()
	for _, st := range stacks.All {
		ss := StackStatus{Name: st.Name, Desc: st.Desc, Enabled: cfg.HasStack(st.Name)}
		file := files.ComposeFile(st.Name)
		if _, err := os.Stat(file); err == nil {
			ss.Installed = true
			if ss.Containers, err = c.deps.ComposePS(ctx, file); err != nil {
				ss.Error = err.Error()
			}
		}
		infra.Stacks = append(infra.Stacks, ss)
	}
	return infra
}
