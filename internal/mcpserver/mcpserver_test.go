package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/docker"
	"github.com/trdSystemDev/cais/internal/github/githubtest"
	"github.com/trdSystemDev/cais/internal/host"
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
func (f *fakeDocker) Build(context.Context, docker.Project, io.Writer) error { return nil }
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

// logsAsked e o ultimo pedido de log que chegou ao docker falso.
var logsAsked docker.LogsOptions

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
		CheckDocker: func(context.Context) docker.Status { return docker.Status{} },
		// a VPS da checagem de seguranca: o Tailscale esperando o login
		CheckHost: func(context.Context) []host.Item {
			return []host.Item{
				{ID: "ssh-root", Title: "Login do root pelo SSH", OK: true, Detail: "bloqueado", Fix: "sudo cais host harden"},
				{ID: "tailscale", Title: "Tailscale (VPN pra administrar a VPS)", Detail: "instalado, fora da tailnet (NeedsLogin)", Fix: host.TailscaleFix},
			}
		},
		TailscaleStatus: func(context.Context) host.Tailscale {
			return host.Tailscale{Installed: true, State: "NeedsLogin", AuthURL: "https://login.tailscale.com/a/segredo123"}
		},
		Version:       "v0.5.0",
		LatestRelease: func(context.Context) (string, error) { return "v0.6.0", nil },
		ConfigPath:    filepath.Join(root, "config.yaml"),
		SecretsDir:    filepath.Join(root, "secrets"),
		StackDir:      func(n string) string { return filepath.Join(root, "stacks", n) },
		AppsDir:       filepath.Join(root, "apps"),
		AppDocker:     &fakeDocker{up: map[string]bool{}},
		AppSettle:     -1,
		GitHubURL:     gh.URL,
		ComposePS:     func(context.Context, string) ([]docker.Container, error) { return nil, nil },
		ContainerLogs: func(_ context.Context, name string, opt docker.LogsOptions) (string, error) {
			logsAsked = opt
			// 3 linhas como o docker logs --timestamps devolve, com as cores
			// do terminal
			return "2026-11-26T13:09:53.474428458Z log do " + name + "\n" +
				"2026-11-26T13:09:54.000000001Z \x1b[31mERR\x1b[0m conexao recusada\n" +
				"2026-11-26T13:09:55.5Z pronto\n", nil
		},
		ExecInput: func(context.Context, string, []byte, ...string) (string, error) { return "", nil },
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
		"databases_list", "infra_logs", "infra_status", "security_check", "status",
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

	// container errado: o erro lista os do app
	call(t, cs, "app_create", map[string]any{"name": "loja", "repo": "trdSystemDev/loja"}, false)
	if out := call(t, cs, "app_logs", map[string]any{"app": "loja", "container": "loja-web-2"}, true); !strings.Contains(out, "faca o deploy") {
		t.Errorf("container sem deploy: %s", out)
	}
	call(t, cs, "app_deploy", map[string]any{"app": "loja"}, false)
	if out := call(t, cs, "app_logs", map[string]any{"app": "loja", "container": "loja-web-2"}, true); !strings.Contains(out, "os dele: loja-web-1") {
		t.Errorf("container errado: %s", out)
	}
	if out := call(t, cs, "app_promote_check", map[string]any{"app": "loja"}, true); !strings.Contains(out, "promote_to") {
		t.Errorf("promote_check fora da homologacao: %s", out)
	}
	// sem nada criado: lista vazia, nao null
	for _, tool := range []string{"databases_list", "buckets_list"} {
		if out := call(t, cs, tool, nil, false); out != "[]" {
			t.Errorf("%s vazio: %q", tool, out)
		}
	}
}

// "esta tudo certo com a VPS?": o security_check roda a checagem e devolve
// so a seguranca, os problemas primeiro; o link de login do Tailscale nunca
// sai pelo MCP
func TestSecurityCheck(t *testing.T) {
	cs := connect(t)
	out := call(t, cs, "security_check", nil, false)
	var r securityReport
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if r.Summary != "1 de 2 itens pra corrigir; ha versao nova do cais (v0.6.0)" || len(r.Failed) != 1 || r.Failed[0].ID != "tailscale" ||
		r.Failed[0].Fix == "" || len(r.OK) != 1 || !strings.Contains(r.Where, "Setup > Seguranca") || r.CheckedAt.IsZero() {
		t.Errorf("relatorio: %+v", r)
	}
	// conserto so no que esta errado, aqui e no status
	if r.OK[0].Fix != "" {
		t.Errorf("item certo com conserto: %+v", r.OK[0])
	}
	if out := call(t, cs, "status", nil, false); strings.Contains(out, "cais host harden") {
		t.Errorf("status com conserto de item certo: %s", out)
	}
	if !r.Tailscale.Installed || r.Tailscale.State != "NeedsLogin" {
		t.Errorf("tailscale: %+v", r.Tailscale)
	}
	for _, tool := range []string{"security_check", "status"} {
		if out := call(t, cs, tool, nil, false); strings.Contains(out, "segredo123") {
			t.Errorf("%s devolveu o link de login do Tailscale: %s", tool, out)
		}
	}
}

