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
			if name == "garage" {
				return nil, errors.New("docker fora do ar")
			}
			return ps[name], nil
		},
	})
	cfg := config.Default()
	cfg.Email = "a@exemplo.com.br"
	cfg.Stacks, cfg.Hosts.S3 = []string{"proxy", "postgres", "mysql", "garage"}, "s3.exemplo.com.br"
	if err := c.SaveConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	// proxy, postgres e garage instalados; mysql ativo mas nunca subiu
	for _, n := range []string{"proxy", "postgres", "garage"} {
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
	if s := got["garage"]; s.Error == "" || s.Healthy() {
		t.Errorf("erro do docker tem que ficar na stack: %+v", s)
	}
	if len(got) != 4 {
		t.Errorf("stacks %v", got)
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

func TestContainerLogsValidaPertencimento(t *testing.T) {
	var called string
	root := t.TempDir()
	stackDir := func(n string) string { return filepath.Join(root, n) }
	os.MkdirAll(stackDir("proxy"), 0o755)
	os.WriteFile(filepath.Join(stackDir("proxy"), "docker-compose.yml"), []byte("name: proxy\n"), 0o644)
	c := New(Deps{
		StackDir: stackDir,
		// como o docker compose: sem o arquivo, erro
		ComposePS: func(_ context.Context, file string) ([]docker.Container, error) {
			if _, err := os.Stat(file); err != nil {
				return nil, err
			}
			return []docker.Container{{Name: "proxy-traefik"}}, nil
		},
		ContainerLogs: func(_ context.Context, name string, _ docker.LogsOptions) (string, error) {
			called = name
			return "traefik pronto", nil
		},
	})
	if _, err := c.ContainerLogs(context.Background(), "inexistente", "proxy-traefik", docker.LogsOptions{}); err == nil {
		t.Fatal("aceitou stack desconhecida")
	}
	if _, err := c.ContainerLogs(context.Background(), "proxy", "mysql", docker.LogsOptions{}); err == nil {
		t.Fatal("aceitou container de outra stack")
	}
	// stack que o Setup nao subiu: o motivo, nao o erro do docker compose
	if _, err := c.ContainerLogs(context.Background(), "postgres", "postgres", docker.LogsOptions{}); err == nil ||
		err.Error() != "a stack postgres nao esta instalada: ligue na aba Infra" {
		t.Fatalf("stack nao instalada: %v", err)
	}
	if called != "" {
		t.Fatal("leu logs antes de validar")
	}
	out, err := c.ContainerLogs(context.Background(), "proxy", "proxy-traefik", docker.LogsOptions{})
	if err != nil || out != "traefik pronto" || called != "proxy-traefik" {
		t.Fatalf("%q, %v, %q", out, err, called)
	}
}
