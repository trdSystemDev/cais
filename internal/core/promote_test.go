package core

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/docker"
)

// homologacao na mesma branch: valida o commit novo e promove pra producao
// pelo GitHub App (compare de verdade, na API falsa)
func TestPromoverHomologacao(t *testing.T) {
	c, d, gh := newAppsCore(t)
	ctx := context.Background()
	d.ps = []docker.Container{{Name: "x-web-1", State: "running"}}
	const repo = "trdSystemDev/meu-app"
	c.CreateApp(ctx, apps.App{Name: "meu-app", Repo: repo})
	if _, err := c.CreateApp(ctx, apps.App{Name: "meu-app-homolog", Repo: repo, PromoteTo: "meu-app"}); err != nil {
		t.Fatal(err)
	}
	prod, err := c.DeployApp(ctx, "meu-app", "")
	if err != nil {
		t.Fatal(err)
	}

	gh.SetFile(repo, "Dockerfile", "FROM node:24\n")
	homolog, err := c.DeployApp(ctx, "meu-app-homolog", "")
	if err != nil {
		t.Fatal(err)
	}
	gh.SetFile(repo, "Dockerfile", "FROM node:25\n") // commit depois do validado

	p, err := c.CheckPromotion(ctx, "meu-app-homolog")
	if err != nil {
		t.Fatal(err)
	}
	if p.State != apps.PromoteReady || p.Deploy != homolog.Commit || p.Current != prod.Commit || p.Ahead != 1 || p.Unvalidated != 1 {
		t.Fatalf("promocao %+v", p)
	}
	res, err := c.PromoteApp(ctx, "meu-app-homolog", false)
	if err != nil || res.Commit != homolog.Commit {
		t.Fatalf("promoveu %+v %v", res, err)
	}
	a := findApp(snap(t, c).Apps.List, "meu-app")
	if a == nil || a.Commit != homolog.Commit || a.Last.PromotedFrom != "meu-app-homolog" || a.Deploying {
		t.Fatalf("producao %+v", a)
	}

	if _, err := c.PromoteApp(ctx, "meu-app", false); err == nil || !strings.Contains(err.Error(), "nao e homologacao") {
		t.Errorf("promoveu um app sem homologacao: %v", err)
	}
}

var errUp = errors.New("container web is unhealthy")

// deploy automatico: so os apps marcados, so commit novo, e um commit que
// falhou nao e tentado de novo a cada rodada
func TestDeployAutomatico(t *testing.T) {
	c, d, gh := newAppsCore(t)
	ctx := context.Background()
	const repo = "trdSystemDev/meu-app"
	c.CreateApp(ctx, apps.App{Name: "meu-app", Repo: repo, AutoDeploy: true})
	c.CreateApp(ctx, apps.App{Name: "manual", Repo: repo})

	round := func() []string {
		started, done := c.AutoDeployOnce(ctx)
		<-done
		return started
	}
	if got := round(); len(got) != 1 || got[0] != "meu-app" {
		t.Fatalf("primeira rodada: %v", got)
	}
	a := findApp(snap(t, c).Apps.List, "meu-app")
	if a.Commit != gh.Commit(repo) || !a.Last.Auto {
		t.Fatalf("depois do deploy automatico: %+v", a)
	}
	if got := round(); len(got) != 0 {
		t.Fatalf("sem commit novo fez deploy: %v", got)
	}

	// commit novo que falha: tenta uma vez so
	gh.SetFile(repo, "Dockerfile", "FROM quebrado\n")
	d.upErr = errUp
	if got := round(); len(got) != 1 {
		t.Fatalf("commit novo: %v", got)
	}
	if got := round(); len(got) != 0 {
		t.Fatalf("tentou de novo o commit que falhou: %v", got)
	}
	// o proximo commit (a correcao) sobe
	d.upErr = nil
	gh.SetFile(repo, "Dockerfile", "FROM node:24\n")
	if got := round(); len(got) != 1 {
		t.Fatalf("correcao: %v", got)
	}
	if a := findApp(snap(t, c).Apps.List, "meu-app"); a.Commit != gh.Commit(repo) {
		t.Fatalf("correcao nao subiu: %+v", a)
	}
	if strings.Contains(strings.Join(d.calls, ","), "up manual") {
		t.Error("deploy automatico num app manual")
	}
}

// rollback num app com deploy automatico: o automatico nao sobe de novo o
// commit que acabou de sair, so o proximo
func TestRollbackPausaODeployAutomatico(t *testing.T) {
	c, _, gh := newAppsCore(t)
	ctx := context.Background()
	const repo = "trdSystemDev/meu-app"
	c.CreateApp(ctx, apps.App{Name: "meu-app", Repo: repo, AutoDeploy: true})
	round := func() []string {
		started, done := c.AutoDeployOnce(ctx)
		<-done
		return started
	}
	round()
	bom := gh.Commit(repo)
	gh.SetFile(repo, "Dockerfile", "FROM node:24 # bug\n")
	round()
	bug := gh.Commit(repo)
	if a := findApp(snap(t, c).Apps.List, "meu-app"); a.Commit != bug || len(a.History) != 2 {
		t.Fatalf("antes do rollback: %+v", a)
	}

	res, err := c.RollbackApp(ctx, "meu-app", "")
	if err != nil || res.Commit != bom {
		t.Fatalf("rollback %+v %v", res, err)
	}
	if got := round(); len(got) != 0 {
		t.Fatalf("o automatico desfez o rollback: %v", got)
	}
	a := findApp(snap(t, c).Apps.List, "meu-app")
	if a.Commit != bom || a.AutoHold != bug || !a.History[0].Rollback {
		t.Fatalf("depois do rollback: %+v", a)
	}
	// a correcao chega: o automatico volta
	gh.SetFile(repo, "Dockerfile", "FROM node:24 # corrigido\n")
	if got := round(); len(got) != 1 {
		t.Fatalf("correcao: %v", got)
	}
	if a := findApp(snap(t, c).Apps.List, "meu-app"); a.Commit != gh.Commit(repo) || a.AutoHold != "" {
		t.Fatalf("depois da correcao: %+v", a)
	}
}

