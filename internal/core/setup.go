package core

import (
	"context"
	"errors"
	"fmt"
	"maps"
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
var ErrBusy = errors.New("outra acao esta mexendo na VPS agora (Setup ou banco); tente de novo quando ela terminar")

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
		ComposeUp:   c.deps.ComposeUp,
		ComposeDown: c.deps.ComposeDown,
		Exec:        c.deps.Exec,
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

func (c *Core) SaveConfig(ctx context.Context, cfg config.Config) error {
	return c.saveConfig(ctx, cfg)
}

func (c *Core) saveConfig(ctx context.Context, cfg config.Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if _, err := stacks.Enabled(cfg); err != nil {
		return err
	}
	if err := c.checkVersions(ctx, cfg); err != nil {
		return err
	}
	return config.Save(c.deps.ConfigPath, cfg)
}

// checkVersions recusa a troca de versao que os dados no volume nao
// aguentam (stacks.CheckUpgrade). A versao de partida e a do container no
// ar; sem ele (parado, desligado com os volumes), a da config de agora, que
// e a dos dados. Servico que nunca subiu nao tem dados: aceita qualquer uma.
func (c *Core) checkVersions(ctx context.Context, cfg config.Config) error {
	old, err := config.Load(c.deps.ConfigPath)
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(stacks.Images)) {
		to := stacks.Version(cfg, name)
		from := c.runningVersion(ctx, name)
		if from == "" && c.stackEverUp(name) {
			from = stacks.Version(old, name)
		}
		if err := stacks.CheckUpgrade(name, from, to); err != nil {
			return err
		}
	}
	return nil
}

// stackEverUp diz se o servico ja subiu: tem o compose gravado, ligado ou
// desligado (o desligado guarda o arquivo e os volumes).
func (c *Core) stackEverUp(name string) bool {
	file := c.setupDeps().ComposeFile(name)
	for _, f := range []string{file, file + bootstrap.DisabledSuffix} {
		if _, err := os.Stat(f); err == nil {
			return true
		}
	}
	return false
}

// runningVersion e a tag da imagem do container do servico (vazio sem
// container).
func (c *Core) runningVersion(ctx context.Context, name string) string {
	file := c.setupDeps().ComposeFile(name)
	if _, err := os.Stat(file); err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cs, err := c.deps.ComposePS(ctx, file)
	if err != nil {
		return ""
	}
	for _, ct := range cs {
		if tag := stacks.TagOf(name, ct.Image); tag != "" {
			return tag
		}
	}
	return ""
}

func (c *Core) SetSecret(_ context.Context, name, value string) error {
	return stacks.SetProvided(c.secretStore(), name, []byte(value))
}

func (c *Core) PlanSetup(context.Context) (bootstrap.Plan, error) {
	return bootstrap.MakePlan(c.setupDeps())
}

// RunSetup aplica a config salva em todos os servicos ligados de uma vez.
// As interfaces nao usam (ligam servico a servico, pelo SetService); fica
// pros testes montarem uma VPS pronta. O progresso vai pra State.Setup.
func (c *Core) RunSetup(ctx context.Context) (Setup, error) {
	if !c.infra.TryLock() {
		return Setup{}, ErrBusy
	}
	defer c.infra.Unlock()
	return c.runSetupLocked(ctx, "", false)
}

// SetService liga ou desliga um servico (proxy, banco, storage) e aplica
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

	// sem config ainda, parte do padrao: os servicos nao dependem do Setup
	// (o proxy com Let's Encrypt pede o e-mail, e a validacao diz onde por)
	cfg, err := config.Load(c.deps.ConfigPath)
	if errors.Is(err, config.ErrNotFound) {
		cfg, err = config.Default(), nil
	}
	if err != nil {
		return Setup{}, err
	}
	if cfg.HasStack(name) == on {
		return c.runSetupLocked(ctx, name, on) // ja estava: so garante que esta aplicado
	}
	if on {
		cfg.Stacks = append(cfg.Stacks, name)
	} else {
		cfg.Stacks = slices.DeleteFunc(cfg.Stacks, func(s string) bool { return s == name })
	}
	if err := c.saveConfig(ctx, cfg); err != nil {
		return Setup{}, err
	}
	return c.runSetupLocked(ctx, name, on)
}

// Acoes do ControlService.
const (
	ServiceStart   = "start"
	ServiceStop    = "stop"
	ServiceRestart = "restart"
)

func (c *Core) ControlService(ctx context.Context, name, action string) error {
	st, ok := stacks.Get(name)
	if !ok || !st.Optional {
		return fmt.Errorf("%q nao e um servico opcional (opcoes: %s)", name, strings.Join(optionalNames(), ", "))
	}
	if action != ServiceStart && action != ServiceStop && action != ServiceRestart {
		return fmt.Errorf("acao %q: use start, stop ou restart", action)
	}
	if !c.infra.TryLock() {
		return ErrBusy
	}
	defer c.infra.Unlock()

	cfg, err := config.Load(c.deps.ConfigPath)
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		return err
	}
	if !cfg.HasStack(name) {
		return fmt.Errorf("%s esta desligado: ligue antes (cais service %s on)", name, name)
	}
	file := c.setupDeps().ComposeFile(name)
	if _, err := os.Stat(file); err != nil {
		return fmt.Errorf("%s ainda nao subiu: aplique antes (cais service %s on)", name, name)
	}
	switch action {
	case ServiceStart:
		err = c.deps.ComposeUp(ctx, file, false)
	case ServiceStop:
		err = c.deps.ComposeStop(ctx, file)
	default:
		err = c.deps.ComposeRestart(ctx, file)
	}
	c.CheckStacks(ctx)
	return err
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

func (c *Core) runSetupLocked(ctx context.Context, service string, on bool) (Setup, error) {
	c.update(func(s *State) {
		s.Setup = Setup{Running: true, StartedAt: c.deps.Now(), Service: service, ServiceOn: on}
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
