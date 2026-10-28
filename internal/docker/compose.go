package docker

import (
	"bytes"
	"context"
	"encoding/json"
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

// ComposeDown derruba o projeto do arquivo (containers e redes dele). Os
// volumes ficam: apagar dados e outra decisao, nunca efeito colateral.
func ComposeDown(ctx context.Context, file string) error {
	_, err := execRunner(ctx, "docker", "compose", "-f", file, "down", "--remove-orphans")
	if err != nil {
		return fmt.Errorf("docker compose down (%s): %w", file, lastLines(err.Error(), 15))
	}
	return nil
}

// Container e um container de um projeto compose, como o "compose ps" ve.
type Container struct {
	Name    string `json:"name"`
	Service string `json:"service"`
	Image   string `json:"image"`
	// State e o estado do docker: running, exited, restarting, created...
	State string `json:"state"`
	// Health vem do healthcheck: healthy, unhealthy, starting ou vazio
	// quando o servico nao tem healthcheck.
	Health string `json:"health,omitempty"`
	// Status e o texto do docker ("Up 5 minutes (healthy)").
	Status string `json:"status"`
}

// ComposePS lista os containers do projeto do arquivo, inclusive parados.
func ComposePS(ctx context.Context, file string) ([]Container, error) {
	out, err := execRunner(ctx, "docker", "compose", "-f", file, "ps", "-a", "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("docker compose ps (%s): %w", file, lastLines(err.Error(), 5))
	}
	return parsePS(out)
}

// parsePS aceita um JSON por linha (compose atual) ou uma lista JSON
// (versoes antigas).
func parsePS(out string) ([]Container, error) {
	type raw struct {
		Name, Service, Image, State, Health, Status string
	}
	var rows []raw
	out = strings.TrimSpace(out)
	switch {
	case out == "":
	case strings.HasPrefix(out, "["):
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			return nil, fmt.Errorf("saida do compose ps: %w", err)
		}
	default:
		for _, line := range strings.Split(out, "\n") {
			var r raw
			if err := json.Unmarshal([]byte(line), &r); err != nil {
				return nil, fmt.Errorf("saida do compose ps: %w", err)
			}
			rows = append(rows, r)
		}
	}
	cs := make([]Container, 0, len(rows))
	for _, r := range rows {
		cs = append(cs, Container{Name: r.Name, Service: r.Service, Image: r.Image, State: r.State, Health: r.Health, Status: r.Status})
	}
	return cs, nil
}

// Exec roda um comando num container e devolve a saida padrao.
func Exec(ctx context.Context, container string, args ...string) (string, error) {
	out, err := execRunner(ctx, "docker", append([]string{"exec", container}, args...)...)
	if err != nil {
		return "", fmt.Errorf("docker exec %s %s: %w", container, strings.Join(args, " "), lastLines(err.Error(), 5))
	}
	return out, nil
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

// Project e um projeto compose de app: nome fixo (o -p tem prioridade sobre
// qualquer name: no arquivo) e os arquivos na ordem do merge. A pasta do
// primeiro arquivo e a base dos caminhos relativos (./secrets, ./app.env).
type Project struct {
	Name  string
	Files []string
}

func (p Project) args(cmd ...string) []string {
	args := []string{"compose", "-p", p.Name}
	for _, f := range p.Files {
		args = append(args, "-f", f)
	}
	return append(args, cmd...)
}

// CLI implementa as operacoes de projeto com o docker compose da maquina.
type CLI struct{}

// Up sobe o projeto e espera os servicos ficarem saudaveis. Imagem que
// ainda nao existe na maquina e baixada (tags de commit nunca mudam).
func (CLI) Up(ctx context.Context, p Project) error {
	_, err := execRunner(ctx, "docker", p.args("up", "-d", "--wait", "--wait-timeout", "300", "--remove-orphans")...)
	if err != nil {
		return fmt.Errorf("docker compose up (%s): %w", p.Name, lastLines(err.Error(), 15))
	}
	return nil
}

// PS lista os containers do projeto, inclusive parados.
func (CLI) PS(ctx context.Context, p Project) ([]Container, error) {
	out, err := execRunner(ctx, "docker", p.args("ps", "-a", "--format", "json")...)
	if err != nil {
		return nil, fmt.Errorf("docker compose ps (%s): %w", p.Name, lastLines(err.Error(), 5))
	}
	return parsePS(out)
}

// Restart reinicia um service do projeto, ou todos se service for vazio.
func (CLI) Restart(ctx context.Context, p Project, service string) error {
	cmd := []string{"restart"}
	if service != "" {
		cmd = append(cmd, service)
	}
	if _, err := execRunner(ctx, "docker", p.args(cmd...)...); err != nil {
		return fmt.Errorf("docker compose restart (%s): %w", p.Name, lastLines(err.Error(), 5))
	}
	return nil
}

// Stop para os containers do projeto sem remover nada.
func (CLI) Stop(ctx context.Context, p Project) error {
	if _, err := execRunner(ctx, "docker", p.args("stop")...); err != nil {
		return fmt.Errorf("docker compose stop (%s): %w", p.Name, lastLines(err.Error(), 5))
	}
	return nil
}

// ExecInput roda um comando num container mandando input pelo stdin. Use
// pra qualquer coisa com senha (SQL de criar usuario): pela linha de
// comando ela apareceria no ps do host enquanto o comando roda.
func ExecInput(ctx context.Context, container string, input []byte, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", append([]string{"exec", "-i", container}, args...)...)
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("docker exec %s %s: %s", container, args[0], msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}