// deploy automatico com muitos apps: a sessao do repositorio e reaproveitada
// e a consulta sem commit novo volta 304 (nao gasta o limite da API)
func TestDeployAutomaticoEconomizaAPI(t *testing.T) {
	c, _, gh := newAppsCore(t)
	ctx := context.Background()
	const repo = "trdSystemDev/meu-app"
	for _, n := range []string{"a1", "a2", "a3"} {
		c.CreateApp(ctx, apps.App{Name: n, Repo: repo, AutoDeploy: true})
	}
	round := func() []string {
		started, done := c.AutoDeployOnce(ctx)
		<-done
		return started
	}
	if got := round(); len(got) != 3 {
		t.Fatalf("primeira rodada: %v", got)
	}
	gh.Calls()
	if got := round(); len(got) != 0 {
		t.Fatalf("sem commit novo: %v", got)
	}
	calls := gh.Calls()
	if calls["commits"] != 3 || calls["304"] != 3 || len(calls) != 2 {
		t.Fatalf("rodada sem mudanca chamou %v; queria so 3 consultas de commit, todas 304", calls)
	}

	// token recusado (app reinstalado): abre outra sessao e segue
	gh.RevokeTokens()
	gh.SetFile(repo, "Dockerfile", "FROM node:24\n")
	if got := round(); len(got) != 3 {
		t.Fatalf("depois do token recusado: %v", got)
	}
	if calls := gh.Calls(); calls["access_tokens"] != 1 {
		t.Errorf("token novo pedido %d vezes (queria 1, guardado pros outros)", calls["access_tokens"])
	}
}

// com a cota da API no fim, o deploy automatico espera ate o reset e deixa
// a reserva pros deploys manuais
func TestDeployAutomaticoPausaNoLimiteDaAPI(t *testing.T) {
	c, _, gh := newAppsCore(t)
	ctx := context.Background()
	const repo = "trdSystemDev/meu-app"
	c.CreateApp(ctx, apps.App{Name: "meu-app", Repo: repo, AutoDeploy: true})
	round := func() []string {
		started, done := c.AutoDeployOnce(ctx)
		<-done
		return started
	}
	gh.SetQuota(5000, 300)
	round() // fica sabendo da cota
	gh.SetFile(repo, "Dockerfile", "FROM node:24\n")
	gh.Calls()
	if got := round(); len(got) != 0 {
		t.Fatalf("deploy automatico com a cota no fim: %v", got)
	}
	if calls := gh.Calls(); len(calls) != 0 {
		t.Fatalf("rodada pausada chamou a API: %v", calls)
	}
	// o manual usa a reserva (o automatico volta no reset da cota, que o
	// teste do pacote github cobre)
	if _, err := c.DeployApp(ctx, "meu-app", ""); err != nil {
		t.Fatalf("deploy manual com a cota baixa: %v", err)
	}
}

// falha passageira (Docker Hub no limite) no deploy automatico: tenta de
// novo depois de 2, 10 e 30 minutos e desiste; o proximo commit sobe normal
func TestDeployAutomaticoTentaDeNovoFalhaPassageira(t *testing.T) {
	c, d, gh := newAppsCore(t)
	ctx := context.Background()
	const repo = "trdSystemDev/meu-app"
	var mu sync.Mutex
	now := fixedNow
	c.deps.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	after := func(dur time.Duration) { mu.Lock(); now = now.Add(dur); mu.Unlock() }
	c.CreateApp(ctx, apps.App{Name: "meu-app", Repo: repo, AutoDeploy: true})

	round := func() int {
		started, done := c.AutoDeployOnce(ctx)
		<-done
		return len(started)
	}
	d.mu.Lock()
	d.buildErr = errors.New("docker compose build (meu-app): toomanyrequests: You have reached your pull rate limit")
	d.mu.Unlock()

	steps := []struct {
		wait time.Duration
		want int
	}{
		{0, 1},                // commit novo: falha passageira
		{time.Minute, 0},      // antes dos 2 min
		{time.Minute, 1},      // 2 min: segunda tentativa
		{9 * time.Minute, 0},  // antes dos 10 min
		{time.Minute, 1},      // 10 min: terceira
		{30 * time.Minute, 1}, // 30 min: quarta e ultima
		{2 * time.Hour, 0},    // desistiu: espera o proximo commit
	}
	for i, s := range steps {
		after(s.wait)
		if got := round(); got != s.want {
			t.Fatalf("passo %d: %d deploys, esperava %d", i, got, s.want)
		}
	}

	d.mu.Lock()
	d.buildErr = nil
	d.mu.Unlock()
	gh.SetFile(repo, "Dockerfile", "FROM node:24\n")
	if got := round(); got != 1 {
		t.Fatalf("commit novo: %d", got)
	}
	if a := findApp(snap(t, c).Apps.List, "meu-app"); a.Commit != gh.Commit(repo) {
		t.Fatalf("commit novo nao subiu: %+v", a.Last)
	}
}
