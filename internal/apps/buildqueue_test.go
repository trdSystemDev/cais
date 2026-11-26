package apps

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/trdSystemDev/cais/internal/docker"
)

// gatedDocker e o docker de um app cujo build so termina quando o gate
// fecha; log e compartilhado entre os apps (ordem dos builds).
type gatedDocker struct {
	*fakeDocker
	app  string
	gate chan struct{}
	mu   *sync.Mutex
	log  *[]string
}

func (g gatedDocker) note(s string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	*g.log = append(*g.log, s)
}

func (g gatedDocker) Build(ctx context.Context, p docker.Project, out io.Writer) error {
	g.note("inicio " + g.app)
	<-g.gate
	g.note("fim " + g.app)
	return g.fakeDocker.Build(ctx, p, out)
}

// dois apps fazendo deploy juntos: o segundo build espera o primeiro
// terminar, o estado fica sabendo da espera e ela nao conta no limite do
// deploy
func TestBuildQueueUmBuildPorVez(t *testing.T) {
	q := NewBuildQueue(1)
	var mu sync.Mutex
	var log, waits []string
	waited := make(chan struct{})
	q.Waiting = func(app string, on bool) {
		mu.Lock()
		defer mu.Unlock()
		if on {
			waits = append(waits, "espera "+app)
			close(waited)
		} else {
			waits = append(waits, "sai "+app)
		}
	}

	setup := func(app string, gate chan struct{}) Manager {
		m, _, d := newManager(t)
		m.Docker = gatedDocker{fakeDocker: d, app: app, gate: gate, mu: &mu, log: &log}
		m.Builds = q
		if _, err := m.Create(App{Name: app, Repo: "o/" + app}); err != nil {
			t.Fatal(err)
		}
		return m
	}
	gateA, gateB := make(chan struct{}), make(chan struct{})
	close(gateB)
	ma, mb := setup("a", gateA), setup("b", gateB)
	mb.Timeout = 50 * time.Millisecond

	errs := make(chan error, 2)
	go func() { _, err := ma.Deploy(context.Background(), "a", "c1"); errs <- err }()
	for {
		mu.Lock()
		started := slices.Contains(log, "inicio a")
		mu.Unlock()
		if started {
			break
		}
		time.Sleep(time.Millisecond)
	}
	go func() { _, err := mb.Deploy(context.Background(), "b", "c1"); errs <- err }()
	select {
	case <-waited:
	case <-time.After(2 * time.Second):
		t.Fatal("o deploy de b nao entrou na fila")
	}
	time.Sleep(150 * time.Millisecond) // mais que o limite de b: a espera nao conta
	close(gateA)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("deploy: %v", err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if want := []string{"inicio a", "fim a", "inicio b", "fim b"}; !slices.Equal(log, want) {
		t.Errorf("builds %v, queria %v", log, want)
	}
	if want := []string{"espera b", "sai b"}; !slices.Equal(waits, want) {
		t.Errorf("avisos da fila %v, queria %v", waits, want)
	}
	if b, _ := mb.Get("b"); b.Commit != "c1" {
		t.Errorf("b nao subiu: %+v", b.Last)
	}
}

// o limite volta a contar depois da fila: um build travado depois da
// espera ainda e cancelado
func TestBuildQueueLimiteContaDepoisDaEspera(t *testing.T) {
	q := NewBuildQueue(1)
	release, err := q.acquire(context.Background(), "outro", func() {}, func() {})
	if err != nil {
		t.Fatal(err)
	}
	m, _, d := newManager(t)
	m.Builds, m.Timeout = q, 50*time.Millisecond
	m.Create(App{Name: "web", Repo: "o/web"})
	d.hang = map[string]bool{"build v1": true}
	go func() { time.Sleep(100 * time.Millisecond); release() }()

	_, err = m.Deploy(context.Background(), "web", "c1")
	var te *TimeoutError
	if err == nil || !errors.As(err, &te) {
		t.Fatalf("erro %v, queria o limite", err)
	}
}
