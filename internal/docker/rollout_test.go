package docker

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// rolloutRunner simula o docker do rollout: containers do service antes e
// depois do up, hash e imagem atuais, e grava os comandos.
type rolloutRunner struct {
	calls   []string
	old     []string // rodando antes
	after   []string // todos depois do up --scale
	hash    string   // config --hash atual
	image   string   // ID da imagem atual
	running string   // "hash imagem" de cada container antigo
	failUp  bool
	// health devolvido a cada inspect de health (o ultimo se repete)
	health  []string
	noTouch bool // imagem sem touch
	// healthcheck dos antigos sem a drenagem (compose de antes dela)
	oldHC bool
	// respostas da API do Traefik, em ordem (a ultima se repete); vazio:
	// API fora do ar
	traefik []string
	ips     map[string]string // container -> IP na proxy_network
}

func (r *rolloutRunner) run(_ context.Context, name string, args ...string) (string, error) {
	cmd := strings.Join(args, " ")
	r.calls = append(r.calls, cmd)
	switch {
	case strings.Contains(cmd, "ps -q --status running web"):
		return strings.Join(r.old, "\n"), nil
	case strings.Contains(cmd, "ps -q -a web"):
		return strings.Join(r.after, "\n"), nil
	case strings.Contains(cmd, "config --hash web"):
		return "web " + r.hash + "\n", nil
	case strings.HasPrefix(cmd, "image inspect"):
		return r.image + "\n", nil
	case strings.HasPrefix(cmd, "exec proxy-traefik"):
		if len(r.traefik) == 0 {
			return "", errors.New("no such container: proxy-traefik")
		}
		out := r.traefik[0]
		if len(r.traefik) > 1 {
			r.traefik = r.traefik[1:]
		}
		return out, nil
	case strings.HasPrefix(cmd, "inspect -f {{with index .NetworkSettings"):
		var ips []string
		for _, id := range args[3:] {
			if ip, ok := r.ips[id]; ok {
				ips = append(ips, ip)
			}
		}
		return strings.Join(ips, "\n"), nil
	case strings.HasPrefix(cmd, "exec"):
		if r.noTouch {
			return "", errors.New("touch: executable file not found")
		}
	case strings.HasPrefix(cmd, "inspect -f {{json .Config.Healthcheck}}"):
		hc := `{"Test":["CMD-SHELL","test ! -f /tmp/drain \u0026\u0026 wget -qO- http://127.0.0.1:3000/"]}`
		if r.oldHC {
			hc = `{"Test":["CMD","wget","-qO-","http://127.0.0.1:3000/"]}`
		}
		var lines []string
		for range r.old {
			lines = append(lines, hc)
		}
		return strings.Join(lines, "\n"), nil
	case strings.HasPrefix(cmd, "inspect -f {{.State.Health.Status}}"):
		h := r.health[0]
		if len(r.health) > 1 {
			r.health = r.health[1:]
		}
		return h + "\n", nil
	case strings.HasPrefix(cmd, "inspect"):
		var lines []string
		for range r.old {
			lines = append(lines, r.running)
		}
		return strings.Join(lines, "\n"), nil
	case strings.Contains(cmd, " up "):
		if r.failUp {
			return "", errors.New("container app-web-2 is unhealthy")
		}
	}
	return "", nil
}

func init() { rolloutPause, drainPoll, traefikPoll = 0, 0, 0 }

var proj = Project{Name: "app", Files: []string{"/apps/app/src/compose.yml"}}

func TestRolloutSobeONovoAntesDeTirarOAntigo(t *testing.T) {
	r := &rolloutRunner{old: []string{"a1"}, hash: "h2", image: "i2", running: "h1 i1"}
	if err := rollout(context.Background(), r.run, proj, "web", "app-web", false); err != nil {
		t.Fatal(err)
	}
	var steps []string
	for _, c := range r.calls {
		switch {
		case strings.Contains(c, " up "):
			steps = append(steps, "up")
			if !strings.Contains(c, "--no-recreate --scale web=2 web") || !strings.Contains(c, "--wait") {
				t.Errorf("up %q", c)
			}
		case c == "stop a1", c == "rm a1":
			steps = append(steps, c)
		case strings.HasPrefix(c, "rm"), strings.HasPrefix(c, "stop"):
			t.Errorf("comando inesperado %q", c)
		}
	}
	if !slices.Equal(steps, []string{"up", "stop a1", "rm a1"}) {
		t.Errorf("ordem %v (chamadas %v)", steps, r.calls)
	}
}

