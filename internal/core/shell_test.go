package core

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/docker"
)

func TestOpenShellEscolheOContainer(t *testing.T) {
	var started []string
	noShell := map[string]bool{}
	c := New(Deps{
		CheckDocker: okStatus,
		Exec: func(_ context.Context, container string, args ...string) (string, error) {
			if noShell[container] {
				return "", errors.New(`exec: "sh": executable file not found in $PATH`)
			}
			return "", nil
		},
		StartTerminal: func(container, term string, rows, cols uint16, args ...string) (*os.File, error) {
			started = append(started, container+"|"+term)
			return os.Open(os.DevNull)
		},
	})
	c.update(func(s *State) {
		s.Apps.List = []AppStatus{
			{App: apps.App{Name: "loja"}, Containers: []docker.Container{
				{Name: "loja-web-1", Service: "web", State: "running"},
				{Name: "loja-worker-1", Service: "worker", State: "running"},
				{Name: "loja-migrate-1", Service: "migrate", State: "exited"}}},
			{App: apps.App{Name: "blog"}, Containers: []docker.Container{{Name: "blog-web-1", Service: "web", State: "running"}}},
			{App: apps.App{Name: "parado"}},
		}
	})
	ctx := context.Background()
	open := func(req ShellRequest) error {
		f, err := c.OpenShell(ctx, req)
		if f != nil {
			f.Close()
		}
		return err
	}
	if err := open(ShellRequest{App: "loja", Service: "worker", Term: "xterm-256color"}); err != nil {
		t.Fatal(err)
	}
	if err := open(ShellRequest{App: "blog", Term: "x; rm -rf /"}); err != nil {
		t.Fatal(err)
	}
	if err := open(ShellRequest{App: "loja", Service: "loja-web-1"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(started, ",") != "loja-worker-1|xterm-256color,blog-web-1|,loja-web-1|" {
		t.Errorf("abriu: %v", started)
	}
	for _, c := range []struct {
		req  ShellRequest
		want string
	}{
		{ShellRequest{App: "loja"}, "escolha o service (web, worker)"},
		{ShellRequest{App: "loja", Service: "migrate"}, "nao tem o service migrate rodando"},
		{ShellRequest{App: "parado"}, "nao tem container rodando"},
		{ShellRequest{App: "outro"}, "app outro nao encontrado"},
	} {
		if err := open(c.req); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v", c.req, err)
		}
	}
	noShell["blog-web-1"] = true
	if err := open(ShellRequest{App: "blog"}); err == nil || !strings.Contains(err.Error(), "nao tem shell") {
		t.Errorf("sem sh: %v", err)
	}
}

func TestOpenShellConsoleDoBanco(t *testing.T) {
	var got []string
	down := true
	c := New(Deps{
		CheckDocker: okStatus,
		Exec: func(_ context.Context, container string, args ...string) (string, error) {
			if down {
				return "", errors.New("No such container: " + container)
			}
			return "", nil
		},
		StartTerminal: func(container, term string, rows, cols uint16, args ...string) (*os.File, error) {
			got = append([]string{container}, args...)
			return os.Open(os.DevNull)
		},
	})
	ctx := context.Background()
	if _, err := c.OpenShell(ctx, ShellRequest{Engine: "postgres"}); err == nil || !strings.Contains(err.Error(), "nao esta no ar") {
		t.Fatalf("parado: %v", err)
	}
	down = false
	f, err := c.OpenShell(ctx, ShellRequest{Engine: "mysql", Database: "loja"})
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if got[0] != "mysql" || got[len(got)-1] != "loja" {
		t.Errorf("mysql: %v", got)
	}
	if _, err := c.OpenShell(ctx, ShellRequest{Engine: "mongo"}); err == nil {
		t.Error("aceitou banco desconhecido")
	}
}
