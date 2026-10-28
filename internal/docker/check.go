package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ErrNotInstalled indica que o binario "docker" nao esta no PATH.
var ErrNotInstalled = errors.New("docker nao encontrado no PATH (instale com: curl -fsSL https://get.docker.com | sh)")

// Status e o resultado da checagem do ambiente Docker. Cada parte tem a
// propria versao ou o proprio erro, pra UI conseguir mostrar exatamente
// o que falta (ex: CLI ok mas daemon sem permissao).
type Status struct {
	ClientVersion  string
	ClientErr      error
	ServerVersion  string
	ServerErr      error
	ComposeVersion string
	ComposeErr     error
}

// OK diz se CLI, daemon e plugin compose estao todos funcionando.
func (s Status) OK() bool {
	return s.ClientErr == nil && s.ServerErr == nil && s.ComposeErr == nil
}

// runner executa um comando e devolve o stdout. Em caso de falha, o erro
// carrega a mensagem do stderr quando houver. Existe pra ser trocado nos testes.
type runner func(ctx context.Context, name string, args ...string) (string, error)

func execRunner(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	// pasta neutra: comando do compose sem -f nunca pega um arquivo da
	// pasta de onde o daemon foi iniciado
	cmd.Dir = "/"
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			err = fmt.Errorf("%s", msg)
		}
	}
	return strings.TrimSpace(stdout.String()), err
}

// Check verifica se o docker CLI existe, se o daemon responde (com a
// permissao do usuario atual) e se o plugin compose esta instalado.
func Check(ctx context.Context) Status {
	return check(ctx, execRunner)
}

func check(ctx context.Context, run runner) Status {
	var s Status

	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// com o daemon fora do ar, "docker version" sai com erro mas ainda
	// imprime a versao do cliente -- por isso o stdout vale mesmo com err
	client, err := run(cctx, "docker", "version", "--format", "{{.Client.Version}}")
	if errors.Is(err, exec.ErrNotFound) {
		s.ClientErr = ErrNotInstalled
		s.ServerErr = ErrNotInstalled
		s.ComposeErr = ErrNotInstalled
		return s
	}
	if client == "" {
		s.ClientErr = errOr(err, "versao do cliente vazia")
	} else {
		s.ClientVersion = client
	}

	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	server, err := run(sctx, "docker", "version", "--format", "{{.Server.Version}}")
	if err != nil || server == "" {
		s.ServerErr = errOr(err, "daemon nao respondeu")
	} else {
		s.ServerVersion = server
	}

	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	compose, err := run(pctx, "docker", "compose", "version", "--short")
	if err != nil || compose == "" {
		s.ComposeErr = errOr(err, "plugin compose nao respondeu")
	} else {
		s.ComposeVersion = compose
	}

	return s
}

func errOr(err error, fallback string) error {
	if err != nil {
		return err
	}
	return errors.New(fallback)
}
