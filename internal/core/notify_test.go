package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/config"
)

// fakeBrevo guarda os e-mails recebidos.
type fakeBrevo struct {
	mu    sync.Mutex
	mails []string // assunto + "\n" + texto
}

// withBrevo liga os avisos no core com uma Brevo falsa.
func withBrevo(t *testing.T, c *Core) *fakeBrevo {
	t.Helper()
	f := &fakeBrevo{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("api-key") != "xkeysib-teste" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"code":"unauthorized","message":"Key not found"}`))
			return
		}
		var m struct {
			Subject     string `json:"subject"`
			TextContent string `json:"textContent"`
		}
		json.NewDecoder(r.Body).Decode(&m)
		f.mu.Lock()
		f.mails = append(f.mails, m.Subject+"\n"+m.TextContent)
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(srv.Close)
	c.deps.BrevoURL = srv.URL
	ctx := context.Background()
	if err := c.changeConfig(func(cfg *config.Config) {
		cfg.Notify = config.Notify{From: "cais@exemplo.com.br", To: []string{"ops@exemplo.com.br"}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.SetSecret(ctx, secretBrevoKey, "xkeysib-teste"); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fakeBrevo) list() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.mails...)
}

// wait espera chegarem n e-mails (e confere que nao passou disso).
func (f *fakeBrevo) wait(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(f.list()) < n && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	got := f.list()
	if len(got) != n {
		t.Fatalf("%d e-mails, esperava %d: %q", len(got), n, got)
	}
	return got
}

func TestTestNotify(t *testing.T) {
	c, _, _ := newAppsCore(t)
	ctx := context.Background()
	if err := c.TestNotify(ctx); err == nil || !strings.Contains(err.Error(), "desligados") {
		t.Fatalf("sem destinatarios: %v", err)
	}
	f := withBrevo(t, c)
	if err := c.TestNotify(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.wait(t, 1); !strings.HasPrefix(got[0], "[cais exemplo.com.br] Os avisos do cais estao chegando") {
		t.Errorf("assunto: %q", got[0])
	}

	c.SetSecret(ctx, secretBrevoKey, "xkeysib-errada")
	if err := c.TestNotify(ctx); err == nil || !strings.Contains(err.Error(), "Key not found") {
		t.Fatalf("chave errada: %v", err)
	}
}

func TestNotifyFromObrigatorio(t *testing.T) {
	cfg := config.Default()
	cfg.Domain, cfg.Email = "exemplo.com.br", "a@exemplo.com.br"
	cfg.Notify.To = []string{"ops@exemplo.com.br"}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "notify.from") {
		t.Fatalf("sem remetente: %v", err)
	}
	cfg.Notify.From, cfg.Notify.To = "cais@exemplo.com.br", []string{"nao-e-email"}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "notify.to") {
		t.Fatalf("destinatario invalido: %v", err)
	}
}

// falha passageira nao avisa enquanto ha nova tentativa marcada; so a
// ultima, que desiste do commit
func TestAvisoDeployAutomaticoSoQuandoDesiste(t *testing.T) {
	c, d, _ := newAppsCore(t)
	f := withBrevo(t, c)
	ctx := context.Background()
	var mu sync.Mutex
	now := fixedNow
	c.deps.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	after := func(dur time.Duration) { mu.Lock(); now = now.Add(dur); mu.Unlock() }
	c.CreateApp(ctx, apps.App{Name: "meu-app", Repo: "trdSystemDev/meu-app", AutoDeploy: true})

	round := func() {
		_, done := c.AutoDeployOnce(ctx)
		<-done
		for c.work.Count() > 0 { // o envio do aviso
			time.Sleep(time.Millisecond)
		}
	}
	d.mu.Lock()
	d.buildErr = errors.New("docker compose build (meu-app): toomanyrequests: You have reached your pull rate limit")
	d.mu.Unlock()

	for _, wait := range []time.Duration{0, 2 * time.Minute, 10 * time.Minute} {
		after(wait)
		round()
		if got := f.list(); len(got) != 0 {
			t.Fatalf("avisou com nova tentativa marcada: %q", got)
		}
	}
	after(30 * time.Minute)
	round()
	got := f.wait(t, 1)
	if !strings.Contains(got[0], "Deploy automatico de meu-app falhou") || !strings.Contains(got[0], "toomanyrequests") {
		t.Errorf("e-mail: %q", got[0])
	}
}

// backup que nao chega no Drive avisa uma vez; as novas tentativas de hora
// em hora nao repetem
func TestAvisoBackupUmaVezPorSequencia(t *testing.T) {
	c, s, _, _ := newBackupCore(t)
	setupBackup(t, c, s)
	f := withBrevo(t, c)
	uploadRetryDelay = 0
	defer func() { uploadRetryDelay = 5 * time.Second }()
	if err := c.SetBackupSchedule(context.Background(), "03:00", 30); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	now := time.Date(2026, 11, 13, 3, 0, 0, 0, time.Local)
	at3 := now
	clock := func(d time.Time) { mu.Lock(); now = d; mu.Unlock() }
	c.deps.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	s.SetDown("PUT /upload")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.RunBackups(ctx, 5*time.Millisecond)

	waitLast := func(at time.Time) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			st, _ := c.Snapshot(context.Background())
			if b := st.Backup; b.Last != nil && !b.Running && b.Last.At.Equal(at) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("backup das %s nao aconteceu", at.Format("15:04"))
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitLast(at3)
	if got := f.wait(t, 1); !strings.Contains(got[0], "Backup falhou") || !strings.Contains(got[0], "nao chegou no Google Drive") {
		t.Errorf("e-mail: %q", got[0])
	}

	clock(at3.Add(time.Hour))
	waitLast(at3.Add(time.Hour))
	time.Sleep(50 * time.Millisecond)
	f.wait(t, 1)
}

// backup que chega no Drive sem algum database: avisa toda vez (nao repete
// no dia); o que nao chegou no Drive so avisa no comeco da sequencia
func TestAvisoBackupIncompleto(t *testing.T) {
	c, _, _ := newAppsCore(t)
	f := withBrevo(t, c)
	wait := func() {
		for c.work.Count() > 0 {
			time.Sleep(time.Millisecond)
		}
	}
	partial := BackupRun{Databases: 5, Errors: []string{"postgres/estoque", "mysql/portal"}}
	c.notifyBackup(&BackupRun{}, partial, nil)
	c.notifyBackup(&partial, partial, nil) // de novo no dia seguinte
	wait()
	got := f.wait(t, 2)
	for _, want := range []string{"Backup incompleto", "estes databases", "postgres/estoque", "mysql/portal", "5 databases"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("e-mail sem %q:\n%s", want, got[0])
		}
	}
	one := BackupRun{Databases: 1, Errors: []string{"postgres/estoque"}}
	c.notifyBackup(nil, one, nil)
	wait()
	if got := f.wait(t, 3); !strings.Contains(got[2], "sem este database:") {
		t.Errorf("singular:\n%s", got[2])
	}

	// backup que deu certo nao avisa; falha de envio depois de outra falha
	// de envio tambem nao
	c.notifyBackup(nil, BackupRun{Databases: 5}, nil)
	c.notifyBackup(&BackupRun{Error: "drive fora"}, BackupRun{Error: "drive fora"}, errors.New("drive fora"))
	wait()
	f.wait(t, 3)
}

// avisos desligados (sem destinatarios) ou sem a chave da Brevo: nada sai
// e nada trava
func TestAvisoSemConfiguracao(t *testing.T) {
	c, _, _ := newAppsCore(t)
	c.notifyAutoDeploy("loja", "abc1234", errors.New("quebrou"))
	if c.work.Count() != 0 {
		t.Fatal("tentou mandar com os avisos desligados")
	}
	f := withBrevo(t, c)
	c.secretStore().Delete(secretBrevoKey)
	c.notifyAutoDeploy("loja", "abc1234", errors.New("quebrou"))
	if c.work.Count() != 0 || len(f.list()) != 0 {
		t.Fatal("tentou mandar sem a chave")
	}
	if err := c.TestNotify(context.Background()); err == nil || !strings.Contains(err.Error(), "cais setup secret brevo_api_key") {
		t.Errorf("e-mail de teste sem chave: %v", err)
	}
}
