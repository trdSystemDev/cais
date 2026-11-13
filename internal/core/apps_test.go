package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

func (d *appDocker) Down(_ context.Context, name string, volumes bool) error {
	d.add(fmt.Sprintf("down %s %v", name, volumes))
	return nil
}

func (_ *appDocker) TagImage(context.Context, string, string) error      { return nil }
func (_ *appDocker) HasImage(context.Context, string) bool               { return false }
func (_ *appDocker) ImageTags(context.Context, string) ([]string, error) { return nil, nil }
func (_ *appDocker) RemoveImages(context.Context, []string) error        { return nil }
func (_ *appDocker) PruneImages(context.Context, string) error           { return nil }

func (d *appDocker) Rollout(context.Context, docker.Project, string, string, bool) error { return nil }

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
	for _, want := range []string{"Host(`${DOMINIO:?", "ex: meu-app.exemplo.com.br", "postgres_network", "${CAIS_SECRETS}/jwt"} {
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
		!reflect.DeepEqual(info.Secrets, []string{"postgres_portal_user", "postgres_portal_pass"}) {
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

	c.deps.ExecInput = func(context.Context, string, []byte, ...string) (string, error) {
		return "postgres\t7500000\nportal\t12345678", nil
	}
	list, err := c.Databases(ctx)
	if err != nil || len(list) != 1 || list[0].Size == nil || *list[0].Size != 12345678 {
		t.Fatalf("lista %+v %v", list, err)
	}
	list[0].Size = nil
	if !reflect.DeepEqual(list[0], info) {
		t.Errorf("lista %+v, criado %+v", list[0], info)
	}
	// postgres fora do ar: lista sem o tamanho
	c.deps.ExecInput = func(context.Context, string, []byte, ...string) (string, error) { return "", errors.New("parado") }
	if list, err := c.Databases(ctx); err != nil || len(list) != 1 || list[0].Size != nil {
		t.Errorf("sem o postgres %+v %v", list, err)
	}

	c.infra.Lock()
	_, err = c.CreateDatabase(ctx, DatabaseRequest{Engine: "postgres", Name: "outro"})
	c.infra.Unlock()
	if !errors.Is(err, ErrBusy) {
		t.Errorf("durante o Setup: %v", err)
	}
}

func TestDeleteAppSaiDoEstado(t *testing.T) {
	c, d, _ := newAppsCore(t)
	ctx := context.Background()
	c.CreateApp(ctx, apps.App{Name: "meu-app", Repo: "trdSystemDev/meu-app"})
	if err := c.DeleteApp(ctx, "meu-app", false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(d.calls, ","), "down meu-app false") {
		t.Errorf("chamadas %v", d.calls)
	}
	if s := snap(t, c).Apps; len(s.List) != 0 {
		t.Fatalf("app continua no estado: %+v", s.List)
	}
	// o nome fica livre pra criar de novo
	if _, err := c.CreateApp(ctx, apps.App{Name: "meu-app", Repo: "trdSystemDev/meu-app"}); err != nil {
		t.Fatal(err)
	}
}

func TestBancoLigadoNoApp(t *testing.T) {
	c, _, _ := newAppsCore(t)
	ctx := context.Background()
	c.deps.ExecInput = func(context.Context, string, []byte, ...string) (string, error) { return "", nil }
	c.CreateApp(ctx, apps.App{Name: "meu-app", Repo: "trdSystemDev/meu-app"})
	if _, err := c.CreateDatabase(ctx, DatabaseRequest{Engine: "postgres", Name: "meu_app"}); err != nil {
		t.Fatal(err)
	}
	if err := c.LinkAppDatabase(ctx, "meu-app", "postgres", "meu_app"); err != nil {
		t.Fatal(err)
	}
	s := snap(t, c).Apps.List[0]
	if len(s.Databases) != 1 || s.Databases[0].ID() != "postgres/meu_app" || len(s.Secrets) != 2 || s.Secrets[0].Infra == "" {
		t.Fatalf("estado do app %+v %+v", s.Databases, s.Secrets)
	}
	list, _ := c.Databases(ctx)
	if len(list) != 1 || !reflect.DeepEqual(list[0].Apps, []string{"meu-app"}) {
		t.Errorf("lista %+v", list)
	}
	// excluir o app nao apaga o database
	c.DeleteApp(ctx, "meu-app", false)
	if list, _ := c.Databases(ctx); len(list) != 1 || len(list[0].Apps) != 0 {
		t.Errorf("depois de excluir o app %+v", list)
	}
}

func TestCreateBucket(t *testing.T) {
	c, _, _ := newAppsCore(t)
	ctx := context.Background()
	var calls []string
	c.deps.Exec = func(_ context.Context, container string, args ...string) (string, error) {
		calls = append(calls, container+" "+strings.Join(args, " "))
		switch {
		case len(args) > 2 && args[1] == "key":
			return "Key ID:              GK5d0e\nSecret key:          6c9b79\n", nil
		case len(args) > 2 && args[2] == "info":
			return "Size:            1.2 MB (1.1 MiB)\n", nil
		}
		return "", nil
	}
	if _, err := c.CreateBucket(ctx, "fotos"); err == nil || !strings.Contains(err.Error(), "desligada") {
		t.Errorf("garage desligado: %v", err)
	}
	cfg, err := config.Load(c.deps.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Stacks = append(cfg.Stacks, "garage")
	if err := c.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	info, err := c.CreateBucket(ctx, "portal-fotos")
	if err != nil || info.Endpoint != "http://garage:3900" ||
		!reflect.DeepEqual(info.Secrets, []string{"s3_portal_fotos_access_key", "s3_portal_fotos_secret_key"}) {
		t.Fatalf("%+v %v", info, err)
	}
	if _, err := c.CreateBucket(ctx, "portal-fotos"); err == nil || !strings.Contains(err.Error(), "ja existe") {
		t.Errorf("repetido: %v", err)
	}
	if _, err := c.CreateBucket(ctx, "Nome_Ruim"); err == nil {
		t.Error("aceitou nome invalido")
	}

	c.CreateApp(ctx, apps.App{Name: "meu-app", Repo: "trdSystemDev/meu-app"})
	if err := c.LinkAppBucket(ctx, "meu-app", "portal-fotos"); err != nil {
		t.Fatal(err)
	}
	list, err := c.Buckets(ctx)
	if err != nil || len(list) != 1 || list[0].Name != "portal-fotos" || list[0].Size != "1.2 MB" ||
		!reflect.DeepEqual(list[0].Apps, []string{"meu-app"}) {
		t.Fatalf("lista %+v %v", list, err)
	}
	s := snap(t, c).Apps.List[0]
	if len(s.Buckets) != 1 || len(s.Secrets) != 2 || s.Secrets[0].Infra != "s3/portal-fotos" {
		t.Errorf("estado do app %+v %+v", s.Buckets, s.Secrets)
	}
	// a secret key nunca vai na linha de comando
	for _, call := range calls {
		if strings.Contains(call, "6c9b79") {
			t.Errorf("secret key na linha de comando: %s", call)
		}
	}
}

func TestDeleteDatabaseEBucket(t *testing.T) {
	c, _, _ := newAppsCore(t)
	ctx := context.Background()
	var sql []string
	c.deps.ExecInput = func(_ context.Context, _ string, input []byte, _ ...string) (string, error) {
		sql = append(sql, string(input))
		return "", nil
	}
	var garage []string
	c.deps.Exec = func(_ context.Context, _ string, args ...string) (string, error) {
		garage = append(garage, strings.Join(args, " "))
		if len(args) > 2 && args[1] == "key" && args[2] == "create" {
			return "Key ID:              GK5d0e\nSecret key:          6c9b79\n", nil
		}
		return "", nil
	}
	cfg, _ := config.Load(c.deps.ConfigPath)
	cfg.Stacks = append(cfg.Stacks, "garage")
	c.SaveConfig(ctx, cfg)
	c.CreateApp(ctx, apps.App{Name: "meu-app", Repo: "trdSystemDev/meu-app"})
	c.CreateDatabase(ctx, DatabaseRequest{Engine: "postgres", Name: "loja"})
	c.CreateBucket(ctx, "loja-fotos")
	c.LinkAppDatabase(ctx, "meu-app", "postgres", "loja")
	c.LinkAppBucket(ctx, "meu-app", "loja-fotos")

	// ligados ao app: recusa dizendo qual
	if err := c.DeleteDatabase(ctx, "postgres", "loja"); err == nil || !strings.Contains(err.Error(), "ligado a meu-app") {
		t.Errorf("banco ligado: %v", err)
	}
	if err := c.DeleteBucket(ctx, "loja-fotos"); err == nil || !strings.Contains(err.Error(), "ligado a meu-app") {
		t.Errorf("bucket ligado: %v", err)
	}
	c.UnlinkAppDatabase(ctx, "meu-app", "postgres", "loja")
	c.UnlinkAppBucket(ctx, "meu-app", "loja-fotos")

	if err := c.DeleteDatabase(ctx, "postgres", "loja"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql[len(sql)-1], `DROP DATABASE IF EXISTS "loja" WITH (FORCE)`) {
		t.Errorf("sql %q", sql[len(sql)-1])
	}
	if err := c.DeleteBucket(ctx, "loja-fotos"); err != nil {
		t.Fatal(err)
	}
	if n := len(garage); garage[n-2] != "/garage bucket delete --yes loja-fotos" || garage[n-1] != "/garage key delete --yes GK5d0e" {
		t.Errorf("garage %v", garage)
	}
	if dbs, _ := c.Databases(ctx); len(dbs) != 0 {
		t.Errorf("banco continua na lista: %+v", dbs)
	}
	if bks, _ := c.Buckets(ctx); len(bks) != 0 {
		t.Errorf("bucket continua na lista: %+v", bks)
	}
	if err := c.DeleteBucket(ctx, "loja-fotos"); err == nil || !strings.Contains(err.Error(), "nao existe") {
		t.Errorf("de novo: %v", err)
	}
	// o nome fica livre pra criar de novo
	if _, err := c.CreateDatabase(ctx, DatabaseRequest{Engine: "postgres", Name: "loja"}); err != nil {
		t.Error(err)
	}
}
