package core

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/trdSystemDev/cais/internal/docker"
	"github.com/trdSystemDev/cais/internal/host"
)

var fixedNow = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func newTestCore(check func(context.Context) docker.Status) *Core {
	return New(Deps{CheckDocker: check, Now: func() time.Time { return fixedNow }, CheckHost: func(context.Context) []host.Item {
		return []host.Item{{ID: "firewall", Title: "Firewall (ufw)", Detail: "desligado"}}
	}})
}

func okStatus(context.Context) docker.Status {
	return docker.Status{ClientVersion: "29.0.1", ServerVersion: "29.0.1", ComposeVersion: "2.40.0"}
}

func recv(t *testing.T, ch <-chan State) State {
	t.Helper()
	select {
	case s, ok := <-ch:
		if !ok {
			t.Fatal("canal fechado inesperadamente")
		}
		return s
	case <-time.After(2 * time.Second):
		t.Fatal("nenhum estado recebido")
	}
	return State{}
}

func TestCheckEnvironmentAtualizaEDevolve(t *testing.T) {
	c := newTestCore(okStatus)

	env, _ := c.CheckEnvironment(context.Background())
	if !env.Ready() || env.Checking {
		t.Fatalf("esperava pronto e sem checagem em andamento: %+v", env)
	}
	if env.Docker.CLI.Version != "29.0.1" || env.Docker.Compose.Version != "2.40.0" {
		t.Fatalf("versoes erradas: %+v", env.Docker)
	}
	if !env.CheckedAt.Equal(fixedNow) {
		t.Fatalf("CheckedAt = %v", env.CheckedAt)
	}
	if len(env.Security) != 1 || env.Security[0].ID != "firewall" {
		t.Fatalf("seguranca: %+v", env.Security)
	}
	if got := snap(t, c).Environment; !reflect.DeepEqual(got, env) {
		t.Fatalf("Snapshot diverge do retorno:\n%+v\n%+v", got, env)
	}
}

func TestSubscribePublicaCheckingEResultado(t *testing.T) {
	release := make(chan struct{})
	c := newTestCore(func(ctx context.Context) docker.Status {
		<-release
		return okStatus(ctx)
	})
	states, cancel, _ := c.Subscribe(context.Background())
	defer cancel()

	if s := recv(t, states); s.Version != 0 || s.Environment.Checked() {
		t.Fatalf("primeira foto deveria ser o estado inicial: %+v", s)
	}

	done := make(chan struct{})
	go func() { c.CheckEnvironment(context.Background()); close(done) }()

	if s := recv(t, states); !s.Environment.Checking {
		t.Fatalf("esperava Checking=true durante a checagem: %+v", s)
	}
	close(release)
	<-done
	if s := recv(t, states); s.Environment.Checking || !s.Environment.Ready() {
		t.Fatalf("esperava resultado final: %+v", s)
	}
}

func TestSubscribeLeitorLentoRecebeSoOUltimo(t *testing.T) {
	c := newTestCore(okStatus)
	states, cancel, _ := c.Subscribe(context.Background())
	defer cancel()

	// ninguem le enquanto 3 checagens (6 publicacoes) acontecem
	for i := 0; i < 3; i++ {
		c.CheckEnvironment(context.Background())
	}
	s := recv(t, states)
	if s.Version != 6 {
		t.Fatalf("esperava so a foto mais recente (versao 6), veio %d", s.Version)
	}
	select {
	case s := <-states:
		t.Fatalf("nao deveria haver foto antiga sobrando: %+v", s)
	default:
	}
}

func TestUnsubscribeFechaCanalEEIdempotente(t *testing.T) {
	c := newTestCore(okStatus)
	states, cancel, _ := c.Subscribe(context.Background())
	<-states
	cancel()
	cancel()
	if _, ok := <-states; ok {
		t.Fatal("canal deveria estar fechado")
	}
	// publicar depois de cancelar nao pode travar nem dar panic
	c.CheckEnvironment(context.Background())
}

func TestCheckingContaChamadasSimultaneas(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	c := newTestCore(func(ctx context.Context) docker.Status {
		started <- struct{}{}
		<-release
		return okStatus(ctx)
	})

	results := make(chan Environment, 2)
	for i := 0; i < 2; i++ {
		go func() { env, _ := c.CheckEnvironment(context.Background()); results <- env }()
	}
	<-started
	<-started

	release <- struct{}{}
	first := <-results
	if !first.Checking {
		t.Fatal("com outra checagem ainda rodando, Checking deveria continuar true")
	}
	release <- struct{}{}
	if last := <-results; last.Checking {
		t.Fatal("depois da ultima checagem, Checking deveria ser false")
	}
}

func TestDockerAusenteViraNaoInstalado(t *testing.T) {
	c := newTestCore(func(context.Context) docker.Status {
		return docker.Status{ClientErr: docker.ErrNotInstalled, ServerErr: docker.ErrNotInstalled, ComposeErr: docker.ErrNotInstalled}
	})
	env, _ := c.CheckEnvironment(context.Background())
	if env.Docker.Installed || env.Ready() {
		t.Fatalf("esperava nao instalado: %+v", env.Docker)
	}
}

// o estado vai ser devolvido pelo MCP: precisa virar JSON com os erros
// legiveis (error do Go vira "{}" em JSON, por isso o core guarda texto)
func TestEstadoSerializaEmJSON(t *testing.T) {
	c := newTestCore(func(context.Context) docker.Status {
		return docker.Status{
			ClientVersion:  "29.0.1",
			ServerErr:      errors.New("permission denied"),
			ComposeVersion: "2.40.0",
		}
	})
	c.CheckEnvironment(context.Background())

	out, err := json.Marshal(snap(t, c))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"daemon":{"error":"permission denied"}`, `"cli":{"version":"29.0.1"}`, `"checked_at":"2026-10-26T12:00:00Z"`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("JSON sem %s:\n%s", want, out)
		}
	}
}

func snap(t *testing.T, c *Core) State {
	t.Helper()
	s, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s
}
