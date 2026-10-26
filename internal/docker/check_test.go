package docker

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// fakeRunner responde de acordo com os argumentos, simulando cada cenario.
func fakeRunner(responses map[string]struct {
	out string
	err error
}) runner {
	return func(_ context.Context, name string, args ...string) (string, error) {
		key := name + " " + strings.Join(args, " ")
		r, ok := responses[key]
		if !ok {
			return "", errors.New("comando inesperado: " + key)
		}
		return r.out, r.err
	}
}

const (
	cmdClient  = "docker version --format {{.Client.Version}}"
	cmdServer  = "docker version --format {{.Server.Version}}"
	cmdCompose = "docker compose version --short"
)

func TestCheckTudoOK(t *testing.T) {
	s := check(context.Background(), fakeRunner(map[string]struct {
		out string
		err error
	}{
		cmdClient:  {out: "29.0.1"},
		cmdServer:  {out: "29.0.1"},
		cmdCompose: {out: "2.40.0"},
	}))
	if !s.OK() {
		t.Fatalf("esperava OK, veio %+v", s)
	}
	if s.ClientVersion != "29.0.1" || s.ServerVersion != "29.0.1" || s.ComposeVersion != "2.40.0" {
		t.Fatalf("versoes erradas: %+v", s)
	}
}

func TestCheckDockerAusente(t *testing.T) {
	s := check(context.Background(), fakeRunner(map[string]struct {
		out string
		err error
	}{
		cmdClient: {err: exec.ErrNotFound},
	}))
	if !errors.Is(s.ClientErr, ErrNotInstalled) || !errors.Is(s.ServerErr, ErrNotInstalled) || !errors.Is(s.ComposeErr, ErrNotInstalled) {
		t.Fatalf("esperava ErrNotInstalled em tudo, veio %+v", s)
	}
}

func TestCheckDaemonSemPermissao(t *testing.T) {
	permErr := errors.New("permission denied while trying to connect to the Docker daemon socket")
	s := check(context.Background(), fakeRunner(map[string]struct {
		out string
		err error
	}{
		// cliente sai com erro mas imprime a propria versao
		cmdClient:  {out: "29.0.1", err: permErr},
		cmdServer:  {err: permErr},
		cmdCompose: {out: "2.40.0"},
	}))
	if s.ClientErr != nil || s.ClientVersion != "29.0.1" {
		t.Fatalf("cliente deveria estar ok: %+v", s)
	}
	if s.ServerErr == nil || !strings.Contains(s.ServerErr.Error(), "permission denied") {
		t.Fatalf("esperava erro de permissao no daemon: %+v", s)
	}
	if s.OK() {
		t.Fatal("nao deveria estar OK")
	}
}

func TestCheckSemCompose(t *testing.T) {
	s := check(context.Background(), fakeRunner(map[string]struct {
		out string
		err error
	}{
		cmdClient:  {out: "29.0.1"},
		cmdServer:  {out: "29.0.1"},
		cmdCompose: {err: errors.New("docker: 'compose' is not a docker command.")},
	}))
	if s.ComposeErr == nil || s.OK() {
		t.Fatalf("esperava erro no compose: %+v", s)
	}
}
