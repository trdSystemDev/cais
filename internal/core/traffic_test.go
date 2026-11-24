package core

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"
)

func metrics(web, webErr, loja, lojaErr, blog float64) string {
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	return `# HELP traefik_entrypoint_requests_total How many HTTP requests
# TYPE traefik_entrypoint_requests_total counter
traefik_entrypoint_requests_total{code="200",entrypoint="websecure",method="GET",protocol="http"} ` + f(web) + `
traefik_entrypoint_requests_total{code="502",entrypoint="websecure",method="GET",protocol="http"} ` + f(webErr) + `
traefik_entrypoint_requests_total{code="200",entrypoint="traefik",method="GET",protocol="http"} 999
traefik_entrypoint_requests_total{code="308",entrypoint="web",method="GET",protocol="http"} ` + f(web) + `
traefik_service_requests_total{code="200",method="GET",protocol="http",service="loja@docker"} ` + f(loja) + `
traefik_service_requests_total{code="502",method="GET",protocol="http",service="loja@docker"} ` + f(lojaErr) + `
traefik_service_requests_total{code="200",method="GET",protocol="http",service="blog@docker"} ` + f(blog) + `
traefik_service_requests_total{code="200",method="GET",protocol="http",service="api@internal"} 50
`
}

func TestTrafficStats(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	text := metrics(100, 0, 60, 0, 40)
	var fail error
	c := New(Deps{CheckDocker: okStatus, Now: func() time.Time { return now },
		TraefikMetrics: func(context.Context) (string, error) { return text, fail }})
	ctx := context.Background()

	tr, _ := c.TrafficStats(ctx)
	if tr.Rate != -1 || tr.Total != 100 || tr.Error != "" {
		t.Fatalf("primeira leitura: %+v", tr)
	}
	// 10s depois: 200 requisicoes a mais (10 com erro), loja 150 e blog 40
	now = now.Add(10 * time.Second)
	text = metrics(290, 10, 200, 10, 80)
	tr, _ = c.TrafficStats(ctx)
	if tr.Rate != 20 || tr.Errors != 1 || len(tr.History) != 1 || tr.Total != 300 {
		t.Fatalf("taxa: %+v", tr)
	}
	if len(tr.Services) != 2 || tr.Services[0].Name != "loja" || tr.Services[0].Rate != 15 || tr.Services[0].Errors != 1 || tr.Services[1].Rate != 4 {
		t.Fatalf("services: %+v", tr.Services)
	}
	// antes do intervalo minimo: a mesma leitura
	now = now.Add(time.Second)
	if again, _ := c.TrafficStats(ctx); again.At != tr.At {
		t.Error("leu de novo antes do intervalo")
	}
	// o proxy reiniciou: contador menor, sem taxa
	now = now.Add(5 * time.Second)
	text = metrics(5, 0, 5, 0, 0)
	if tr, _ = c.TrafficStats(ctx); tr.Rate != -1 {
		t.Errorf("depois do reinicio: %+v", tr)
	}
	// proxy desligado e metricas desligadas
	now = now.Add(5 * time.Second)
	fail = errors.New("Error response from daemon: No such container: proxy-traefik")
	if tr, _ = c.TrafficStats(ctx); tr.Error != "o proxy nao esta no ar" {
		t.Errorf("desligado: %+v", tr)
	}
	now = now.Add(5 * time.Second)
	fail = errors.New("wget: server returned error: HTTP/1.1 404 Not Found")
	if tr, _ = c.TrafficStats(ctx); !strings.Contains(tr.Error, "Aplicar") {
		t.Errorf("sem metricas: %+v", tr)
	}
	// metricas ligadas sem requisicao ainda: zero, sem erro
	now = now.Add(5 * time.Second)
	fail, text = nil, "# HELP traefik_config_reloads_total x\ntraefik_config_reloads_total 1\n"
	if tr, _ = c.TrafficStats(ctx); tr.Error != "" || tr.Total != 0 || math.IsNaN(tr.Rate) {
		t.Errorf("sem requisicao: %+v", tr)
	}
}
