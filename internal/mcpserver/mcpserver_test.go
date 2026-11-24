package mcpserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/docker"
	"github.com/trdSystemDev/cais/internal/github/githubtest"
)

// fakeDocker finge o docker compose dos apps: o up deixa um container
// saudavel no ar.
type fakeDocker struct {
	mu sync.Mutex
	up map[string]bool
}

func (f *fakeDocker) Config(context.Context, docker.Project) ([]byte, error) {
	return []byte(`{"services": {}}`), nil
}
func (f *fakeDocker) Build(context.Context, docker.Project) error { return nil }
func (f *fakeDocker) Up(_ context.Context, p docker.Project) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.up[p.Name] = true
	return nil
}
func (f *fakeDocker) PS(_ context.Context, p docker.Project) ([]docker.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.up[p.Name] {
		return nil, nil
	}
	return []docker.Container{{Name: p.Name + "-web-1", Service: "web", State: "running", Health: "healthy"}}, nil
}
func (f *fakeDocker) Restart(context.Context, docker.Project, string) error { return nil }
func (f *fakeDocker) Stop(context.Context, docker.Project) error            { return nil }
func (*fakeDocker) TagImage(context.Context, string, string) error          { return nil }
func (*fakeDocker) HasImage(context.Context, string) bool                   { return false }
func (*fakeDocker) ImageTags(context.Context, string) ([]string, error)     { return nil, nil }
func (*fakeDocker) RemoveImages(context.Context, []string) error            { return nil }
func (*fakeDocker) PruneImages(context.Context, string) error               { return nil }

func (f *fakeDocker) Rollout(context.Context, docker.Project, string, string, bool) error { return nil }
func (f *fakeDocker) Down(context.Context, string, bool) error                            { return nil }

// connect sobe o servidor MCP sobre um core de teste (Postgres e Garage
// ligados, GitHub App falso com o repositorio trdSystemDev/loja) e devolve
// um cliente MCP conectado nele.
func connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	gh := githubtest.New(t, 5512022, map[string]map[string]string{
		"trdSystemDev/loja": {"docker-compose.yml": "services: {web: {build: .}}\n", "Dockerfile": "FROM nginx\n"},
	})
	c := core.New(core.Deps{
		CheckDocker:   func(context.Context) docker.Status { return docker.Status{} },
		ConfigPath:    filepath.Join(root, "config.yaml"),
		SecretsDir:    filepath.Join(root, "secrets"),
		StackDir:      func(n string) string { return filepath.Join(root, "stacks", n) },
		AppsDir:       filepath.Join(root, "apps"),
		AppDocker:     &fakeDocker{up: map[string]bool{}},
		AppSettle:     -1,
		GitHubURL:     gh.URL,
		ComposePS:     func(context.Context, string) ([]docker.Container, error) { return nil, nil },
		ContainerLogs: func(_ context.Context, name string) (string, error) { return "log do " + name, nil },
		ExecInput:     func(context.Context, string, []byte, ...string) (string, error) { return "", nil },
		Exec: func(_ context.Context, _ string, args ...string) (string, error) {
			if strings.Contains(strings.Join(args, " "), "key create") {
				return "Key ID:              GK5d0e\nSecret key:          6c9b79\n", nil
			}
			return "", nil
		},
	})
	cfg := config.Default()
	cfg.TestDomain, cfg.Email, cfg.TLS = "trd.localhost", "a@trd.localhost", config.TLSSelfSigned
	cfg.Stacks, cfg.Hosts.S3 = []string{"proxy", "postgres", "garage"}, "s3.trd.localhost"
	cfg.GitHub.AppID = gh.AppID
	if err := c.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := c.SetSecret(ctx, core.GitHubKeySecret, string(gh.Key)); err != nil {
		t.Fatal(err)
	}

	st, ct := mcp.NewInMemoryTransports()
	if _, err := New(c, "teste").Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "teste"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// call chama a ferramenta e devolve o texto da resposta; falha o teste se
// o resultado de erro nao for o esperado.
func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, wantErr bool) string {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var out strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			out.WriteString(tc.Text)
		}
	}
	if res.IsError != wantErr {
		t.Fatalf("%s: erro=%v, quer %v: %s", name, res.IsError, wantErr, out.String())
	}
	return out.String()
}

