package github

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const sha40 = "0123456789012345678901234567890123456789"

// limitedGitHub e uma API minima pra testar sessao, ETag e limite.
type limitedGitHub struct {
	mu     sync.Mutex
	now    time.Time
	calls  map[string]int
	commit func(w http.ResponseWriter, r *http.Request) // resposta de /commits
}

func (f *limitedGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	switch {
	case strings.HasSuffix(p, "/installation"):
		f.calls["installation"]++
		io.WriteString(w, `{"id": 9}`)
	case strings.HasSuffix(p, "/access_tokens"):
		f.calls["token"]++
		w.WriteHeader(201)
		io.WriteString(w, `{"token": "t`+strconv.Itoa(f.calls["token"])+`", "expires_at": "`+f.now.Add(time.Hour).UTC().Format(time.RFC3339)+`"}`)
	case p == "/repos/o/r":
		f.calls["repo"]++
		io.WriteString(w, `{"default_branch": "main"}`)
	case strings.HasPrefix(p, "/repos/o/r/commits/"):
		f.calls["commits"]++
		f.commit(w, r)
	default:
		w.WriteHeader(404)
	}
}

func newLimited(t *testing.T) (App, *limitedGitHub, *time.Time) {
	_, pemKey := testKey(t)
	key, err := ParseKey(pemKey)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	f := &limitedGitHub{now: clock, calls: map[string]int{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	app := App{ID: 1, Key: key, BaseURL: srv.URL}
	app.Now = func() time.Time { return clock } // o teste adianta pelo ponteiro
	return app, f, &clock
}

func TestSessionsReaproveitaTokenEUsaETag(t *testing.T) {
	app, f, clock := newLimited(t)
	f.commit = func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `W/"e1"` {
			f.calls["304"]++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `W/"e1"`)
		io.WriteString(w, sha40)
	}
	var c Sessions
	ctx := context.Background()
	repo := Repo{"o", "r"}
	for i := 0; i < 3; i++ {
		s, err := c.Open(ctx, app, repo)
		if err != nil {
			t.Fatal(err)
		}
		sha, err := s.Resolve(ctx, "")
		if err != nil || sha != sha40 {
			t.Fatalf("Resolve %d = %q, %v", i, sha, err)
		}
	}
	if f.calls["installation"] != 1 || f.calls["token"] != 1 || f.calls["repo"] != 1 || f.calls["commits"] != 3 || f.calls["304"] != 2 {
		t.Fatalf("chamadas %v", f.calls)
	}

	// perto de vencer, troca o token e continua com o ETag
	*clock = clock.Add(56 * time.Minute)
	f.mu.Lock()
	f.now = *clock
	f.mu.Unlock()
	s, err := c.Open(ctx, app, repo)
	if err != nil {
		t.Fatal(err)
	}
	if sha, err := s.Resolve(ctx, ""); err != nil || sha != sha40 {
		t.Fatalf("Resolve com token novo = %q, %v", sha, err)
	}
	if f.calls["token"] != 2 || f.calls["304"] != 3 {
		t.Fatalf("depois de renovar: %v", f.calls)
	}

	// Forget abre outra na proxima
	c.Forget(app, repo)
	if _, err := c.Open(ctx, app, repo); err != nil || f.calls["token"] != 3 {
		t.Fatalf("depois do Forget: %v %v", f.calls, err)
	}
}

func TestLimiteEsgotadoNaoChamaDeNovo(t *testing.T) {
	app, f, clock := newLimited(t)
	reset := clock.Add(10 * time.Minute)
	f.commit = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"message":"API rate limit exceeded"}`)
	}
	var c Sessions
	ctx := context.Background()
	s, err := c.Open(ctx, app, Repo{"o", "r"})
	if err != nil {
		t.Fatal(err)
	}
	var rl *RateLimitError
	if _, err := s.Resolve(ctx, ""); !errors.As(err, &rl) || !rl.Until.Equal(reset) {
		t.Fatalf("Resolve com a cota esgotada: %v", err)
	}
	if _, err := s.Resolve(ctx, ""); !errors.As(err, &rl) || f.calls["commits"] != 1 {
		t.Fatalf("chamou de novo com a cota esgotada: %v, %v", err, f.calls)
	}
	if q, low := c.Limits.Low(*clock); !low || q.Name != QuotaInstallation(9) {
		t.Fatalf("Low = %+v %v", q, low)
	}
	// passado o reset, chama de novo
	if _, low := c.Limits.Low(reset.Add(time.Second)); low {
		t.Error("cota ainda baixa depois do reset")
	}
}

func TestLimitsReservaERetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	h := func(remaining int) http.Header {
		return http.Header{
			"X-Ratelimit-Limit":     {"5000"},
			"X-Ratelimit-Remaining": {strconv.Itoa(remaining)},
			"X-Ratelimit-Reset":     {strconv.FormatInt(now.Add(time.Hour).Unix(), 10)},
		}
	}
	var l Limits
	if err := l.observe(QuotaApp, 200, h(600), now); err != nil {
		t.Fatal(err)
	}
	if _, low := l.Low(now); low {
		t.Error("600 de 5000 nao e baixo (reserva de 500)")
	}
	l.observe(QuotaApp, 200, h(499), now)
	if q, low := l.Low(now); !low || q.Remaining != 499 {
		t.Errorf("499 de 5000: %+v %v", q, low)
	}

	// limite secundario: 429 com Retry-After bloqueia por esse tempo
	var l2 Limits
	err := l2.observe(QuotaApp, 429, http.Header{"Retry-After": {"30"}}, now)
	var rl *RateLimitError
	if !errors.As(err, &rl) || !rl.Until.Equal(now.Add(30*time.Second)) {
		t.Fatalf("429: %v", err)
	}
	if l2.wait(QuotaApp, now.Add(10*time.Second)) == nil || l2.wait(QuotaApp, now.Add(31*time.Second)) != nil {
		t.Error("bloqueio do Retry-After errado")
	}
	// 403 que nao e de limite (sem permissao) nao bloqueia
	var l3 Limits
	if err := l3.observe(QuotaApp, 403, h(4000), now); err != nil {
		t.Errorf("403 de permissao virou limite: %v", err)
	}
}
