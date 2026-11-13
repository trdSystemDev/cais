package docker

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// rolloutPause e quanto o rollout espera, com os novos ja healthy, antes de
// parar os antigos: o Traefik junta as mudancas do Docker a cada 2s
// (providersThrottleDuration), e parar os antigos antes de ele ver os novos
// deixa o service sem nenhum container por alguns segundos.
var rolloutPause = 3 * time.Second

// Rollout troca os containers de um service sem deixar o service fora do
// ar: sobe os novos ao lado dos antigos (mesmo projeto, mesmos labels, o
// Traefik reparte entre eles), espera os novos ficarem healthy e so entao
// para e remove os antigos. image e o nome da imagem do service (a do
// image: ou <projeto>-<service> do build).
//
// Sem container rodando (primeiro deploy, app parado) ou com o service
// igual ao que ja esta no ar (mesmo commit), nao faz nada: o Up que vem
// depois cuida. Se os novos nao ficam healthy, eles sao removidos e os
// antigos seguem no ar sem terem sido tocados.
//
// So serve pra service sem container_name e sem ports (o cais ja recusa
// os dois) e com healthcheck: sem ele o Traefik mandaria trafego pro
// container novo antes de ele estar pronto.
func (CLI) Rollout(ctx context.Context, p Project, service, image string) error {
	return rollout(ctx, execRunner, p, service, image)
}

func rollout(ctx context.Context, run runner, p Project, service, image string) error {
	fail := func(step string, err error) error {
		return fmt.Errorf("troca sem queda de %s (%s): %s: %w", service, p.Name, step, lastLines(err.Error(), 15))
	}
	old, err := serviceIDs(ctx, run, p, service, true)
	if err != nil {
		return fail("compose ps", err)
	}
	if len(old) == 0 {
		return nil
	}
	changed, err := serviceChanged(ctx, run, p, service, image, old)
	if err != nil {
		return fail("comparar com o no ar", err)
	}
	if !changed {
		return nil
	}

	// --no-recreate: os antigos (e as dependencias) ficam como estao; o
	// --scale cria os novos com a imagem e o .env novos
	scale := service + "=" + strconv.Itoa(2*len(old))
	if _, err := run(ctx, "docker", p.args("up", "-d", "--wait", "--wait-timeout", "300", "--no-recreate", "--scale", scale, service)...); err != nil {
		// tira so os novos; os antigos nem foram tocados
		if now, perr := serviceIDs(ctx, run, p, service, false); perr == nil {
			var added []string
			for _, id := range now {
				if !slices.Contains(old, id) {
					added = append(added, id)
				}
			}
			if len(added) > 0 {
				run(ctx, "docker", append([]string{"rm", "-f"}, added...)...)
			}
		}
		return fail("subir os novos", err)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(rolloutPause):
	}

	// docker stop respeita o stop_grace_period do compose (o SIGTERM deixa
	// o app fechar as conexoes)
	if _, err := run(ctx, "docker", append([]string{"stop"}, old...)...); err != nil {
		return fail("parar os antigos", err)
	}
	if _, err := run(ctx, "docker", append([]string{"rm"}, old...)...); err != nil {
		return fail("remover os antigos", err)
	}
	return nil
}

// serviceIDs devolve os IDs dos containers do service; running deixa so os
// que estao rodando.
func serviceIDs(ctx context.Context, run runner, p Project, service string, running bool) ([]string, error) {
	cmd := []string{"ps", "-q"}
	if running {
		cmd = append(cmd, "--status", "running")
	} else {
		cmd = append(cmd, "-a")
	}
	out, err := run(ctx, "docker", p.args(append(cmd, service)...)...)
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

// serviceChanged diz se o compose recriaria os containers: config do
// service (hash que o compose grava no label) ou imagem diferente.
func serviceChanged(ctx context.Context, run runner, p Project, service, image string, ids []string) (bool, error) {
	out, err := run(ctx, "docker", p.args("config", "--hash", service)...)
	if err != nil {
		return false, err
	}
	f := strings.Fields(out)
	if len(f) != 2 || f[0] != service {
		return false, fmt.Errorf("saida inesperada do config --hash: %q", strings.TrimSpace(out))
	}
	hash := f[1]
	img, err := run(ctx, "docker", "image", "inspect", "-f", "{{.Id}}", image)
	if err != nil {
		return true, nil // imagem que ainda nao existe: o up baixa, e mudou
	}
	img = strings.TrimSpace(img)
	out, err = run(ctx, "docker", append([]string{"inspect", "-f", `{{index .Config.Labels "com.docker.compose.config-hash"}} {{.Image}}`}, ids...)...)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if f := strings.Fields(line); len(f) != 2 || f[0] != hash || f[1] != img {
			return true, nil
		}
	}
	return false, nil
}