func TestRolloutSemMudancaNaoTrocaNada(t *testing.T) {
	r := &rolloutRunner{old: []string{"a1"}, hash: "h1", image: "i1", running: "h1 i1"}
	if err := rollout(context.Background(), r.run, proj, "web", "app-web", false); err != nil {
		t.Fatal(err)
	}
	for _, c := range r.calls {
		if strings.Contains(c, " up ") || strings.HasPrefix(c, "stop") || strings.HasPrefix(c, "rm") {
			t.Errorf("mexeu no container igual: %q", c)
		}
	}
	// so a imagem mudou (rebuild com codigo novo, mesmo compose): troca
	r = &rolloutRunner{old: []string{"a1"}, hash: "h1", image: "i2", running: "h1 i1"}
	rollout(context.Background(), r.run, proj, "web", "app-web", false)
	if !slices.Contains(r.calls, "stop a1") {
		t.Errorf("imagem nova nao trocou: %v", r.calls)
	}
}

func TestRolloutSemContainerDeixaProUp(t *testing.T) {
	r := &rolloutRunner{}
	if err := rollout(context.Background(), r.run, proj, "web", "app-web", false); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 {
		t.Errorf("chamadas %v", r.calls)
	}
}

func TestRolloutNovoDoenteSaiEOAntigoFica(t *testing.T) {
	r := &rolloutRunner{old: []string{"a1"}, after: []string{"a1", "n2"}, hash: "h2", image: "i2", running: "h1 i1", failUp: true}
	err := rollout(context.Background(), r.run, proj, "web", "app-web", false)
	if err == nil || !strings.Contains(err.Error(), "unhealthy") {
		t.Fatalf("erro %v", err)
	}
	if !slices.Contains(r.calls, "rm -f n2") {
		t.Errorf("nao removeu o novo: %v", r.calls)
	}
	for _, c := range r.calls {
		if strings.Contains(c, "a1") && (strings.HasPrefix(c, "stop") || strings.HasPrefix(c, "rm")) {
			t.Errorf("mexeu no antigo: %q", c)
		}
	}
}

func steps(calls []string) []string {
	var out []string
	for _, c := range calls {
		switch {
		case strings.Contains(c, " up "):
			out = append(out, "up")
		case strings.HasPrefix(c, "exec proxy-traefik"):
			out = append(out, "traefik")
		case strings.HasPrefix(c, "exec"), strings.HasPrefix(c, "stop"), strings.HasPrefix(c, "rm"):
			out = append(out, c)
		case strings.HasPrefix(c, "inspect -f {{.State.Health.Status}}"):
			out = append(out, "health")
		}
	}
	return out
}

func TestRolloutComDrainTiraDoTraefikAntesDeParar(t *testing.T) {
	r := &rolloutRunner{old: []string{"a1"}, hash: "h2", image: "i2", running: "h1 i1", health: []string{"healthy", "healthy", "unhealthy"}}
	if err := rollout(context.Background(), r.run, proj, "web", "app-web", true); err != nil {
		t.Fatal(err)
	}
	want := []string{"up", "exec a1 touch /tmp/drain", "health", "health", "health", "stop a1", "rm a1"}
	if got := steps(r.calls); !slices.Equal(got, want) {
		t.Errorf("passos %v\nesperava %v", got, want)
	}
}

func TestRolloutDrainQueFalhaAindaTroca(t *testing.T) {
	r := &rolloutRunner{old: []string{"a1"}, hash: "h2", image: "i2", running: "h1 i1", noTouch: true}
	if err := rollout(context.Background(), r.run, proj, "web", "app-web", true); err != nil {
		t.Fatal(err)
	}
	if got := steps(r.calls); !slices.Equal(got, []string{"up", "exec a1 touch /tmp/drain", "stop a1", "rm a1"}) {
		t.Errorf("passos %v", got)
	}
	// healthcheck que nunca fica unhealthy: para no timeout
	old := drainTimeout
	drainTimeout = 0
	defer func() { drainTimeout = old }()
	r = &rolloutRunner{old: []string{"a1"}, hash: "h2", image: "i2", running: "h1 i1", health: []string{"healthy"}}
	if err := rollout(context.Background(), r.run, proj, "web", "app-web", true); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(r.calls, "stop a1") {
		t.Errorf("nao parou o antigo: %v", r.calls)
	}
}

