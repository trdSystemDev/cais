package apps

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/trdSystemDev/cais/internal/docker"
)

// promoteSetup monta o historico:
//
//	main:    c1 ── c2 ── c3
//	homolog: c1 ── h1
//
// e os apps web (producao, main) e web-homolog (homologacao de web).
func promoteSetup(t *testing.T, homologBranch string) (Manager, *fakeSource, *fakeDocker) {
	t.Helper()
	m, src, d := newManager(t)
	src.commits = map[string]map[string]string{}
	for _, c := range []string{"c1", "c2", "c3", "h1", "m1", "s1"} {
		src.commits[c] = map[string]string{"docker-compose.yml": "v-" + c}
	}
	src.parents = map[string][]string{"c2": {"c1"}, "c3": {"c2"}, "h1": {"c1"}, "m1": {"c3", "h1"}, "s1": {"c3"}}
	src.branches = map[string]string{"main": "c3", "homolog": "h1"}
	src.head = "c3"
	if _, err := m.Create(App{Name: "web", Repo: "trd/web", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(App{Name: "web-homolog", Repo: "trd/web", Branch: homologBranch, PromoteTo: "web"}); err != nil {
		t.Fatal(err)
	}
	return m, src, d
}

func deployAt(t *testing.T, m Manager, app, commit string) {
	t.Helper()
	if _, err := m.Deploy(context.Background(), app, commit); err != nil {
		t.Fatal(err)
	}
}

// mesma branch: a homologacao esta um commit a frente da producao
func TestPromoteMesmaBranch(t *testing.T) {
	m, _, _ := promoteSetup(t, "main")
	ctx := context.Background()
	deployAt(t, m, "web", "c1")
	deployAt(t, m, "web-homolog", "c2")

	p, err := m.CheckPromotion(ctx, "web-homolog")
	if err != nil {
		t.Fatal(err)
	}
	if p.State != PromoteReady || p.Deploy != "c2" || p.Ahead != 1 || p.Unvalidated != 1 {
		t.Fatalf("promocao %+v", p)
	}
	if _, err := m.Promote(ctx, "web-homolog", false); err != nil {
		t.Fatal(err)
	}
	prod, _ := m.Get("web")
	if prod.Commit != "c2" || prod.Last.PromotedFrom != "web-homolog" {
		t.Fatalf("producao no ar %s, ultimo %+v", prod.Commit, prod.Last)
	}
	// de novo: nada a promover
	if p, _ := m.CheckPromotion(ctx, "web-homolog"); p.State != PromoteSame {
		t.Fatalf("depois de promover: %+v", p)
	}
	if _, err := m.Promote(ctx, "web-homolog", false); err == nil || !strings.Contains(err.Error(), "nada a promover") {
		t.Fatalf("promoveu o mesmo commit: %v", err)
	}
}

// branches diferentes: sem merge bloqueia; com merge sobe o commit validado
// (nao o merge); com squash sobe a ponta da main (mesmo codigo)
func TestPromoteBranchDiferente(t *testing.T) {
	m, src, _ := promoteSetup(t, "homolog")
	ctx := context.Background()
	deployAt(t, m, "web", "c2")
	deployAt(t, m, "web-homolog", "")
	if a, _ := m.Get("web-homolog"); a.Commit != "h1" {
		t.Fatalf("homologacao no ar %s", a.Commit)
	}

	p, _ := m.CheckPromotion(ctx, "web-homolog")
	if p.State != PromoteBlocked || !strings.Contains(p.Reason, "faca o merge") {
		t.Fatalf("sem merge: %+v", p)
	}
	if _, err := m.Promote(ctx, "web-homolog", true); err == nil {
		t.Fatal("promoveu sem merge")
	}

	// merge da homolog na main, mas a homolog saiu de c1 e nao tem c2 e c3
	// (que estao na producao): promover h1 tiraria c2 da producao
	src.branches["main"] = "m1"
	p, _ = m.CheckPromotion(ctx, "web-homolog")
	if p.State != PromoteOlder || !strings.Contains(p.Reason, "perderia") {
		t.Fatalf("homolog desatualizada: %+v", p)
	}

	// o fluxo certo: a homolog sai da main atual (c2) e volta por merge
	src.parents["h1"] = []string{"c2"}
	src.parents["m1"] = []string{"c2", "h1"}
	p, _ = m.CheckPromotion(ctx, "web-homolog")
	if p.State != PromoteReady || p.Deploy != "h1" || p.Ahead != 1 {
		t.Fatalf("com merge: %+v", p)
	}

	src.branches["main"] = "s1" // squash: commit novo com o mesmo codigo
	src.parents["s1"] = []string{"c2"}
	src.trees = map[string]string{"s1": "tree-h1"}
	p, _ = m.CheckPromotion(ctx, "web-homolog")
	if p.State != PromoteReady || p.Deploy != "s1" {
		t.Fatalf("com squash: %+v", p)
	}
	if _, err := m.Promote(ctx, "web-homolog", false); err != nil {
		t.Fatal(err)
	}
	if prod, _ := m.Get("web"); prod.Commit != "s1" {
		t.Fatalf("producao no ar %s", prod.Commit)
	}
}

// voltar a producao pra um commit mais velho so com confirmacao
func TestPromoteMaisVelhoPedeConfirmacao(t *testing.T) {
	m, _, _ := promoteSetup(t, "main")
	ctx := context.Background()
	deployAt(t, m, "web", "c3")
	deployAt(t, m, "web-homolog", "c2")
	p, _ := m.CheckPromotion(ctx, "web-homolog")
	if p.State != PromoteOlder {
		t.Fatalf("%+v", p)
	}
	if _, err := m.Promote(ctx, "web-homolog", false); !errors.Is(err, ErrConfirm) {
		t.Fatalf("sem confirmacao: %v", err)
	}
	if _, err := m.Promote(ctx, "web-homolog", true); err != nil {
		t.Fatal(err)
	}
	if prod, _ := m.Get("web"); prod.Commit != "c2" {
		t.Fatalf("producao no ar %s", prod.Commit)
	}
}

// so promove o que esta no ar e de pe na homologacao
func TestPromoteSoOQueEstaDePe(t *testing.T) {
	m, _, d := promoteSetup(t, "main")
	ctx := context.Background()
	if p, _ := m.CheckPromotion(ctx, "web-homolog"); p.State != PromoteBlocked || !strings.Contains(p.Reason, "ainda nao teve deploy") {
		t.Fatalf("sem deploy: %+v", p)
	}
	deployAt(t, m, "web-homolog", "c2")

	// deploy novo falhou: o que esta no ar e o anterior
	d.failUp["v-c3"] = true
	m.Deploy(ctx, "web-homolog", "c3")
	if p, _ := m.CheckPromotion(ctx, "web-homolog"); p.State != PromoteBlocked || !strings.Contains(p.Reason, "falhou") {
		t.Fatalf("deploy falho: %+v", p)
	}
	d.failUp["v-c3"] = false
	deployAt(t, m, "web-homolog", "c2")

	d.ps = map[string][]docker.Container{"v-c2": {{Name: "web-homolog-web-1", State: "exited", ExitCode: 1, Status: "Exited (1)"}}}
	if p, _ := m.CheckPromotion(ctx, "web-homolog"); p.State != PromoteBlocked || !strings.Contains(p.Reason, "nao esta bem") {
		t.Fatalf("container caido: %+v", p)
	}
}

func TestVinculoDeHomologacao(t *testing.T) {
	m, _, _ := promoteSetup(t, "main")
	cases := map[string]App{
		"outro repositorio":      {Name: "x", Repo: "trd/outro", PromoteTo: "web"},
		"producao inexistente":   {Name: "x", Repo: "trd/web", PromoteTo: "nada"},
		"homologacao da homolog": {Name: "x", Repo: "trd/web", PromoteTo: "web-homolog"},
		"ele mesmo":              {Name: "x", Repo: "trd/web", PromoteTo: "x"},
	}
	for name, a := range cases {
		if _, err := m.Create(a); err == nil {
			t.Errorf("%s: aceitou %+v", name, a)
		}
	}
	// a producao (que tem homologacao) nao vira homologacao de outro
	m.Create(App{Name: "outro", Repo: "trd/web"})
	if err := m.Update(App{Name: "web", Repo: "trd/web", PromoteTo: "outro"}); err == nil {
		t.Error("producao virou homologacao")
	}
	// Update guarda o vinculo e o deploy automatico
	if err := m.Update(App{Name: "outro", Repo: "trd/web", PromoteTo: "web", AutoDeploy: true}); err != nil {
		t.Fatal(err)
	}
	if a, _ := m.Get("outro"); a.PromoteTo != "web" || !a.AutoDeploy {
		t.Fatalf("%+v", a)
	}
}
