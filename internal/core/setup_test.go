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
		Login: func(context.Context, string, string, []byte) error { return nil },
	})
	cfg := config.Default()
	cfg.Domain = "exemplo.com.br"
	cfg.Email = "admin@exemplo.com.br"
	cfg.Woodpecker = config.Woodpecker{GitHubClient: "Ov23li", Admin: "fulano"}
	if err := c.SaveConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if err := c.SetSecret(context.Background(), "github_client_secret", "x"); err != nil {
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
	if _, err := c.AddRegistryUser(context.Background(), "patrick"); !errors.Is(err, ErrBusy) {
		t.Errorf("usuario do registry durante o Setup: %v, esperava ErrBusy", err)
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
	info.Config.Stacks = []string{"dbgate"}
	if err := c.SaveConfig(context.Background(), info.Config); err == nil {
		t.Fatal("salvou dbgate sem proxy/infra")
	}
	if err := c.SetSecret(context.Background(), "postgres_password", "x"); err == nil {
		t.Fatal("deixou sobrescrever secret gerado")
	}
}