// as ferramentas sao exatamente estas: nada que apaga dados
func TestFerramentasSemExclusao(t *testing.T) {
	cs := connect(t)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		if tool.Description == "" || tool.Annotations == nil {
			t.Errorf("%s sem descricao ou anotacoes", tool.Name)
		}
		if tool.Annotations != nil && tool.Annotations.DestructiveHint != nil && *tool.Annotations.DestructiveHint {
			t.Errorf("%s marcada como destrutiva", tool.Name)
		}
	}
	slices.Sort(names)
	want := []string{
		"app_create", "app_deploy", "app_env_get", "app_env_set", "app_guide", "app_link_bucket", "app_link_database",
		"app_logs", "app_promote", "app_promote_check", "app_restart", "app_rollback", "app_stop", "app_unlink_bucket",
		"app_unlink_database", "app_update", "apps_list", "bucket_create", "buckets_list", "database_create",
		"databases_list", "infra_logs", "infra_status", "status",
	}
	if !slices.Equal(names, want) {
		t.Errorf("ferramentas\n got %v\nwant %v", names, want)
	}
	for _, n := range names {
		for _, proibido := range []string{"delete", "remove", "disable", "config", "setup", "service"} {
			if strings.Contains(n, proibido) {
				t.Errorf("ferramenta %s nao devia existir", n)
			}
		}
	}
}

// o fluxo de um app novo pela IA, do cadastro ao deploy
func TestFluxoDoApp(t *testing.T) {
	cs := connect(t)
	call(t, cs, "app_create", map[string]any{"name": "loja", "repo": "trdSystemDev/loja"}, false)
	if g := call(t, cs, "app_guide", map[string]any{"app": "loja"}, false); !strings.Contains(g, "traefik") {
		t.Errorf("ajuda sem labels do traefik:\n%s", g)
	}

	set := call(t, cs, "app_env_set", map[string]any{"app": "loja", "set": map[string]any{"NODE_ENV": "production", "VELHA": "x",
		"JWT_SECRET": "valor-que-nao-volta"}}, false)
	out := call(t, cs, "app_env_set", map[string]any{"app": "loja", "unset": []any{"VELHA"}}, false)
	if !strings.Contains(out, "NODE_ENV") || strings.Contains(out, "VELHA") {
		t.Errorf("env_set: %s", out)
	}
	call(t, cs, "database_create", map[string]any{"engine": "postgres", "name": "loja"}, false)
	call(t, cs, "app_link_database", map[string]any{"app": "loja", "engine": "postgres", "name": "loja"}, false)
	call(t, cs, "bucket_create", map[string]any{"name": "loja-fotos"}, false)
	call(t, cs, "app_link_bucket", map[string]any{"app": "loja", "bucket": "loja-fotos"}, false)

	// app_env_get: so os nomes, os do app e os dos bancos e buckets ligados
	got := call(t, cs, "app_env_get", map[string]any{"app": "loja"}, false)
	var env struct{ Vars, Infra []string }
	json.Unmarshal([]byte(got), &env)
	if !slices.Equal(env.Vars, []string{"JWT_SECRET", "NODE_ENV"}) || !slices.Contains(env.Infra, "POSTGRES_LOJA_URL") ||
		!slices.Contains(env.Infra, "S3_LOJA_FOTOS_SECRET_KEY") {
		t.Errorf("env_get %s", got)
	}
	// nenhuma resposta traz valor de variavel ou credencial guardada
	all := set + out + got + call(t, cs, "app_guide", map[string]any{"app": "loja"}, false) + call(t, cs, "databases_list", nil, false) +
		call(t, cs, "buckets_list", nil, false) + call(t, cs, "status", nil, false)
	for _, valor := range []string{"valor-que-nao-volta", "production", "6c9b79"} {
		if strings.Contains(all, valor) {
			t.Errorf("valor de variavel na resposta: %s", valor)
		}
	}

	if out := call(t, cs, "app_deploy", map[string]any{"app": "loja"}, false); !strings.Contains(out, `"commit"`) {
		t.Errorf("deploy: %s", out)
	}
	if out := call(t, cs, "apps_list", nil, false); !strings.Contains(out, "loja-web-1") {
		t.Errorf("apps_list sem o container: %s", out)
	}
	if out := call(t, cs, "app_logs", map[string]any{"app": "loja", "container": "loja-web-1"}, false); !strings.Contains(out, "log do loja-web-1") {
		t.Errorf("logs: %s", out)
	}
	call(t, cs, "app_update", map[string]any{"app": "loja", "branch": "cais"}, false)
	if out := call(t, cs, "apps_list", nil, false); !strings.Contains(out, `"branch":"cais"`) {
		t.Errorf("branch nao mudou: %s", out)
	}
}

// erro da acao volta como resultado de erro da ferramenta, com o motivo
func TestErroVoltaComMotivo(t *testing.T) {
	cs := connect(t)
	if out := call(t, cs, "app_deploy", map[string]any{"app": "nao-existe"}, true); !strings.Contains(out, "nao-existe") {
		t.Errorf("erro sem o motivo: %s", out)
	}
	if out := call(t, cs, "app_env_set", map[string]any{"app": "loja"}, true); !strings.Contains(out, "set ou unset") {
		t.Errorf("env_set vazio: %s", out)
	}
	// argumento obrigatorio faltando: recusado pelo schema
	call(t, cs, "app_logs", map[string]any{"app": "loja"}, true)
}
