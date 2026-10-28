package core

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trdSystemDev/cais/internal/ci"
	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/github/githubtest"
)

func newGitHubCore(t *testing.T) (*Core, *githubtest.Server) {
	t.Helper()
	gh := githubtest.New(t, 5512022, map[string]map[string]string{
		"trdSystemDev/loja": {
			"Dockerfile":   "FROM node:22\nEXPOSE 3000\n",
			"package.json": `{"dependencies": {"pg": "8"}}`,
			".env.example": "NODE_ENV=production\nJWT_SECRET=x\n",
		},
		"trdSystemDev/Meu_App": {"Dockerfile": "FROM nginx\n"},
	})
	root := t.TempDir()
	c := New(Deps{CheckDocker: okStatus, Now: func() time.Time { return fixedNow },
		ConfigPath: filepath.Join(root, "config.yaml"), SecretsDir: filepath.Join(root, "secrets"),
		AppsDir: filepath.Join(root, "apps"), AppDocker: &appDocker{release: make(chan struct{}), ups: make(chan string, 10)},
		GitHubURL: gh.URL,
		ExecInput: func(context.Context, string, []byte, ...string) (string, error) { return "", nil }})
	cfg := config.Default()
	cfg.Domain, cfg.Email = "trd.localhost", "a@trd.localhost"
	cfg.GitHub.AppID = 5512022
	cfg.Stacks = []string{"postgres"}
	if err := config.Save(c.deps.ConfigPath, cfg); err != nil {
		t.Fatal(err)
	}
	return c, gh
}

func TestImportPeloGitHub(t *testing.T) {
	c, gh := newGitHubCore(t)
	ctx := context.Background()

	if _, err := c.PlanAppFromGitHub(ctx, "https://github.com/trdSystemDev/loja"); err == nil || !strings.Contains(err.Error(), "github_app_key") {
		t.Fatalf("sem a chave devia pedir o secret: %v", err)
	}
	if err := c.SetSecret(ctx, GitHubKeySecret, string(gh.Key)); err != nil {
		t.Fatal(err)
	}

	plan, err := c.PlanAppFromGitHub(ctx, "https://github.com/trdSystemDev/loja.git")
	if err != nil {
		t.Fatal(err)
	}
	if plan.App != "loja" || plan.Repo != "trdSystemDev/loja" || plan.Spec.Port != 3000 || plan.Database == nil {
		t.Fatalf("plano %+v", plan)
	}
	if err := c.ApplyApp(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if a, _ := c.appManager().Get("loja"); a.Repo != "trdSystemDev/loja" {
		t.Fatalf("repo nao gravado: %+v", a)
	}

	link, err := c.ProposeAppCI(ctx, "loja")
	if err != nil {
		t.Fatal(err)
	}
	if link != "https://github.com/trdSystemDev/loja/pull/1" || gh.PR("trdSystemDev/loja", ".woodpecker.yml") != string(ci.Pipeline()) {
		t.Fatalf("PR %s com %q", link, gh.PR("trdSystemDev/loja", ".woodpecker.yml"))
	}
}

func TestImportRecusa(t *testing.T) {
	c, gh := newGitHubCore(t)
	ctx := context.Background()
	c.SetSecret(ctx, GitHubKeySecret, string(gh.Key))

	cases := map[string]string{
		"https://github.com/trdSystemDev/nao-instalado": "nao esta instalado",
		"https://github.com/trdSystemDev/Meu_App":       "renomeie o repositorio",
		"https://gitlab.com/a/b":                        "nao e de um repositorio do GitHub",
	}
	for link, want := range cases {
		if _, err := c.PlanAppFromGitHub(ctx, link); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", link, err)
		}
	}
	// plano adulterado: o app tem que ter o nome do repositorio
	plan, err := c.PlanAppFromGitHub(ctx, "trdSystemDev/loja")
	if err != nil {
		t.Fatal(err)
	}
	plan.App = "outro"
	plan.Spec.Image = "registry.trd.localhost/outro"
	if err := c.ApplyApp(ctx, plan); err == nil || !strings.Contains(err.Error(), "nome do repositorio") {
		t.Fatalf("aplicou com nome diferente do repositorio: %v", err)
	}
	// app sem repositorio nao abre PR
	c.CreateApp(ctx, "local")
	if _, err := c.ProposeAppCI(ctx, "local"); err == nil {
		t.Error("abriu PR pra app sem repositorio")
	}
}

// banco de stack desligada: recusa antes de criar qualquer coisa
func TestApplyNaoDeixaAppPelaMetade(t *testing.T) {
	c, gh := newGitHubCore(t)
	ctx := context.Background()
	c.SetSecret(ctx, GitHubKeySecret, string(gh.Key))
	plan, err := c.PlanAppFromGitHub(ctx, "trdSystemDev/loja")
	if err != nil {
		t.Fatal(err)
	}
	plan.Database.Engine = "mysql" // stack desligada
	if err := c.ApplyApp(ctx, plan); err == nil || !strings.Contains(err.Error(), "stack mysql esta desligada") {
		t.Fatalf("erro %v", err)
	}
	if _, err := c.appManager().Get("loja"); err == nil {
		t.Fatal("o app foi criado mesmo com o plano recusado")
	}
}
