package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/docker"
	"github.com/trdSystemDev/cais/internal/github/githubtest"
)

// appDocker finge o docker compose dos apps.
type appDocker struct {
	mu     sync.Mutex
	calls  []string
	block  chan struct{} // se nao nil, o build espera
	upErr  error
	ps     []docker.Container
	config string
}

func (d *appDocker) add(s string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, s)
}

func (d *appDocker) Config(_ context.Context, p docker.Project) ([]byte, error) {
	d.add("config " + p.Name)
	if d.config != "" {
		return []byte(d.config), nil
	}
	return []byte(`{"services": {}}`), nil
}

func (d *appDocker) Build(_ context.Context, p docker.Project) error {
	d.add("build " + p.Name)
	if d.block != nil {
		<-d.block
	}
	return nil
}

func (d *appDocker) Up(_ context.Context, p docker.Project) error {
	d.add("up " + p.Name)
	return d.upErr
}

func (d *appDocker) PS(context.Context, docker.Project) ([]docker.Container, error) {
	return d.ps, nil
}

func (d *appDocker) Restart(_ context.Context, p docker.Project, service string) error {
	d.add("restart " + p.Name + " " + service)
	return nil
}

func (d *appDocker) Stop(_ context.Context, p docker.Project) error {
	d.add("stop " + p.Name)
	return nil
}

// core com o GitHub falso, um repositorio com compose e o docker fingido
func newAppsCore(t *testing.T) (*Core, *appDocker, *githubtest.Server) {
	t.Helper()
	root := t.TempDir()
	gh := githubtest.New(t, 5512022, map[string]map[string]string{
		"trdSystemDev/meu-app": {"docker-compose.yml": "services: {web: {build: .}}\n", "Dockerfile": "FROM node:22\n"},
	})
	d := &appDocker{}
	c := New(Deps{
		CheckDocker: okStatus,
		Now:         func() time.Time { return fixedNow },
		ConfigPath:  filepath.Join(root, "config.yaml"),
		SecretsDir:  filepath.Join(root, "secrets"),
		StackDir:    func(n string) string { return filepath.Join(root, "stacks", n) },
		AppsDir:     filepath.Join(root, "apps"),
		AppDocker:   d,
		AppSettle:   -1,
		GitHubURL:   gh.URL,
		ContainerLogs: func(_ context.Context, name string) (string, error) {
			return "log de " + name, nil
		},
	})
	cfg := config.Default()
	cfg.Domain, cfg.Email = "exemplo.com.br", "a@exemplo.com.br"
	cfg.GitHub.AppID = gh.AppID
	cfg.Stacks = []string{"postgres"}
	if err := c.SaveConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if err := c.SetSecret(context.Background(), GitHubKeySecret, string(gh.Key)); err != nil {
		t.Fatal(err)
	}
	return c, d, gh
}

