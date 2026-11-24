package core

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trdSystemDev/cais/internal/host"
)

// hostFake e a VPS do lado do core: o harden manda dois passos e deixa o
// SSH pendente; o confirm e a volta mexem em hardened.
type hostFake struct {
	mu        sync.Mutex
	hardened  bool
	confirmed bool
	fixed     []string
	opts      host.Options
}

func newHostCore(f *hostFake, now *time.Time) *Core {
	return New(Deps{
		CheckDocker: okStatus,
		Now:         func() time.Time { return *now },
		CheckHost:   func(context.Context) []host.Item { return nil },
		HardenHost: func(_ context.Context, o host.Options, p func(host.Step)) (host.Result, error) {
			f.mu.Lock()
			f.opts, f.hardened = o, true
			f.mu.Unlock()
			p(host.Step{ID: "admin", Status: host.StepRunning})
			p(host.Step{ID: "admin", Status: host.StepChanged})
			p(host.Step{ID: "ssh", Status: host.StepChanged})
			return host.Result{PendingConfirm: true}, nil
		},
		FixHost: func(_ context.Context, id string) (string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.fixed = append(f.fixed, id)
			return "ok", nil
		},
		ConfirmSSH: func(context.Context) (bool, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.confirmed = true
			return true, nil
		},
		SSHHardened: func() bool {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.hardened
		},
	})
}

func TestHardenHostEConfirm(t *testing.T) {
	now := time.Date(2026, 11, 24, 15, 0, 0, 0, time.UTC)
	f := &hostFake{}
	c := newHostCore(f, &now)
	ctx := context.Background()

	before := now.Add(-time.Minute) // a sessao que pediu o harden
	if err := c.HardenHost(ctx, HardenRequest{User: "deploy", Password: "12345678"}); err != nil {
		t.Fatal(err)
	}
	st, _ := c.Snapshot(ctx)
	h := st.Hardening
	if h.Running || !h.Pending() || !h.RollbackAt.Equal(now.Add(host.RollbackDelay)) || len(h.Steps) != 2 || h.Steps[0].Status != host.StepChanged {
		t.Fatalf("estado: %+v", h)
	}
	if f.opts.User != "deploy" || f.opts.Password != "12345678" {
		t.Errorf("opcoes: %+v", f.opts)
	}

	// a sessao antiga nao confirma: nao prova que o login novo funciona
	if err := c.ConfirmHost(ctx, before); err == nil || !strings.Contains(err.Error(), "abra outra") {
		t.Fatalf("confirm da sessao antiga: %v", err)
	}
	if f.confirmed {
		t.Fatal("cancelou a volta pela sessao antiga")
	}
	if err := c.ConfirmHost(ctx, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	st, _ = c.Snapshot(ctx)
	if st.Hardening.Pending() || !st.Hardening.Confirmed || !f.confirmed {
		t.Fatalf("depois do confirm: %+v", st.Hardening)
	}
	if err := c.ConfirmHost(ctx, now.Add(time.Second)); err == nil || !strings.Contains(err.Error(), "nada pendente") {
		t.Errorf("segundo confirm: %v", err)
	}
}

// ninguem confirmou: passada a hora, o estado mostra que o SSH voltou
func TestHardenHostVoltaSozinho(t *testing.T) {
	old := rollbackGrace
	rollbackGrace = 0
	t.Cleanup(func() { rollbackGrace = old })
	now := time.Now()
	f := &hostFake{}
	c := newHostCore(f, &now)
	c.deps.RollbackDelay = 50 * time.Millisecond
	if err := c.HardenHost(context.Background(), HardenRequest{User: "deploy", Password: "12345678"}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.hardened = false // o timer do systemd tirou a config
	f.mu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	for {
		st, _ := c.Snapshot(context.Background())
		if st.Hardening.RolledBack && !st.Hardening.Pending() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("nao marcou a volta: %+v", st.Hardening)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestFixHostItemEOcupado(t *testing.T) {
	now := time.Now()
	f := &hostFake{}
	c := newHostCore(f, &now)
	if d, err := c.FixHostItem(context.Background(), "firewall"); err != nil || d != "ok" || f.fixed[0] != "firewall" {
		t.Fatalf("%q %v %v", d, err, f.fixed)
	}
	c.infra.Lock()
	_, err := c.FixHostItem(context.Background(), "fail2ban")
	err2 := c.HardenHost(context.Background(), HardenRequest{User: "deploy"})
	c.infra.Unlock()
	if !errors.Is(err, ErrBusy) || !errors.Is(err2, ErrBusy) {
		t.Errorf("com o Setup rodando: %v %v", err, err2)
	}
}