// services da API do Traefik com os servidores (IPs) dados
func api(ips ...string) string {
	var servers []string
	for _, ip := range ips {
		servers = append(servers, `{"url":"http://`+ip+`:3000"}`)
	}
	return `[{"loadBalancer":{"servers":[` + strings.Join(servers, ",") + `]},"name":"app@docker"}]`
}

func TestRolloutEsperaOTraefikUsarONovoELargarOAntigo(t *testing.T) {
	r := &rolloutRunner{old: []string{"a1"}, after: []string{"a1", "n2"}, hash: "h2", image: "i2", running: "h1 i1",
		ips:    map[string]string{"a1": "172.18.0.1", "n2": "172.18.0.12"},
		health: []string{"unhealthy"},
		traefik: []string{
			api("172.18.0.1"),                // ainda nao viu o novo
			api("172.18.0.1", "172.18.0.12"), // os dois (172.18.0.1 nao casa com .12)
			api("172.18.0.1", "172.18.0.12"), // drenando: ainda manda pro antigo
			api("172.18.0.12"),               // largou o antigo
		}}
	if err := rollout(context.Background(), r.run, proj, "web", "app-web", true); err != nil {
		t.Fatal(err)
	}
	want := []string{"up", "traefik", "traefik", "exec a1 touch /tmp/drain", "health", "traefik", "traefik", "stop a1", "rm a1"}
	if got := steps(r.calls); !slices.Equal(got, want) {
		t.Errorf("passos %v\nesperava %v", got, want)
	}
}

func TestRolloutSemDrainNaoEsperaOTraefikLargar(t *testing.T) {
	r := &rolloutRunner{old: []string{"a1"}, after: []string{"a1", "n2"}, hash: "h2", image: "i2", running: "h1 i1",
		ips: map[string]string{"a1": "172.18.0.1", "n2": "172.18.0.12"}, traefik: []string{api("172.18.0.1", "172.18.0.12")}}
	if err := rollout(context.Background(), r.run, proj, "web", "app-web", false); err != nil {
		t.Fatal(err)
	}
	if got := steps(r.calls); !slices.Equal(got, []string{"up", "traefik", "stop a1", "rm a1"}) {
		t.Errorf("passos %v", got)
	}
}

func TestRolloutTraefikQueNaoLargaSegueNoTimeout(t *testing.T) {
	old := traefikTimeout
	traefikTimeout = 0
	defer func() { traefikTimeout = old }()
	r := &rolloutRunner{old: []string{"a1"}, after: []string{"a1", "n2"}, hash: "h2", image: "i2", running: "h1 i1",
		ips: map[string]string{"a1": "172.18.0.1", "n2": "172.18.0.12"}, health: []string{"unhealthy"},
		traefik: []string{api("172.18.0.1")}} // nunca ve o novo nem larga o antigo
	if err := rollout(context.Background(), r.run, proj, "web", "app-web", true); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(r.calls, "stop a1") {
		t.Errorf("nao trocou: %v", r.calls)
	}
}

func TestRolloutCanceladoNaoParaOAntigo(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &rolloutRunner{old: []string{"a1"}, after: []string{"a1", "n2"}, hash: "h2", image: "i2", running: "h1 i1",
		ips: map[string]string{"a1": "172.18.0.1", "n2": "172.18.0.12"}, traefik: []string{api("172.18.0.1")}}
	traefikPoll = time.Millisecond
	defer func() { traefikPoll = 0 }()
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if err := rollout(ctx, r.run, proj, "web", "app-web", false); err == nil {
		t.Fatal("cancelado e seguiu")
	}
	for _, c := range r.calls {
		if strings.HasPrefix(c, "stop") {
			t.Errorf("parou o antigo depois de cancelado: %v", r.calls)
		}
	}
}

func TestRolloutNaoDrenaAntigoComHealthcheckDeAntes(t *testing.T) {
	// o compose novo tem a drenagem, o container no ar ainda nao
	r := &rolloutRunner{old: []string{"a1"}, hash: "h2", image: "i2", running: "h1 i1", oldHC: true, health: []string{"healthy"}}
	if err := rollout(context.Background(), r.run, proj, "web", "app-web", true); err != nil {
		t.Fatal(err)
	}
	if got := steps(r.calls); !slices.Equal(got, []string{"up", "stop a1", "rm a1"}) {
		t.Errorf("passos %v", got)
	}
}
