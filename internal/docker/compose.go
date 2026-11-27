package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
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
		return fmt.Errorf("docker compose up (%s): %w", file, tail(err, 15))
	}
	return nil
}

// ComposeDown derruba o projeto do arquivo (containers e redes dele). Os
// volumes ficam: apagar dados e outra decisao, nunca efeito colateral.
func ComposeDown(ctx context.Context, file string) error {
	_, err := execRunner(ctx, "docker", "compose", "-f", file, "down", "--remove-orphans")
	if err != nil {
		return fmt.Errorf("docker compose down (%s): %w", file, tail(err, 15))
	}
	return nil
}

// ComposeStop para os containers do projeto do arquivo sem remover nada.
func ComposeStop(ctx context.Context, file string) error {
	if _, err := execRunner(ctx, "docker", "compose", "-f", file, "stop"); err != nil {
		return fmt.Errorf("docker compose stop (%s): %w", file, tail(err, 15))
	}
	return nil
}

// ComposeRestart reinicia os containers do projeto do arquivo.
func ComposeRestart(ctx context.Context, file string) error {
	if _, err := execRunner(ctx, "docker", "compose", "-f", file, "restart"); err != nil {
		return fmt.Errorf("docker compose restart (%s): %w", file, tail(err, 15))
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
	// ExitCode e o codigo de saida de um container parado.
	ExitCode int `json:"exit_code,omitempty"`
	// URLs sao os enderecos publicados pelo Traefik (Host das regras dos
	// routers, com traefik.enable=true), ja com as variaveis resolvidas.
	URLs []string `json:"urls,omitempty"`
	// Job: outro service espera este terminar (depends_on com
	// service_completed_successfully), ou seja, e tarefa e nao servico.
	Job bool `json:"job,omitempty"`
	// ConfigFiles e o compose que criou o container (label
	// com.docker.compose.project.config_files): diz se o container e do cais
	// ou de um compose solto com o mesmo nome de projeto. Fica no daemon.
	ConfigFiles string `json:"-"`
}

// Done diz que o container e uma tarefa que ja terminou bem (migracao,
// download inicial): outro service do projeto espera ele com
// service_completed_successfully e ele saiu com 0. Nao conta como fora do
// ar. Saida 0 sozinha nao basta: um app parado pelo Parar tambem sai com 0.
func (c Container) Done() bool { return c.Job && c.State == "exited" && c.ExitCode == 0 }

// ComposePS lista os containers do projeto do arquivo, inclusive parados.
func ComposePS(ctx context.Context, file string) ([]Container, error) {
	out, err := execRunner(ctx, "docker", "compose", "-f", file, "ps", "-a", "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("docker compose ps (%s): %w", file, tail(err, 5))
	}
	return parsePS(out)
}

// parsePS aceita um JSON por linha (compose atual) ou uma lista JSON
// (versoes antigas).
func parsePS(out string) ([]Container, error) {
	type raw struct {
		Name, Service, Image, State, Health, Status, Labels string
		ExitCode                                            int
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
	// tarefas: os services que outro espera com service_completed_successfully
	// (a label do compose: "svc:condicao:restart,svc:condicao:restart")
	jobs := map[string]bool{}
	for _, r := range rows {
		for _, dep := range strings.Split(parseLabels(r.Labels)["com.docker.compose.depends_on"], ",") {
			if svc, rest, ok := strings.Cut(dep, ":"); ok && strings.HasPrefix(rest, "service_completed_successfully") {
				jobs[svc] = true
			}
		}
	}
	cs := make([]Container, 0, len(rows))
	for _, r := range rows {
		cs = append(cs, Container{Name: r.Name, Service: r.Service, Image: r.Image, State: r.State,
			Health: r.Health, Status: r.Status, ExitCode: r.ExitCode, URLs: traefikURLs(r.Labels), Job: jobs[r.Service],
			ConfigFiles: parseLabels(r.Labels)["com.docker.compose.project.config_files"]})
	}
	return cs, nil
}

// Exec roda um comando num container e devolve a saida padrao.
func Exec(ctx context.Context, container string, args ...string) (string, error) {
	out, err := execRunner(ctx, "docker", append([]string{"exec", container}, args...)...)
	if err != nil {
		return "", fmt.Errorf("docker exec %s %s: %w", container, strings.Join(args, " "), tail(err, 5))
	}
	return out, nil
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
		return fmt.Errorf("docker compose up (%s): %w", p.Name, tail(err, 15))
	}
	return nil
}

// Config devolve o compose resolvido (interpolado, com ancoras e extends
// aplicados) em JSON, como o docker compose vai usar.
func (CLI) Config(ctx context.Context, p Project) ([]byte, error) {
	out, err := execRunner(ctx, "docker", p.args("config", "--format", "json", "--no-env-resolution")...)
	if err != nil {
		return nil, fmt.Errorf("o compose do app tem erro (%s): %w", p.Name, tail(err, 10))
	}
	return []byte(out), nil
}

// Build constroi as imagens dos services com build. Nao mexe nos
// containers no ar: o app continua respondendo durante o build. A saida
// (passos do Dockerfile, em texto) vai pra out enquanto roda; o fim dela
// vai tambem no erro.
func (CLI) Build(ctx context.Context, p Project, out io.Writer) error {
	cmd := command(ctx, "docker", p.args("build", "--pull")...)
	// sem terminal o buildkit ja imprime em texto; plain garante uma linha
	// por passo, sem redesenhar a tela
	cmd.Env = append(os.Environ(), "BUILDKIT_PROGRESS=plain")
	last := &tailBuffer{limit: stderrLimit}
	var w io.Writer = last
	if out != nil {
		w = io.MultiWriter(last, out)
	}
	cmd.Stdout, cmd.Stderr = w, w
	if err := run(ctx, cmd); err != nil {
		err = &CmdError{Args: cmd.Args, Stderr: strings.TrimSpace(last.String()), Err: err}
		return fmt.Errorf("docker compose build (%s): %w", p.Name, tail(err, 20))
	}
	return nil
}

// PS lista os containers do projeto, inclusive parados.
func (CLI) PS(ctx context.Context, p Project) ([]Container, error) {
	out, err := execRunner(ctx, "docker", p.args("ps", "-a", "--format", "json")...)
	if err != nil {
		return nil, fmt.Errorf("docker compose ps (%s): %w", p.Name, tail(err, 5))
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
		return fmt.Errorf("docker compose restart (%s): %w", p.Name, tail(err, 5))
	}
	return nil
}

// Stop para os containers do projeto sem remover nada.
func (CLI) Stop(ctx context.Context, p Project) error {
	if _, err := execRunner(ctx, "docker", p.args("stop")...); err != nil {
		return fmt.Errorf("docker compose stop (%s): %w", p.Name, tail(err, 5))
	}
	return nil
}

// Down remove o projeto pelo nome (containers, redes e as imagens que o
// build dele criou), sem precisar do compose: funciona com o arquivo
// quebrado ou ja apagado. Com volumes, apaga tambem os volumes (os dados).
func (CLI) Down(ctx context.Context, name string, volumes bool) error {
	args := []string{"compose", "-p", name, "down", "--remove-orphans"}
	if volumes {
		args = append(args, "--volumes")
	}
	if _, err := execRunner(ctx, "docker", args...); err != nil {
		return fmt.Errorf("docker compose down (%s): %w", name, tail(err, 5))
	}
	out, err := execRunner(ctx, "docker", "image", "ls", "-q", "--filter", "label=com.docker.compose.project="+name)
	if err != nil || strings.TrimSpace(out) == "" {
		return nil // imagem que sobrar nao impede a exclusao
	}
	execRunner(ctx, "docker", append([]string{"image", "rm", "-f"}, strings.Fields(out)...)...)
	return nil
}

// ExecInput roda um comando num container mandando input pelo stdin. Use
// pra qualquer coisa com senha (SQL de criar usuario): pela linha de
// comando ela apareceria no ps do host enquanto o comando roda.
func ExecInput(ctx context.Context, container string, input []byte, args ...string) (string, error) {
	cmd := command(ctx, "docker", append([]string{"exec", "-i", container}, args...)...)
	cmd.Stdin = bytes.NewReader(input)
	var stdout bytes.Buffer
	stderr := &tailBuffer{limit: stderrLimit}
	cmd.Stdout, cmd.Stderr = &stdout, stderr
	if err := run(ctx, cmd); err != nil {
		err = &CmdError{Args: cmd.Args, Stderr: strings.TrimSpace(stderr.String()), Err: err}
		return "", fmt.Errorf("docker exec %s %s: %w", container, args[0], err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// ExecStream roda um comando num container ligando stdin e stdout direto
// (sem passar pela memoria): dump e restore de banco. stdin pode ser nil.
func ExecStream(ctx context.Context, container string, stdin io.Reader, stdout io.Writer, args ...string) error {
	flags := []string{"exec"}
	if stdin != nil {
		flags = append(flags, "-i")
	}
	cmd := command(ctx, "docker", append(append(flags, container), args...)...)
	cmd.Stdin, cmd.Stdout = stdin, stdout
	stderr := &tailBuffer{limit: stderrLimit}
	cmd.Stderr = stderr
	if err := run(ctx, cmd); err != nil {
		err = &CmdError{Args: cmd.Args, Stderr: strings.TrimSpace(stderr.String()), Err: err}
		return fmt.Errorf("docker exec %s %s: %w", container, args[0], tail(err, 5))
	}
	return nil
}
