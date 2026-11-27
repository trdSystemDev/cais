package core

import (
	"context"
	"errors"
	"strings"

	"github.com/trdSystemDev/cais/internal/config"
)

// O reinicio da VPS fora do Corrigir (que reinicia na hora): agendado uma
// vez, ou o horario automatico (reboot_time da config), em que a VPS
// reinicia sozinha quando uma atualizacao pede. Fora do MCP: a IA nao
// reinicia a VPS.

// ScheduleReboot agenda o reinicio pra proxima vez do horario at (HH:MM,
// hoje ou amanha), no lugar do agendado; vazio desfaz o agendamento (com o
// automatico ligado e um reinicio pendente, ele agenda de novo).
func (c *Core) ScheduleReboot(ctx context.Context, at string) error {
	ctx = context.WithoutCancel(ctx)
	var err error
	if at = strings.TrimSpace(at); at == "" {
		err = c.deps.CancelReboot(ctx)
	} else if when, ok := config.NextClock(c.deps.Now(), at); !ok {
		return errors.New("horario " + at + " invalido (use HH:MM, ex: 01:00)")
	} else {
		err = c.deps.ScheduleReboot(ctx, when)
	}
	c.CheckEnvironment(ctx)
	if err == nil {
		c.deps.Logger.Info("reinicio da VPS", "agendado", at)
	}
	return err
}

// SetAutoReboot troca o horario automatico do reinicio (HH:MM; vazio
// desliga) e ja agenda, se houver reinicio pendente.
func (c *Core) SetAutoReboot(ctx context.Context, at string) error {
	ctx = context.WithoutCancel(ctx)
	// numa VPS sem o Setup ainda, a config comeca so com isso
	cfg, err := config.Load(c.deps.ConfigPath)
	if errors.Is(err, config.ErrNotFound) {
		cfg, err = config.Default(), nil
	}
	if err != nil {
		return err
	}
	cfg.RebootTime = strings.TrimSpace(at)
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := config.Save(c.deps.ConfigPath, cfg); err != nil {
		return err
	}
	err = c.autoReboot(ctx)
	c.CheckEnvironment(ctx)
	return err
}

// autoReboot aplica o horario automatico da config (a checagem do ambiente
// chama de hora em hora).
func (c *Core) autoReboot(ctx context.Context) error {
	clock := ""
	if cfg, err := config.Load(c.deps.ConfigPath); err == nil {
		clock = cfg.RebootTime
	}
	if err := c.deps.EnsureAutoReboot(ctx, clock, c.deps.Now()); err != nil {
		c.deps.Logger.Warn("reinicio automatico: nao deu pra agendar", "erro", err)
		return err
	}
	return nil
}
