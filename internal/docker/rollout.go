package docker

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// O Traefik do proxy: o rollout pergunta a API dele (presa no loopback do
// container, sem porta publicada) quais containers ele usa, pela rede por
// onde ele alcanca os apps.
var (
	TraefikContainer = "proxy-traefik"
	TraefikNetwork   = "proxy_network"
	traefikAPI       = "http://127.0.0.1:8080/api/http/services?per_page=10000"
	// traefikTimeout e o maximo que o rollout espera o Traefik passar a usar
	// os novos ou largar os antigos. O Traefik relista o Docker a cada
	// evento, um de cada vez: com o Docker lento chega a ficar 10-15s atras.
	traefikTimeout = 2 * time.Minute
	traefikPoll    = 300 * time.Millisecond
)

// rolloutPause e a espera fixa no lugar da confirmacao quando a API do
// Traefik nao responde (proxy de antes da API, Traefik fora do ar): o
// Traefik costuma levar 1-2s pra ver uma mudanca do Docker.
var rolloutPause = 3 * time.Second

// Rollout troca os containers de um service sem deixar o service fora do
// ar: sobe os novos ao lado dos antigos (mesmo projeto, mesmos labels, o
// Traefik reparte entre eles), espera os novos ficarem healthy e o Traefik
// passar a usa-los, e so entao para e remove os antigos. image e o nome da imagem do service (a do
// image: ou <projeto>-<service> do build).
//
// Com drain, o service segue a convencao de drenagem: o healthcheck falha
// quando /tmp/drain existe. Antes de parar os antigos, o rollout cria esse
// arquivo neles e espera o Traefik larga-los (ele tira do balanceamento o
// container unhealthy enquanto ele ainda esta vivo), entao nenhuma
// requisicao vai pra um container que ja parou. Sem drain o Traefik so tira
// o antigo depois que ele morre, e as requisicoes ate ele relistar o Docker
// vao pro IP que sumiu e ficam presas.
//
// Sem container rodando (primeiro deploy, app parado) ou com o service
// igual ao que ja esta no ar (mesmo commit), nao faz nada: o Up que vem
// depois cuida. Se os novos nao ficam healthy, eles sao removidos e os
// antigos seguem no ar sem terem sido tocados.
//
// So serve pra service sem container_name e sem ports (o cais ja recusa
// os dois) e com healthcheck: sem ele o Traefik mandaria trafego pro
// container novo antes de ele estar pronto.
func (CLI) Rollout(ctx context.Context, p Project, service, image string, drain bool) error {
	return rollout(ctx, execRunner, p, service, image, drain)
}

// DrainFile e o arquivo que, existindo no container, faz o healthcheck de
// um service com drenagem falhar.
const DrainFile = "/tmp/drain"

// drainTimeout e o maximo que o rollout espera os antigos ficarem
// unhealthy (interval x retries do healthcheck). Passou disso, para mesmo
// assim: o novo ja esta no ar.
var drainTimeout = 2 * time.Minute

func rollout(ctx context.Context, run runner, p Project, service, image string, drain bool) error {
	fail := func(step string, err error) error {
		return fmt.Errorf("troca sem queda de %s (%s): %s: %w", service, p.Name, step, tail(err, 15))
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
	_, upErr := run(ctx, "docker", p.args("up", "-d", "--wait", "--wait-timeout", "300", "--no-recreate", "--scale", scale, service)...)
	var added []string
	if now, err := serviceIDs(ctx, run, p, service, false); err == nil {
		for _, id := range now {
			if !slices.Contains(old, id) {
				added = append(added, id)
			}
		}
	}
	if upErr != nil {
		// tira so os novos; os antigos nem foram tocados
		if len(added) > 0 {
			run(ctx, "docker", append([]string{"rm", "-f"}, added...)...)
		}
		return fail("subir os novos", upErr)
	}

	// os novos ja estao healthy; o Traefik leva um tempo pra ver
	if err := waitTraefik(ctx, run, added, true); err != nil {
		return err
	}
	// drenagem que falha (imagem sem touch, healthcheck que nao muda) nao
	// derruba o deploy: so volta pro corte sem drenar
	if drain && drainOld(ctx, run, old) {
		if err := waitTraefik(ctx, run, old, false); err != nil {
			return err
		}
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

// drainOld cria o DrainFile nos antigos e espera todos ficarem unhealthy.
// Devolve se conseguiu. So drena se o healthcheck dos proprios antigos usa
// o DrainFile: no primeiro deploy depois de por a drenagem no compose, o
// antigo ainda roda o healthcheck de antes e nunca ficaria unhealthy.
func drainOld(ctx context.Context, run runner, ids []string) bool {
	out, err := run(ctx, "docker", append([]string{"inspect", "-f", "{{json .Config.Healthcheck}}"}, ids...)...)
	if err != nil || strings.Count(out, DrainFile) < len(ids) {
		return false
	}
	for _, id := range ids {
		if _, err := run(ctx, "docker", "exec", id, "touch", DrainFile); err != nil {
			return false
		}
	}
	deadline := time.Now().Add(drainTimeout)
	for {
		out, err := run(ctx, "docker", append([]string{"inspect", "-f", "{{.State.Health.Status}}"}, ids...)...)
		if err != nil {
			return false
		}
		if strings.Count(out, "unhealthy") == len(ids) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		if sleep(ctx, drainPoll) != nil {
			return false
		}
	}
}

var drainPoll = 200 * time.Millisecond

// waitTraefik espera o Traefik usar (present) ou largar todos os
// containers, pelo IP deles na rede do proxy. Sem a API do Traefik, ou sem
// IP nessa rede, espera rolloutPause no lugar. Passado traefikTimeout,
// segue mesmo assim: so o ctx cancelado e erro.
func waitTraefik(ctx context.Context, run runner, ids []string, present bool) error {
	ips, ok := proxyIPs(ctx, run, ids)
	deadline := time.Now().Add(traefikTimeout)
	for ok {
		out, err := run(ctx, "docker", "exec", TraefikContainer, "wget", "-qO-", traefikAPI)
		if err != nil {
			break
		}
		done := true
		for _, ip := range ips {
			if strings.Contains(out, "//"+ip+":") != present {
				done = false
			}
		}
		if done || time.Now().After(deadline) {
			return ctx.Err()
		}
		if err := sleep(ctx, traefikPoll); err != nil {
			return err
		}
	}
	return sleep(ctx, rolloutPause)
}

// proxyIPs devolve o IP de cada container na rede do proxy.
func proxyIPs(ctx context.Context, run runner, ids []string) ([]string, bool) {
	if len(ids) == 0 {
		return nil, false
	}
	format := `{{with index .NetworkSettings.Networks "` + TraefikNetwork + `"}}{{.IPAddress}}{{end}}`
	out, err := run(ctx, "docker", append([]string{"inspect", "-f", format}, ids...)...)
	if err != nil {
		return nil, false
	}
	ips := strings.Fields(out)
	return ips, len(ips) == len(ids)
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
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

// traefikMetrics e o endereco das metricas do Traefik (o mesmo da API, so
// no loopback do container).
const traefikMetrics = "http://127.0.0.1:8080/metrics"

// TraefikMetrics le as metricas do Traefik (formato do Prometheus) por
// docker exec: nenhuma porta publicada.
func TraefikMetrics(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return execRunner(ctx, "docker", "exec", TraefikContainer, "wget", "-qO-", traefikMetrics)
}
