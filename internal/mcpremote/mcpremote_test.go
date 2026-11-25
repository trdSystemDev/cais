package mcpremote

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/core/coretest"
	"github.com/trdSystemDev/cais/internal/host"
	"github.com/trdSystemDev/cais/internal/mcpserver"
	"github.com/trdSystemDev/cais/internal/remotes"
)

// tres VPS: prod e dev respondem (o MCP do cais de verdade sobre um core
// que so grava as chamadas); fora nao abre a conexao
func connect(t *testing.T) (*mcp.ClientSession, map[string]*coretest.Recorder, *atomic.Int32) {
	t.Helper()
	ctx := context.Background()
	cores := map[string]*coretest.Recorder{"prod": coretest.NewRecorder(), "dev": coretest.NewRecorder()}
	cores["prod"].SetOut("CheckHealth", core.Health{Version: "v0.5.12", Latest: "v0.5.12"})
	cores["prod"].SetOut("CheckEnvironment", core.Environment{Security: []host.Item{{ID: "firewall", OK: true}}})
	cores["dev"].SetOut("CheckHealth", core.Health{Version: "v0.5.10", Latest: "v0.5.12"})
	cores["dev"].SetOut("CheckEnvironment", core.Environment{Security: []host.Item{
		{ID: "firewall", OK: true}, {ID: "tailscale-key", Detail: "vence em 24/05/2027", Fix: host.TailscaleKeyFix}}})
	dials := &atomic.Int32{}
	dial := func(ctx context.Context, r remotes.Remote) (mcp.Transport, error) {
		dials.Add(1)
		rec, ok := cores[r.Name]
		if !ok {
			return nil, errors.New("ssh: connect to host fora: Connection timed out")
		}
		st, ct := mcp.NewInMemoryTransports()
		if _, err := mcpserver.New(rec, "v0.5.12").Connect(ctx, st, nil); err != nil {
			return nil, err
		}
		return ct, nil
	}
	list := []remotes.Remote{{Name: "prod", Target: "cais@prod"}, {Name: "dev", Target: "cais@dev"}, {Name: "fora", Target: "cais@fora"}}
	s, closeAll, err := New(ctx, list, dial, "v0.5.12")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeAll)
	st, ct := mcp.NewInMemoryTransports()
	if _, err := s.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "teste"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs, cores, dials
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

func methods(r *coretest.Recorder) []string {
	var out []string
	for _, c := range r.Take() {
		out = append(out, c.Method)
	}
	return out
}

// as ferramentas sao as de uma VPS, cada uma com o vps (obrigatorio, menos
// no security_check), mais o vps_list
func TestFerramentas(t *testing.T) {
	cs, _, _ := connect(t)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	one, _ := mcpserver.Tools(context.Background(), "v")
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		if tool.Name == "vps_list" {
			continue
		}
		b, _ := json.Marshal(tool.InputSchema)
		var s struct {
			Properties map[string]struct {
				Enum []string `json:"enum"`
			} `json:"properties"`
			Required []string `json:"required"`
		}
		json.Unmarshal(b, &s)
		if !slices.Equal(s.Properties["vps"].Enum, []string{"prod", "dev", "fora"}) {
			t.Errorf("%s sem o vps: %s", tool.Name, b)
		}
		if slices.Contains(s.Required, "vps") == (tool.Name == "security_check") {
			t.Errorf("%s: vps obrigatorio = %v", tool.Name, slices.Contains(s.Required, "vps"))
		}
	}
	if len(names) != len(one)+1 || !slices.Contains(names, "vps_list") || !slices.Contains(names, "app_deploy") {
		t.Errorf("ferramentas: %v", names)
	}
}

// cada chamada vai so pra VPS do vps; sem ele (ou com uma que nao existe),
// recusa e diz quais existem
func TestRepassaPraVPSCerta(t *testing.T) {
	cs, cores, dials := connect(t)
	out, isErr := call(t, cs, "app_deploy", map[string]any{"app": "loja"})
	if !isErr || !strings.Contains(out, "falta vps") || !strings.Contains(out, "prod, dev, fora") {
		t.Fatalf("sem vps: %v %s", isErr, out)
	}
	if out, isErr := call(t, cs, "app_deploy", map[string]any{"vps": "homolog", "app": "loja"}); !isErr || !strings.Contains(out, `"homolog" nao existe`) {
		t.Fatalf("vps errada: %s", out)
	}
	if dials.Load() != 0 {
		t.Fatal("conectou sem vps valida")
	}
	if _, isErr := call(t, cs, "app_deploy", map[string]any{"vps": "dev", "app": "loja"}); isErr {
		t.Fatal("deploy na dev falhou")
	}
	if got := methods(cores["dev"]); !slices.Equal(got, []string{"DeployApp"}) {
		t.Errorf("dev: %v", got)
	}
	if got := methods(cores["prod"]); len(got) != 0 {
		t.Errorf("a prod recebeu: %v", got)
	}
	// a conexao fica aberta: a segunda chamada nao conecta de novo
	call(t, cs, "apps_list", map[string]any{"vps": "dev"})
	if dials.Load() != 1 {
		t.Errorf("conexoes: %d", dials.Load())
	}
	if out, isErr := call(t, cs, "apps_list", map[string]any{"vps": "fora"}); !isErr || !strings.Contains(out, "a VPS fora (cais@fora) nao respondeu") {
		t.Errorf("fora do ar: %s", out)
	}
}

// "como estao as VPS?": o vps_list e o security_check sem vps conferem
// todas; uma fora do ar aparece com o motivo, sem segurar as outras
func TestTodasAsVPS(t *testing.T) {
	cs, _, _ := connect(t)
	out, isErr := call(t, cs, "vps_list", nil)
	var l vpsList
	if isErr || json.Unmarshal([]byte(out), &l) != nil || len(l.VPS) != 3 {
		t.Fatalf("vps_list: %s", out)
	}
	by := map[string]vpsSummary{}
	for _, v := range l.VPS {
		by[v.Name] = v
	}
	if v := by["dev"]; !v.Online || v.Version != "v0.5.10" || !v.UpdateAvailable || v.Failed != 1 || !strings.Contains(v.Security, "1 de 2") {
		t.Errorf("dev: %+v", v)
	}
	if v := by["prod"]; !v.Online || v.Failed != 0 || !strings.HasPrefix(v.Security, "tudo certo") {
		t.Errorf("prod: %+v", v)
	}
	if v := by["fora"]; v.Online || !strings.Contains(v.Error, "Connection timed out") {
		t.Errorf("fora: %+v", v)
	}
	if l.VPS[0].Name != "dev" {
		t.Errorf("a com problema primeiro: %v", l.VPS)
	}

	out, isErr = call(t, cs, "security_check", nil)
	if isErr || !strings.Contains(out, `"vps":"dev"`) || !strings.Contains(out, "Disable key expiry") || !strings.Contains(out, `"vps":"fora"`) {
		t.Errorf("security_check de todas: %s", out)
	}
	out, isErr = call(t, cs, "security_check", map[string]any{"vps": "prod"})
	if isErr || !strings.Contains(out, "tudo certo") || strings.Contains(out, "Disable key expiry") {
		t.Errorf("security_check da prod: %s", out)
	}
}
