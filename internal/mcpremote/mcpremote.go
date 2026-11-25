// Package mcpremote e o MCP de varias VPS num so: roda no computador de quem
// administra (cais mcp --remotes) e repassa cada chamada pro cais mcp da VPS
// escolhida, pelo SSH dela. As ferramentas sao as de uma VPS, cada uma com o
// argumento vps; as regras sao as de cada VPS (nada que apaga dados, a
// seguranca so leitura). Mais o vps_list e o security_check de todas.
package mcpremote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/trdSystemDev/cais/internal/mcpserver"
	"github.com/trdSystemDev/cais/internal/remotes"
)

// Dialer abre o transporte pro cais mcp de uma VPS.
type Dialer func(ctx context.Context, r remotes.Remote) (mcp.Transport, error)

// SSH e o Dialer de verdade: ssh <destino> cais mcp, sem perguntar nada
// (o MCP nao tem terminal: a chave tem que entrar sozinha). O stderr do ssh
// fica guardado: e ele que diz por que nao conectou.
func SSH(_ context.Context, r remotes.Remote) (mcp.Transport, error) {
	cmd := exec.Command("ssh", remotes.SSHArgs(r.Target, true, "cais", "mcp")...)
	t := &sshTransport{CommandTransport: mcp.CommandTransport{Command: cmd}}
	cmd.Stderr = &t.stderr
	return t, nil
}

type sshTransport struct {
	mcp.CommandTransport
	stderr lockedBuffer
}

// why e a ultima linha do stderr do ssh.
func (t *sshTransport) why() string {
	s := strings.TrimSpace(t.stderr.String())
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return s
}

// lockedBuffer e o stderr do ssh: o processo escreve enquanto a conexao le.
type lockedBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.b) < 64<<10 {
		l.b = append(l.b, p...)
	}
	return len(p), nil
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return string(l.b)
}

// connectTimeout e quanto espera a VPS responder ao abrir a conexao.
var connectTimeout = 30 * time.Second

type hub struct {
	list    []remotes.Remote
	dial    Dialer
	version string

	mu       sync.Mutex
	sessions map[string]*mcp.ClientSession
}

const instructions = `Este MCP fala com VARIAS VPS com o cais. Toda ferramenta recebe vps, o
nome da VPS (vps_list mostra quais existem, com a versao e o resumo da
seguranca de cada uma). Na duvida de qual VPS, pergunte ao usuario: um deploy
na VPS errada e um deploy em producao. security_check sem vps confere todas.

O resto vale pra cada VPS:

`

// New monta o MCP das VPS da lista. As conexoes abrem na primeira chamada a
// cada VPS e ficam abertas; Close fecha todas.
func New(ctx context.Context, list []remotes.Remote, dial Dialer, version string) (*mcp.Server, func(), error) {
	if len(list) == 0 {
		return nil, nil, errors.New("nenhuma VPS: cais remote add NOME cais@vps")
	}
	tools, err := mcpserver.Tools(ctx, version)
	if err != nil {
		return nil, nil, err
	}
	h := &hub{list: list, dial: dial, version: version, sessions: map[string]*mcp.ClientSession{}}
	s := mcp.NewServer(&mcp.Implementation{Name: "cais-vps", Version: version},
		&mcp.ServerOptions{Instructions: instructions + mcpserver.Instructions()})
	for _, t := range tools {
		t2, err := h.withVPS(t)
		if err != nil {
			return nil, nil, err
		}
		s.AddTool(t2, h.forward(t.Name))
	}
	mcp.AddTool(s, &mcp.Tool{Name: "vps_list", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: "As VPS com o cais: nome, destino do SSH e, de cada uma, a versao do cais e o resumo da checagem de seguranca (ou por que nao respondeu)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, vpsList, error) {
			return nil, h.vpsList(ctx), nil
		})
	return s, h.close, nil
}

// withVPS e a ferramenta com o argumento vps (obrigatorio, menos no
// security_check, que sem ele confere todas). Sem o esquema de saida: a
// resposta e a da VPS, repassada como veio.
func (h *hub) withVPS(t *mcp.Tool) (*mcp.Tool, error) {
	var schema map[string]any
	b, err := json.Marshal(t.InputSchema)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &schema); err != nil {
		return nil, err
	}
	props, _ := schema["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
	}
	desc := "a VPS: " + strings.Join(remotes.Names(h.list), ", ")
	if t.Name == "security_check" {
		desc += " (vazio: todas)"
	}
	props["vps"] = map[string]any{"type": "string", "enum": remotes.Names(h.list), "description": desc}
	schema["properties"] = props
	if t.Name != "security_check" {
		req, _ := schema["required"].([]any)
		schema["required"] = append(req, "vps")
	}
	t2 := *t
	t2.InputSchema, t2.OutputSchema = schema, nil
	t2.Description = t.Description + " Recebe vps: em qual VPS."
	return &t2, nil
}

// errResult e o erro como resposta da ferramenta (a IA le o motivo).
func errResult(format string, args ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, args...)}}}
}

