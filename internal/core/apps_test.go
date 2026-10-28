package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/docker"
)

// appDocker e um docker falso cujo Up espera o teste liberar.
type appDocker struct {
	release chan struct{}
	ups     chan string
}

func (d *appDocker) Up(ctx context.Context, p docker.Project) error {
	d.ups <- p.Name
	select {
	case <-d.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (d *appDocker) PS(_ context.Context, p docker.Project) ([]docker.Container, error) {
	return []docker.Container{{Name: p.Name + "-web-1", Service: "web", State: "running"}}, nil
}
func (d *appDocker) Restart(context.Context, docker.Project, string) error { return nil }
func (d *appDocker) Recreate(ctx context.Context, p docker.Project) error  { return d.Up(ctx, p) }
func (d *appDocker) Stop(context.Context, docker.Project) error            { return nil }

func newAppsCore(t *testing.T) (*Core, *appDocker) {
	t.Helper()
	root := t.TempDir()
	d := &appDocker{release: make(chan struct{}), ups: make(chan string, 10)}
	c := New(Deps{
		CheckDocker:   okStatus,
		Now:           func() time.Time { return fixedNow },
		ConfigPath:    filepath.Join(root, "config.yaml"),
		AppsDir:       filepath.Join(root, "apps"),
		AppDocker:     d,
		ContainerLogs: func(_ context.Context, name string) (string, error) { return "log de " + name, nil },
	})
	cfg := config.Default()
	cfg.Domain, cfg.Email = "exemplo.com.br", "a@exemplo.com.br"
	if err := config.Save(c.deps.ConfigPath, cfg); err != nil {
		t.Fatal(err)
	}
	return c, d
}

const appCompose = "services:\n  web:\n    image: registry.exemplo.com.br/meu-app:v1\n"

func TestDeployDoMesmoAppNaoRodaJunto(t *testing.T) {
	c, d := newAppsCore(t)
	ctx := context.Background()
	c.CreateApp(ctx, "meu-app")
	c.CreateApp(ctx, "outro")
	states, unsub, _ := c.Subscribe(ctx)
	defer unsub()

	done := make(chan error, 1)
	go func() {
		_, err := c.DeployApp(ctx, "meu-app", appCompose)
		done <- err
	}()
	<-d.ups // primeiro deploy chegou no up

	// o estado mostra o deploy em andamento
	for {
		s := recv(t, states)
		if a := findApp(s.Apps.List, "meu-app"); a != nil && a.Deploying {
			break
		}
	}
	if _, err := c.DeployApp(ctx, "meu-app", appCompose); err == nil || !strings.Contains(err.Error(), "em andamento") {
		t.Errorf("segundo deploy do mesmo app: %v", err)
	}

	// outro app pode subir ao mesmo tempo
	other := make(chan error, 1)
	go func() {
		_, err := c.DeployApp(ctx, "outro", appCompose)
		other <- err
	}()
	if name := <-d.ups; name != "outro" {
		t.Fatalf("up de %s, esperava outro", name)
	}

	close(d.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-other; err != nil {
		t.Fatal(err)
	}
	s := snap(t, c).Apps
	if a := findApp(s.List, "meu-app"); a == nil || a.Deploying || len(a.Containers) != 1 || a.Last == nil {
		t.Fatalf("estado final do app: %+v", a)
	}
}

func TestRotaNaoPodeUsarDominioDeStack(t *testing.T) {
	c, _ := newAppsCore(t)
	ctx := context.Background()
	c.CreateApp(ctx, "meu-app")
	err := c.SetAppRoutes(ctx, "meu-app", []apps.Route{{Host: "registry.exemplo.com.br", Service: "web", Port: 80}})
	if err == nil || !strings.Contains(err.Error(), "stack") {
		t.Fatalf("aceitou o dominio do registry: %v", err)
	}
}

func TestAppLogsSoDoProprioApp(t *testing.T) {
	c, d := newAppsCore(t)
	ctx := context.Background()
	c.CreateApp(ctx, "meu-app")
	close(d.release)
	if _, err := c.DeployApp(ctx, "meu-app", appCompose); err != nil {
		t.Fatal(err)
	}
	if out, err := c.AppLogs(ctx, "meu-app", "meu-app-web-1"); err != nil || out != "log de meu-app-web-1" {
		t.Fatalf("logs: %q %v", out, err)
	}
	for _, other := range []string{"registry", "postgres", "outro-web-1"} {
		if _, err := c.AppLogs(ctx, "meu-app", other); err == nil {
			t.Errorf("leu log de %s pelo app", other)
		}
	}
	if _, err := os.Stat(filepath.Join(c.deps.AppsDir, "meu-app", apps.ComposeFile)); err != nil {
		t.Fatal(err)
	}
}
