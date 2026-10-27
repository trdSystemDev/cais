package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/trdSystemDev/cais/internal/bootstrap"
	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/secrets"
	"github.com/trdSystemDev/cais/internal/stacks"
)

// ErrBusy indica que outra acao que mexe na VPS esta em andamento.
var ErrBusy = errors.New("outra acao esta mexendo na VPS agora (Setup ou registry); tente de novo quando ela terminar")

func (c *Core) setupDeps() bootstrap.Deps {
	return bootstrap.Deps{
		ConfigPath: c.deps.ConfigPath,
		SecretsDir: c.deps.SecretsDir,
		StackDir:   c.deps.StackDir,
		CheckDocker: func(ctx context.Context) error {
			env, _ := c.CheckEnvironment(ctx)
			if !env.Docker.OK() {
				return errors.New("docker ou compose com problema: veja o Dashboard")
			}
			return nil
		},
		ComposeUp:     c.deps.ComposeUp,
		ComposeDown:   c.deps.ComposeDown,
		Exec:          c.deps.Exec,
		Login:         c.deps.Login,
		LoginAttempts: 18,
		LoginDelay:    10 * time.Second,
	}
}

func (c *Core) secretStore() secrets.FileStore {
	return secrets.FileStore{Dir: c.deps.SecretsDir}
}

func (c *Core) LoadConfig(context.Context) (ConfigInfo, error) {
	info := ConfigInfo{Path: c.deps.ConfigPath}
	cfg, err := config.Load(c.deps.ConfigPath)
	if errors.Is(err, config.ErrNotFound) {
		info.Config = config.Default()
		return info, nil
	}
	if err != nil {
		return info, err
	}
	info.Exists = true
	info.Config = cfg
	return info, nil
}

func (c *Core) SaveConfig(_ context.Context, cfg config.Config) error {
	return c.saveConfig(cfg)
}

func (c *Core) saveConfig(cfg config.Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if _, err := stacks.Enabled(cfg); err != nil {
		return err
	}
	return config.Save(c.deps.ConfigPath, cfg)
}

func (c *Core) SetSecret(_ context.Context, name, value string) error {
	return stacks.SetProvided(c.secretStore(), name, []byte(value))
}

func (c *Core) PlanSetup(context.Context) (bootstrap.Plan, error) {
	return bootstrap.MakePlan(c.setupDeps())
}

// RunSetup roda o Setup inteiro. O progresso vai pra State.Setup a cada
// passo, entao qualquer interface acompanha ao vivo; o resultado final
// tambem e devolvido. Um Setup que falha pode ser rodado de novo.
func (c *Core) RunSetup(ctx context.Context) (Setup, error) {
	if !c.infra.TryLock() {
		return Setup{}, ErrBusy
	}
	defer c.infra.Unlock()
	return c.runSetupLocked(ctx)
}

// SetService liga ou desliga um servico opcional (banco, storage) e aplica
// na hora: grava a config e roda o Setup, que sobe ou derruba so o que
// mudou. O lock vem antes de gravar: com outra acao em andamento, nada muda.
func (c *Core) SetService(ctx context.Context, name string, on bool) (Setup, error) {
	st, ok := stacks.Get(name)
	if !ok || !st.Optional {
		return Setup{}, fmt.Errorf("%q nao e um servico opcional (opcoes: %s)", name, strings.Join(optionalNames(), ", "))
	}
	if !c.infra.TryLock() {
		return Setup{}, ErrBusy
	}
	defer c.infra.Unlock()

	cfg, err := config.Load(c.deps.ConfigPath)
	if errors.Is(err, config.ErrNotFound) {
		return Setup{}, errors.New("a VPS ainda nao tem configuracao: preencha a aba Setup primeiro")
	}
	if err != nil {
		return Setup{}, err
	}
	if cfg.HasStack(name) == on {
		return c.runSetupLocked(ctx) // ja estava: so garante que esta aplicado
	}
	if on {
		cfg.Stacks = append(cfg.Stacks, name)
	} else {
		cfg.Stacks = slices.DeleteFunc(cfg.Stacks, func(s string) bool { return s == name })
	}
	if err := c.saveConfig(cfg); err != nil {
		return Setup{}, err
	}
	return c.runSetupLocked(ctx)
}

func optionalNames() []string {
	var names []string
	for _, s := range stacks.All {
		if s.Optional {
			names = append(names, s.Name)
		}
	}
	return names
}

func (c *Core) runSetupLocked(ctx context.Context) (Setup, error) {
	c.update(func(s *State) {
		s.Setup = Setup{Running: true, StartedAt: c.deps.Now()}
	})

	steps, err := bootstrap.Run(ctx, c.setupDeps(), func(steps []bootstrap.Step) {
		c.update(func(s *State) { s.Setup.Steps = steps })
	})

	st := c.update(func(s *State) {
		s.Setup.Running = false
		s.Setup.FinishedAt = c.deps.Now()
		if steps != nil {
			s.Setup.Steps = steps
		}
		if err != nil {
			s.Setup.Error = err.Error()
		}
	})
	return st.Setup, err
}

func (c *Core) RegistryUsers(context.Context) ([]string, error) {
	return secrets.RegistryUsers(c.secretStore())
}

func (c *Core) AddRegistryUser(ctx context.Context, user string) (string, error) {
	if !c.infra.TryLock() {
		return "", ErrBusy
	}
	defer c.infra.Unlock()

	pw, err := secrets.AddRegistryUser(c.secretStore(), user)
	if err != nil {
		return "", err
	}
	if err := c.reloadRegistry(ctx); err != nil {
		return "", fmt.Errorf("usuario criado, mas o registry nao recarregou (rode o Setup de novo): %w", err)
	}
	return pw, nil
}

func (c *Core) RemoveRegistryUser(ctx context.Context, user string) error {
	if !c.infra.TryLock() {
		return ErrBusy
	}
	defer c.infra.Unlock()

	if err := secrets.RemoveRegistryUser(c.secretStore(), user); err != nil {
		return err
	}
	if err := c.reloadRegistry(ctx); err != nil {
		// o usuario ainda entra ate o container ser recriado: isso e erro
		return fmt.Errorf("usuario removido do htpasswd, mas o registry ainda nao recarregou e o acesso continua: %w", err)
	}
	return nil
}

// reloadRegistry recria o container do registry pra ele ler o htpasswd
// novo. Antes do primeiro Setup nao ha registry rodando: nada a fazer.
func (c *Core) reloadRegistry(ctx context.Context) error {
	file := c.setupDeps().ComposeFile("registry")
	if _, err := os.Stat(file); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return c.deps.ComposeUp(ctx, file, true)
}