// os logs chegam limpos pra IA: sem cores, horario ate o segundo, com lines,
// since e grep
func TestLogsParaIA(t *testing.T) {
	cs := connect(t)
	call(t, cs, "app_create", map[string]any{"name": "loja", "repo": "trdSystemDev/loja"}, false)
	call(t, cs, "app_deploy", map[string]any{"app": "loja"}, false)
	args := func(extra map[string]any) map[string]any {
		m := map[string]any{"app": "loja", "container": "loja-web-1"}
		maps.Copy(m, extra)
		return m
	}

	out := call(t, cs, "app_logs", args(nil), false)
	want := "2026-11-26T13:09:53Z log do loja-web-1\n2026-11-26T13:09:54Z ERR conexao recusada\n2026-11-26T13:09:55Z pronto\n"
	if out != want {
		t.Errorf("logs:\n%q\nquer\n%q", out, want)
	}
	if logsAsked != (docker.LogsOptions{Tail: 50}) {
		t.Errorf("pedido ao docker: %+v", logsAsked)
	}
	if out := call(t, cs, "app_logs", args(map[string]any{"lines": 1}), false); out != "2026-11-26T13:09:55Z pronto\n" {
		t.Errorf("lines 1: %q", out)
	}
	// o grep procura numa janela maior e devolve so as linhas com o texto
	if out := call(t, cs, "app_logs", args(map[string]any{"grep": "err", "since": "2h"}), false); out != "2026-11-26T13:09:54Z ERR conexao recusada\n" {
		t.Errorf("grep: %q", out)
	}
	if logsAsked != (docker.LogsOptions{Tail: grepWindow, Since: "2h"}) {
		t.Errorf("pedido ao docker com grep: %+v", logsAsked)
	}
	if out := call(t, cs, "app_logs", args(map[string]any{"grep": "timeout"}), false); !strings.Contains(out, "nenhuma linha") {
		t.Errorf("grep sem achar: %q", out)
	}
	for _, bad := range []map[string]any{{"lines": 5000}, {"lines": -1}, {"since": "ontem"}, {"since": "-5m"}} {
		call(t, cs, "app_logs", args(bad), true)
	}
}

// as respostas mandam a IA pras ferramentas deste MCP, nao pra TUI ou CLI
func TestRespostasApontamProMCP(t *testing.T) {
	cs := connect(t)
	call(t, cs, "app_create", map[string]any{"name": "loja", "repo": "trdSystemDev/loja"}, false)
	guide := call(t, cs, "app_guide", map[string]any{"app": "loja"}, false)
	for _, s := range []string{"database_create e depois app_link_database", "bucket_create e depois app_link_bucket"} {
		if !strings.Contains(guide, s) {
			t.Errorf("guia sem %q:\n%s", s, guide)
		}
	}
	for _, s := range []string{"aba Infra", "cais app db", "cais app bucket", "Apps ->"} {
		if strings.Contains(guide, s) {
			t.Errorf("guia com %q (caminho da TUI/CLI):\n%s", s, guide)
		}
	}
	out := call(t, cs, "app_link_database", map[string]any{"app": "loja", "engine": "postgres", "name": "nao_existe"}, true)
	if !strings.Contains(out, "crie com database_create") {
		t.Errorf("link de banco inexistente: %s", out)
	}
	out = call(t, cs, "app_link_bucket", map[string]any{"app": "loja", "bucket": "nao-existe"}, true)
	if !strings.Contains(out, "crie com bucket_create") {
		t.Errorf("link de bucket inexistente: %s", out)
	}
	// mysql desligado no teste: ligar e com o usuario
	out = call(t, cs, "database_create", map[string]any{"engine": "mysql", "name": "loja"}, true)
	if !strings.Contains(out, "peca ao usuario") || !strings.Contains(out, "cais service mysql on") {
		t.Errorf("stack desligada: %s", out)
	}
}

func TestRewriteForAI(t *testing.T) {
	err := (&apps.ComposeError{App: "loja", Problems: []string{"web: publica a porta 80 no host (porta 80). Use os labels (cais app guide loja)"}}).Error()
	got := rewriteForAI(err)
	if strings.Contains(got, "cais app guide") || strings.Count(got, "app_guide (app loja)") != 2 {
		t.Errorf("erro do compose: %s", got)
	}
	got = rewriteForAI("a rede mysql_network e do mysql, que esta desligado. Ligue o mysql na aba Infra (ou cais service mysql on)")
	if !strings.Contains(got, "Ligar o mysql nao e pelo MCP: peca ao usuario") {
		t.Errorf("rede de stack desligada: %s", got)
	}
	// no JSON: a string trocada no lugar, a ordem dos campos mantida
	raw := json.RawMessage(`{"z":1,"history":[{"error":"x\n\nModelo pronto pra colar: cais app guide loja"}],"a":"crie na aba Infra -\u003e Novo banco"}`)
	out, changed := rewriteJSON(raw)
	want := `{"z":1,"history":[{"error":"x\n\nModelo pronto pra colar: app_guide (app loja)"}],"a":"crie com database_create"}`
	if !changed || string(out) != want {
		t.Errorf("json:\n%s\nquer\n%s", out, want)
	}
	if _, changed := rewriteJSON(json.RawMessage(`{"a":"nada a trocar"}`)); changed {
		t.Error("trocou JSON sem referencia da TUI")
	}
}
