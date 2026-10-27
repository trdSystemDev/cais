package docker

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// ComposeUp sobe o projeto do arquivo e espera todos os servicos ficarem
// de pe (healthy quando tem healthcheck). recreate forca recriar os
// containers: necessario quando um arquivo de secret montado mudou, ja que
// o bind mount continua preso no arquivo antigo.
func ComposeUp(ctx context.Context, file string, recreate bool) error {
	args := []string{"compose", "-f", file, "up", "-d", "--wait", "--wait-timeout", "300", "--remove-orphans"}
	if recreate {
		args = append(args, "--force-recreate")
	}
	_, err := execRunner(ctx, "docker", args...)
	if err != nil {
		return fmt.Errorf("docker compose up (%s): %w", file, lastLines(err.Error(), 15))
	}
	return nil
}

// Login faz docker login com a senha pelo stdin (nunca na linha de
// comando, onde apareceria no ps). A credencial fica no config.json do
// usuario que roda o daemon, que e quem faz os pulls.
func Login(ctx context.Context, host, user string, password []byte) error {
	cmd := exec.CommandContext(ctx, "docker", "login", host, "-u", user, "--password-stdin")
	cmd.Stdin = bytes.NewReader(password)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("docker login %s: %s", host, msg)
	}
	return nil
}

// lastLines fica com o fim da saida do compose: e la que esta o erro, e o
// comeco e so progresso de pull.
func lastLines(s string, n int) error {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return fmt.Errorf("%s", strings.Join(lines, "\n"))
}