func TestDeployPeloGitHubApp(t *testing.T) {
	c, d, gh := newAppsCore(t)
	ctx := context.Background()
	if _, err := c.CreateApp(ctx, apps.App{Name: "meu-app", Repo: "trdSystemDev/meu-app"}); err != nil {
		t.Fatal(err)
	}
	if err := c.SetAppEnv(ctx, "meu-app", map[string]string{"NODE_ENV": "production"}); err != nil {
		t.Fatal(err)
	}
	res, err := c.DeployApp(ctx, "meu-app", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Commit != gh.Commit("trdSystemDev/meu-app") {
		t.Errorf("commit %s", res.Commit)
	}
	src := filepath.Join(c.deps.AppsDir, "meu-app", apps.SrcDir)
	if data, err := os.ReadFile(filepath.Join(src, "Dockerfile")); err != nil || string(data) != "FROM node:22\n" {
		t.Errorf("codigo baixado: %q %v", data, err)
	}
	env, _ := os.ReadFile(filepath.Join(src, ".env"))
	if !strings.Contains(string(env), "NODE_ENV='production'") || !strings.Contains(string(env), "CAIS_COMMIT='"+res.Commit+"'") {
		t.Errorf(".env:\n%s", env)
	}
	if strings.Join(d.calls, ",") != "config meu-app,build meu-app,up meu-app" {
		t.Errorf("chamadas %v", d.calls)
	}
	s := snap(t, c).Apps
	if len(s.List) != 1 || s.List[0].Commit != res.Commit || s.List[0].Deploying {
		t.Fatalf("estado %+v", s)
	}

	// commit novo no repositorio: o proximo deploy pega
	gh.SetFile("trdSystemDev/meu-app", "Dockerfile", "FROM node:24\n")
	res2, err := c.DeployApp(ctx, "meu-app", "")
	if err != nil || res2.Commit == res.Commit {
		t.Fatalf("segundo deploy %+v %v", res2, err)
	}
	if _, err := c.DeployApp(ctx, "meu-app", "nao-existe"); err == nil {
		t.Error("deploy de ref que nao existe")
	}
}

func TestDeploySemGitHubAppExplica(t *testing.T) {
	c, d, gh := newAppsCore(t)
	ctx := context.Background()
	c.CreateApp(ctx, apps.App{Name: "meu-app", Repo: "trdSystemDev/meu-app"})
	c.CreateApp(ctx, apps.App{Name: "outro", Repo: "trdSystemDev/nao-instalado"})
	if _, err := c.DeployApp(ctx, "outro", ""); err == nil || !strings.Contains(err.Error(), "nao esta instalado") {
		t.Errorf("repositorio sem o app instalado: %v", err)
	}
	os.Remove(filepath.Join(c.deps.SecretsDir, GitHubKeySecret))
	if _, err := c.DeployApp(ctx, "meu-app", ""); err == nil || !strings.Contains(err.Error(), "chave do GitHub App") {
		t.Errorf("sem a chave: %v", err)
	}
	c.SetSecret(ctx, GitHubKeySecret, string(gh.Key))
	if len(d.calls) != 0 {
		t.Errorf("chamou o docker sem codigo: %v", d.calls)
	}
}

func TestDeployMarcaAndamentoERecusaOutro(t *testing.T) {
	c, d, _ := newAppsCore(t)
	ctx := context.Background()
	c.CreateApp(ctx, apps.App{Name: "meu-app", Repo: "trdSystemDev/meu-app"})
	d.block = make(chan struct{})

	done := make(chan error, 1)
	go func() {
		_, err := c.DeployApp(ctx, "meu-app", "")
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s := snap(t, c).Apps
		if len(s.List) == 1 && s.List[0].Deploying {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("o estado nao mostrou o deploy em andamento")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := c.DeployApp(ctx, "meu-app", ""); err == nil || !strings.Contains(err.Error(), "em andamento") {
		t.Errorf("segundo deploy junto: %v", err)
	}
	if err := c.RestartApp(ctx, "meu-app", ""); err == nil {
		t.Error("restart durante o deploy")
	}
	close(d.block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if s := snap(t, c).Apps; s.List[0].Deploying {
		t.Error("continuou marcado como deploy")
	}
}

func TestAppLogsSoDoProprioApp(t *testing.T) {
	c, d, _ := newAppsCore(t)
	ctx := context.Background()
	c.CreateApp(ctx, apps.App{Name: "meu-app", Repo: "trdSystemDev/meu-app"})
	if _, err := c.DeployApp(ctx, "meu-app", ""); err != nil {
		t.Fatal(err)
	}
	d.ps = []docker.Container{{Name: "meu-app-web-1", State: "running"}}
	if _, err := c.AppLogs(ctx, "meu-app", "postgres"); err == nil {
		t.Error("leu log de container de fora do app")
	}
	out, err := c.AppLogs(ctx, "meu-app", "meu-app-web-1")
	if err != nil || out != "log de meu-app-web-1" {
		t.Errorf("%q %v", out, err)
	}
}

func TestVariaveisESecretsDoApp(t *testing.T) {
	c, _, _ := newAppsCore(t)
	ctx := context.Background()
	c.CreateApp(ctx, apps.App{Name: "meu-app", Repo: "trdSystemDev/meu-app"})
	if err := c.SetAppEnv(ctx, "meu-app", map[string]string{"CAIS_COMMIT": "x"}); err == nil {
		t.Error("deixou trocar variavel do cais")
	}
	c.SetAppEnv(ctx, "meu-app", map[string]string{"A": "1"})
	if env, _ := c.AppEnv(ctx, "meu-app"); env["A"] != "1" {
		t.Errorf("env %v", env)
	}
	if err := c.SetAppSecret(ctx, "meu-app", "jwt", "segredo"); err != nil {
		t.Fatal(err)
	}
	if s := snap(t, c).Apps.List[0]; len(s.Secrets) != 1 || s.Secrets[0].Name != "jwt" {
		t.Errorf("secrets no estado %+v", s.Secrets)
	}
	// o estado nunca leva valor de secret
	data, _ := os.ReadFile(filepath.Join(c.deps.AppsDir, "meu-app", apps.MetaFile))
	if strings.Contains(string(data), "segredo") {
		t.Error("app.yaml com o valor do secret")
	}

	g, err := c.AppGuide(ctx, "meu-app")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Host(`meu-app.exemplo.com.br`)", "postgres_network", "${CAIS_SECRETS}/jwt"} {
		if !strings.Contains(g, want) {
			t.Errorf("ajuda sem %q", want)
		}
	}
	if err := c.DeleteAppSecret(ctx, "meu-app", "jwt"); err != nil {
		t.Fatal(err)
	}
	if list, _ := c.AppSecrets(ctx, "meu-app"); len(list) != 0 {
		t.Errorf("secrets %v", list)
	}
}

func TestCreateDatabase(t *testing.T) {
	c, _, _ := newAppsCore(t)
	ctx := context.Background()
	var sql []string
	c.deps.ExecInput = func(_ context.Context, container string, input []byte, args ...string) (string, error) {
		sql = append(sql, container+": "+string(input))
		return "", nil
	}

	info, err := c.CreateDatabase(ctx, DatabaseRequest{Engine: "postgres", Name: "portal", Extensions: []string{"postgis"}})
	if err != nil {
		t.Fatal(err)
	}
	if info.Host != "postgres" || info.Port != 5432 || info.Network != "postgres_network" || info.User != "portal" ||
		len(info.Password) < 20 || info.URL != "postgres://portal:"+info.Password+"@postgres:5432/portal" {
		t.Errorf("credencial %+v", info)
	}
	if len(sql) == 0 || !strings.HasPrefix(sql[0], "postgres: ") || !strings.Contains(strings.Join(sql, "\n"), "postgis") {
		t.Errorf("sql %v", sql)
	}
	for _, s := range sql {
		if strings.Contains(s, "args") {
			t.Error("senha na linha de comando")
		}
	}

	if _, err := c.CreateDatabase(ctx, DatabaseRequest{Engine: "postgres", Name: "portal"}); err == nil || !strings.Contains(err.Error(), "ja existe") {
		t.Errorf("database repetido: %v", err)
	}
	if _, err := c.CreateDatabase(ctx, DatabaseRequest{Engine: "mysql", Name: "estoque"}); err == nil || !strings.Contains(err.Error(), "desligada") {
		t.Errorf("mysql desligado: %v", err)
	}
	if _, err := c.CreateDatabase(ctx, DatabaseRequest{Engine: "postgres", Name: "Nome-Ruim"}); err == nil {
		t.Error("aceitou nome invalido")
	}

	list, err := c.Databases(ctx)
	if err != nil || len(list) != 1 || list[0] != info {
		t.Fatalf("lista %+v %v", list, err)
	}

	c.infra.Lock()
	_, err = c.CreateDatabase(ctx, DatabaseRequest{Engine: "postgres", Name: "outro"})
	c.infra.Unlock()
	if !errors.Is(err, ErrBusy) {
		t.Errorf("durante o Setup: %v", err)
	}
}
