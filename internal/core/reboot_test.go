package core

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/host"
)

// o reinicio: agendar uma vez vai pro proximo horario; o automatico fica
// na config e a checagem do ambiente aplica (e publica o agendado)
func TestReboot(t *testing.T) {
	var mu sync.Mutex
	var r host.Reboot
	var clocks []string
	now := time.Date(2026, 11, 27, 14, 30, 0, 0, time.Local)
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.Default()
	cfg.Email = "a@b.com"
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	c := New(Deps{
		CheckDocker: okStatus,
		Now:         func() time.Time { return now },
		CheckHost:   func(context.Context) []host.Item { return nil },
		ConfigPath:  cfgPath,
		RebootStatus: func(context.Context) host.Reboot {
			mu.Lock()
			defer mu.Unlock()
			return r
		},
		ScheduleReboot: func(_ context.Context, at time.Time) error {
			mu.Lock()
			defer mu.Unlock()
			r.At, r.Auto = at, false
			return nil
		},
		CancelReboot: func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			r.At, r.Auto = time.Time{}, false
			return nil
		},
		EnsureAutoReboot: func(_ context.Context, clock string, _ time.Time) error {
			mu.Lock()
			defer mu.Unlock()
			clocks = append(clocks, clock)
			return nil
		},
	})
	ctx := context.Background()

	if err := c.ScheduleReboot(ctx, "1:00"); err == nil {
		t.Error("aceitou 1:00")
	}
	if err := c.ScheduleReboot(ctx, "01:00"); err != nil {
		t.Fatal(err)
	}
	st, _ := c.Snapshot(ctx)
	if want := time.Date(2026, 11, 28, 1, 0, 0, 0, time.Local); !st.Environment.Reboot.At.Equal(want) {
		t.Errorf("agendado: %v, queria %v", st.Environment.Reboot.At, want)
	}
	if err := c.ScheduleReboot(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if st, _ := c.Snapshot(ctx); st.Environment.Reboot.Scheduled() {
		t.Error("o vazio nao desfez")
	}

	if err := c.SetAutoReboot(ctx, "02:00"); err != nil {
		t.Fatal(err)
	}
	if got, _ := config.Load(cfgPath); got.RebootTime != "02:00" {
		t.Errorf("config: %q", got.RebootTime)
	}
	if err := c.SetAutoReboot(ctx, "25:00"); err == nil {
		t.Error("aceitou 25:00")
	}
	c.CheckEnvironment(ctx)
	mu.Lock()
	defer mu.Unlock()
	if len(clocks) == 0 || clocks[len(clocks)-1] != "02:00" {
		t.Errorf("a checagem nao aplicou o automatico: %q", clocks)
	}
}
