package core

import (
	"context"
	"errors"
	"strings"
	"testing"

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
