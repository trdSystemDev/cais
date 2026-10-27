package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/docker"
)

func TestCheckStacks(t *testing.T) {
	root := t.TempDir()
	stackDir := func(n string) string { return filepath.Join(root, "stacks", n) }
	ps := map[string][]docker.Container{
		"proxy":    {{Name: "proxy-traefik", State: "running", Health: "healthy"}, {Name: "proxy-docker-proxy", State: "running"}},
		"postgres": {{Name: "postgres", State: "exited", Status: "Exited (1)"}},
	}
	c := New(Deps{
		CheckDocker: okStatus,
		Now:         func() time.Time { return fixedNow },
		ConfigPath:  filepath.Join(root, "config.yaml"),
		SecretsDir:  filepath.Join(root, "secrets"),
		StackDir:    stackDir,
		ComposePS: func(_ context.Context, file string) ([]docker.Container, error) {
			name := filepath.Base(filepath.Dir(file))
			if name == "registry" {
				return nil, errors.New("docker fora do ar")
			}
			return ps[name], nil
		},
	})
	cfg := config.Default()
	cfg.Domain, cfg.Email = "exemplo.com.br", "a@exemplo.com.br"
	cfg.Woodpecker = config.Woodpecker{GitHubClient: "Ov23li", Admin: "fulano"}
	cfg.Stacks = []string{"postgres", "mysql"}
	if err := c.SaveConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	// proxy, postgres e registry instalados; mysql ativo mas nunca subiu
	for _, n := range []string{"proxy", "postgres", "registry"} {
		os.MkdirAll(stackDir(n), 0o700)
		os.WriteFile(filepath.Join(stackDir(n), "docker-compose.yml"), []byte("name: "+n), 0o644)
	}

	infra, err := c.CheckStacks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if infra.Checking || !infra.CheckedAt.Equal(fixedNow) {
		t.Fatalf("infra: %+v", infra)
	}
	got := map[string]StackStatus{}
	for _, s := range infra.Stacks {
		got[s.Name] = s
	}
	if s := got["proxy"]; !s.Enabled || !s.Installed || !s.Healthy() || len(s.Containers) != 2 {
		t.Errorf("proxy: %+v", s)
	}
	if s := got["postgres"]; !s.Installed || s.Healthy() {
		t.Errorf("postgres parado tem que aparecer com problema: %+v", s)
	}
	if s := got["mysql"]; !s.Enabled || s.Installed || s.Healthy() {
		t.Errorf("mysql ativo e nao instalado: %+v", s)
	}
	if s := got["registry"]; s.Error == "" || s.Healthy() {
		t.Errorf("erro do docker tem que ficar na stack: %+v", s)
	}
	if s := got["woodpecker"]; !s.Enabled || s.Installed {
		t.Errorf("woodpecker e fixo (sempre ativo), mas nao subiu: %+v", s)
	}
	if s := got["garage"]; s.Enabled || s.Installed {
		t.Errorf("garage desligado: %+v", s)
	}
	if snap(t, c).Infra.CheckedAt.IsZero() {
		t.Error("resultado nao foi publicado no estado")
	}
}

func TestHealthyComHealthcheckSubindo(t *testing.T) {
	s := StackStatus{Installed: true, Containers: []docker.Container{{State: "running", Health: "starting"}}}
	if s.Healthy() {
		t.Error("healthcheck ainda subindo nao e saudavel")
	}
}
