package core

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/trdSystemDev/cais/internal/config"
)

// core com caminhos temporarios e um compose up que espera o teste liberar
func newSetupCore(t *testing.T, release <-chan struct{}) *Core {
	t.Helper()
	root := t.TempDir()
	c := New(Deps{
		CheckDocker: okStatus,
		Now:         func() time.Time { return fixedNow },
		ConfigPath:  filepath.Join(root, "config.yaml"),
		SecretsDir:  filepath.Join(root, "secrets"),
		StackDir:    func(n string) string { return filepath.Join(root, "stacks", n) },
		ComposeUp: func(ctx context.Context, _ string, _ bool) error {
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
		ComposeDown: func(context.Context, string) error { return nil },
	})
	cfg := config.Default()
	cfg.Domain = "exemplo.com.br"
	cfg.Email = "admin@exemplo.com.br"
	if err := c.SaveConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRunSetupPublicaProgressoERecusaSegundaAcao(t *testing.T) {
	release := make(chan struct{})
	c := newSetupCore(t, release)
	states, unsub, _ := c.Subscribe(context.Background())
	defer unsub()

	done := make(chan error, 1)
	go func() {
		_, err := c.RunSetup(context.Background())
		done <- err
	}()

	// espera o Setup chegar no primeiro compose up (preso no release)
	for {
		s := recv(t, states)
		if s.Setup.Running && len(s.Setup.Steps) > 3 && s.Setup.Steps[3].Status == "running" {
			break
		}
	}
	if _, err := c.RunSetup(context.Background()); !errors.Is(err, ErrBusy) {
		t.Errorf("segundo Setup: %v, esperava ErrBusy", err)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	s := snap(t, c).Setup
	if s.Running || s.Error != "" || !s.FinishedAt.Equal(fixedNow) {
		t.Fatalf("estado final: %+v", s)
	}
	for _, st := range s.Steps {
		if st.Status != "done" {
			t.Errorf("passo %s %s", st.ID, st.Status)
		}
	}
}

func TestSaveConfigValidaStacks(t *testing.T) {
	c := newSetupCore(t, nil)
	info, _ := c.LoadConfig(context.Background())
	info.Config.Stacks = []string{"redis"}
	if err := c.SaveConfig(context.Background(), info.Config); err == nil {
		t.Fatal("salvou stack desconhecida")
	}
	if err := c.SetSecret(context.Background(), "postgres_password", "x"); err == nil {
		t.Fatal("deixou sobrescrever secret gerado")
	}
}

func TestSetServiceLigaEDesligaEAplica(t *testing.T) {
	release := make(chan struct{})
	close(release)
	c := newSetupCore(t, release)
	var stopped []string
	c.deps.ComposeDown = func(_ context.Context, file string) error {
		stopped = append(stopped, filepath.Base(filepath.Dir(file)))
		return nil
	}
	ctx := context.Background()

	if _, err := c.SetService(ctx, "postgres", true); err != nil {
		t.Fatal(err)
	}
	info, _ := c.LoadConfig(ctx)
	if !info.Config.HasStack("postgres") {
		t.Fatal("postgres nao ficou ligado na config")
	}
	if s := snap(t, c).Setup; s.Running || s.Error != "" || len(s.Steps) == 0 || s.Service != "postgres" || !s.ServiceOn {
		t.Fatalf("Setup nao rodou pra aplicar: %+v", s)
	}

	if _, err := c.SetService(ctx, "postgres", false); err != nil {
		t.Fatal(err)
	}
	if s := snap(t, c).Setup; s.Service != "postgres" || s.ServiceOn {
		t.Fatalf("operacao sem destino: %+v", s)
	}
	if len(stopped) != 1 || stopped[0] != "postgres" {
		t.Fatalf("stacks desligadas: %v", stopped)
	}
	if info, _ = c.LoadConfig(ctx); info.Config.HasStack("postgres") {
		t.Fatal("postgres continua ligado")
	}

	for _, bad := range []string{"proxy", "registry", "redis"} {
		if _, err := c.SetService(ctx, bad, false); err == nil {
			t.Errorf("deixou mexer em %q", bad)
		}
	}
}

func TestSetServiceSemConfig(t *testing.T) {
	root := t.TempDir()
	c := New(Deps{CheckDocker: okStatus, ConfigPath: filepath.Join(root, "config.yaml"), SecretsDir: filepath.Join(root, "s")})
	if _, err := c.SetService(context.Background(), "postgres", true); err == nil {
		t.Fatal("ligou servico sem a VPS configurada")
	}
}