// forward repassa a chamada pra VPS do argumento vps.
func (h *hub) forward(name string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := map[string]any{}
		if raw := req.Params.Arguments; len(raw) > 0 {
			if err := json.Unmarshal(raw, &args); err != nil {
				return errResult("argumentos invalidos: %v", err), nil
			}
		}
		vps, _ := args["vps"].(string)
		delete(args, "vps")
		if vps == "" && name == "security_check" {
			return h.securityAll(ctx), nil
		}
		r, ok := remotes.Find(h.list, vps)
		if !ok {
			if vps == "" {
				return errResult("falta vps: em qual VPS (%s)? Na duvida, pergunte ao usuario.", strings.Join(remotes.Names(h.list), ", ")), nil
			}
			return errResult("VPS %q nao existe: as VPS sao %s", vps, strings.Join(remotes.Names(h.list), ", ")), nil
		}
		res, err := h.call(ctx, r, name, args)
		if err != nil {
			return errResult("a VPS %s (%s) nao respondeu: %v", r.Name, r.Target, err), nil
		}
		return res, nil
	}
}

// call chama a ferramenta na VPS; conexao caida reconecta uma vez.
func (h *hub) call(ctx context.Context, r remotes.Remote, name string, args map[string]any) (*mcp.CallToolResult, error) {
	for try := 0; ; try++ {
		cs, err := h.session(ctx, r)
		if err != nil {
			return nil, err
		}
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err == nil || try > 0 || ctx.Err() != nil {
			return res, err
		}
		h.drop(r.Name, cs)
	}
}

// session e a conexao aberta com a VPS (abre na primeira vez).
func (h *hub) session(ctx context.Context, r remotes.Remote) (*mcp.ClientSession, error) {
	h.mu.Lock()
	cs := h.sessions[r.Name]
	h.mu.Unlock()
	if cs != nil {
		return cs, nil
	}
	tr, err := h.dial(ctx, r)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	cs, err = mcp.NewClient(&mcp.Implementation{Name: "cais-vps", Version: h.version}, nil).Connect(cctx, tr, nil)
	if err != nil {
		if st, ok := tr.(*sshTransport); ok && st.why() != "" {
			err = errors.New(st.why())
		}
		return nil, fmt.Errorf("conectando por ssh %s: %v (confira com: ssh %s cais version)", r.Target, err, r.Target)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if old := h.sessions[r.Name]; old != nil { // outra chamada conectou junto
		cs.Close()
		return old, nil
	}
	h.sessions[r.Name] = cs
	return cs, nil
}

func (h *hub) drop(name string, cs *mcp.ClientSession) {
	h.mu.Lock()
	if h.sessions[name] == cs {
		delete(h.sessions, name)
	}
	h.mu.Unlock()
	cs.Close()
}

func (h *hub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for name, cs := range h.sessions {
		cs.Close()
		delete(h.sessions, name)
	}
}

// vpsReport e a checagem de seguranca de uma VPS (ou por que nao veio).
type vpsReport struct {
	VPS    string          `json:"vps"`
	Target string          `json:"target"`
	Error  string          `json:"error,omitempty"`
	Report json.RawMessage `json:"report,omitempty"`
}

// eachSecurity roda o security_check em todas as VPS ao mesmo tempo: uma
// fora do ar nao segura as outras.
func (h *hub) eachSecurity(ctx context.Context) []vpsReport {
	out := make([]vpsReport, len(h.list))
	var wg sync.WaitGroup
	for i, r := range h.list {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = vpsReport{VPS: r.Name, Target: r.Target}
			res, err := h.call(ctx, r, "security_check", nil)
			switch {
			case err != nil:
				out[i].Error = "nao respondeu: " + err.Error()
			case res.IsError:
				out[i].Error = text(res)
			case res.StructuredContent != nil:
				out[i].Report, _ = json.Marshal(res.StructuredContent)
			default:
				out[i].Report = json.RawMessage(text(res))
			}
		}()
	}
	wg.Wait()
	return out
}

func text(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

// securityAll e o security_check sem vps: todas as VPS.
func (h *hub) securityAll(ctx context.Context) *mcp.CallToolResult {
	all := struct {
		VPS []vpsReport `json:"vps"`
	}{h.eachSecurity(ctx)}
	b, _ := json.Marshal(all)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}, StructuredContent: all}
}

// vpsList e a resposta do vps_list.
type vpsList struct {
	VPS []vpsSummary `json:"vps"`
}

type vpsSummary struct {
	Name            string `json:"name"`
	Target          string `json:"target"`
	Online          bool   `json:"online"`
	Version         string `json:"version,omitempty"`
	UpdateAvailable bool   `json:"update_available,omitempty"`
	Security        string `json:"security,omitempty"`
	Failed          int    `json:"failed"`
	Error           string `json:"error,omitempty"`
}

func (h *hub) vpsList(ctx context.Context) vpsList {
	var out vpsList
	for _, r := range h.eachSecurity(ctx) {
		s := vpsSummary{Name: r.VPS, Target: r.Target, Error: r.Error}
		var rep struct {
			Summary         string            `json:"summary"`
			Failed          []json.RawMessage `json:"failed"`
			Version         string            `json:"version"`
			UpdateAvailable bool              `json:"update_available"`
		}
		if r.Error == "" && json.Unmarshal(r.Report, &rep) == nil {
			s.Online, s.Version, s.UpdateAvailable, s.Security, s.Failed = true, rep.Version, rep.UpdateAvailable, rep.Summary, len(rep.Failed)
		}
		out.VPS = append(out.VPS, s)
	}
	slices.SortStableFunc(out.VPS, func(a, b vpsSummary) int { return b.Failed - a.Failed })
	return out
}
