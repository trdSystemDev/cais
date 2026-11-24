package core

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Traffic sao as requisicoes que passam pelo proxy (Traefik): a taxa
// agora, o historico e quem recebe mais. Vem dos contadores do Traefik,
// lidos por docker exec; a taxa e a diferenca entre duas leituras.
type Traffic struct {
	At time.Time `json:"at,omitzero"`
	// Rate sao as requisicoes por segundo desde a leitura anterior (-1 na
	// primeira: precisa de duas).
	Rate float64 `json:"rate"`
	// Errors sao as respostas 5xx por segundo, no mesmo intervalo.
	Errors float64 `json:"errors"`
	// History e a taxa em cada leitura, da mais velha pra mais nova.
	History []float64 `json:"history,omitempty"`
	// Total sao as requisicoes desde que o proxy subiu.
	Total float64 `json:"total"`
	// Services e a taxa por service do Traefik (o router do app), da maior
	// pra menor; so os que tiveram requisicao no intervalo.
	Services []ServiceTraffic `json:"services,omitempty"`
	// Error e por que nao deu pra ler (proxy desligado, metricas ainda
	// nao aplicadas); o resto do dashboard continua.
	Error string `json:"error,omitempty"`
}

// ServiceTraffic e a taxa de um service do Traefik.
type ServiceTraffic struct {
	Name   string  `json:"name"`
	Rate   float64 `json:"rate"`
	Errors float64 `json:"errors"`
}

// trafficMinGap e o intervalo minimo entre leituras (cada uma e um docker
// exec); trafficMaxGap, a partir de quando o historico recomeca (o
// dashboard ficou fechado).
const (
	trafficMinGap = 4 * time.Second
	trafficMaxGap = 30 * time.Second
)

type trafficRuntime struct {
	mu   sync.Mutex
	last Traffic
	prev counters
}

// counters sao os totais de uma leitura: requisicoes e 5xx, no geral e por
// service.
type counters struct {
	at                time.Time
	total, errors     float64
	service, svcError map[string]float64
}

// TrafficStats le os contadores do proxy e devolve a taxa com o historico.
// Como o uso da maquina, so le quando alguem pede (o dashboard aberto).
func (c *Core) TrafficStats(ctx context.Context) (Traffic, error) {
	r := &c.traffic
	r.mu.Lock()
	defer r.mu.Unlock()
	now := c.deps.Now()
	if !r.last.At.IsZero() && now.Sub(r.last.At) < trafficMinGap {
		return r.copy(), nil
	}
	text, err := c.deps.TraefikMetrics(ctx)
	if err != nil {
		r.last, r.prev = Traffic{At: now, Rate: -1, Error: trafficError(err)}, counters{}
		return r.copy(), nil
	}
	cur := parseTraefik(text)
	cur.at = now
	if cur.service == nil {
		r.last, r.prev = Traffic{At: now, Rate: -1, Error: "o proxy ainda nao conta as requisicoes: aplique de novo na aba Infra (proxy > Aplicar)"}, counters{}
		return r.copy(), nil
	}
	t := Traffic{At: now, Rate: -1, Total: cur.total}
	p := r.prev
	dt := now.Sub(p.at).Seconds()
	// o contador volta a zero quando o proxy reinicia: sem taxa nessa
	if !p.at.IsZero() && now.Sub(p.at) < trafficMaxGap && dt > 0 && cur.total >= p.total {
		t.Rate = (cur.total - p.total) / dt
		t.Errors = max(0, cur.errors-p.errors) / dt
		t.History = appendCapped(r.last.History, t.Rate)
		for name, n := range cur.service {
			if d := n - p.service[name]; d > 0 {
				t.Services = append(t.Services, ServiceTraffic{Name: name, Rate: d / dt,
					Errors: max(0, cur.svcError[name]-p.svcError[name]) / dt})
			}
		}
		slices.SortFunc(t.Services, func(a, b ServiceTraffic) int {
			if a.Rate != b.Rate {
				if a.Rate > b.Rate {
					return -1
				}
				return 1
			}
			return strings.Compare(a.Name, b.Name)
		})
	}
	r.last, r.prev = t, cur
	return r.copy(), nil
}

func (r *trafficRuntime) copy() Traffic {
	out := r.last
	out.History = append([]float64(nil), r.last.History...)
	out.Services = append([]ServiceTraffic(nil), r.last.Services...)
	return out
}

func trafficError(err error) string {
	msg := firstLineErr(err)
	switch {
	case strings.Contains(msg, "No such container"), strings.Contains(msg, "is not running"):
		return "o proxy nao esta no ar"
	case strings.Contains(msg, "404"):
		return "o proxy ainda nao conta as requisicoes: aplique de novo na aba Infra (proxy > Aplicar)"
	}
	return msg
}

// parseTraefik soma os contadores do formato do Prometheus: as requisicoes
// que entraram pelos entrypoints de fora (web, websecure; o "traefik" e a
// API, onde o proprio cais le isto) e as de cada service. service nil:
// nao sao as metricas do Traefik. Sem requisicao nenhuma ainda, o Traefik
// nem escreve os contadores: tudo zero.
func parseTraefik(text string) counters {
	var c counters
	if strings.Contains(text, "traefik_") {
		c.service, c.svcError = map[string]float64{}, map[string]float64{}
	}
	for _, line := range strings.Split(text, "\n") {
		if line == "" || line[0] == '#' {
			continue
		}
		name, rest, ok := strings.Cut(line, "{")
		if !ok {
			continue
		}
		labels, value, ok := strings.Cut(rest, "} ")
		if !ok {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			continue
		}
		l := promLabels(labels)
		server := strings.HasPrefix(l["code"], "5")
		switch name {
		case "traefik_entrypoint_requests_total":
			// o "web" (porta 80) so redireciona pro https: a mesma visita
			// contaria duas vezes
			if l["entrypoint"] == "traefik" || (l["entrypoint"] == "web" && strings.HasPrefix(l["code"], "3")) {
				continue
			}
			c.total += v
			if server {
				c.errors += v
			}
		case "traefik_service_requests_total":
			svc := strings.TrimSuffix(l["service"], "@docker")
			if strings.HasSuffix(svc, "@internal") {
				continue // api, ping, o redirecionamento pro https
			}
			c.service[svc] += v
			if server {
				c.svcError[svc] += v
			}
		}
	}
	return c
}

// promLabels le `a="1",b="2"` (sem aspas escapadas nos nomes que importam).
func promLabels(s string) map[string]string {
	out := map[string]string{}
	for s != "" {
		k, rest, ok := strings.Cut(s, `="`)
		if !ok {
			break
		}
		v, rest, _ := strings.Cut(rest, `"`)
		out[strings.TrimLeft(k, ", ")] = v
		s = strings.TrimPrefix(rest, ",")
	}
	return out
}
