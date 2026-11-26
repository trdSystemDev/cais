package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// As mensagens do cais (erros, o guia do compose, o historico de deploy)
// sao escritas pra quem usa a TUI ou o CLI: "crie na aba Infra", "cais app
// guide portal". Pela IA, o caminho e outra ferramenta deste MCP, ou o
// usuario quando a acao nao existe aqui (ligar servico). forAI troca essas
// referencias em toda resposta de ferramenta, inclusive nos textos que
// vieram do daemon e no historico guardado.
var aiRewrites = []struct {
	re   *regexp.Regexp
	with string
}{
	// o guia: como criar e ligar banco e bucket
	{regexp.MustCompile(`aba Infra -> Novo banco e depois Apps -> Bancos e buckets ->\n# Ligar banco \(ou cais app db [a-z0-9-]+ postgres/<nome>\)`),
		"database_create e depois app_link_database"},
	{regexp.MustCompile(`aba Infra -> Garage -> Novo bucket e depois Apps -> Bancos e\n# buckets -> Ligar bucket \(ou cais app bucket [a-z0-9-]+ <nome>\)`),
		"bucket_create e depois app_link_bucket"},
	{regexp.MustCompile(`\(ligados na aba Infra\)`), "(ligados com app_link_database e app_link_bucket)"},
	// o modelo de compose citado nos erros do deploy
	{regexp.MustCompile(`cais app guide ([a-z0-9-]+)`), "app_guide (app $1)"},
	// banco ou bucket que nao existe: a IA cria
	{regexp.MustCompile(`crie na aba Infra -> Novo banco`), "crie com database_create"},
	{regexp.MustCompile(`crie na aba Infra -> Garage -> Novo bucket`), "crie com bucket_create"},
	// ligar servico nao existe aqui: e com o usuario
	{regexp.MustCompile(`a stack ([a-z]+) (esta desligada|nao esta instalada): ligue na aba Infra`),
		"a stack $1 $2. Ligar servico nao e pelo MCP: peca ao usuario pra ligar na TUI do cais (aba Infra) ou com cais service $1 on"},
	{regexp.MustCompile(`Ligue o ([a-z]+) na aba Infra \(ou cais service [a-z]+ on\)`),
		"Ligar o $1 nao e pelo MCP: peca ao usuario (TUI do cais, aba Infra, ou cais service $1 on)"},
	{regexp.MustCompile(`aplique o postgres na aba Infra \(ou cais service postgres on\)`),
		"peca ao usuario pra aplicar o postgres na TUI do cais (aba Infra) ou com cais service postgres on"},
}

func rewriteForAI(s string) string {
	for _, r := range aiRewrites {
		s = r.re.ReplaceAllString(s, r.with)
	}
	return s
}

// forAI e o middleware que reescreve as respostas de ferramenta: o texto e o
// JSON estruturado (as strings dentro dele, como o erro de um deploy no
// history do apps_list).
func forAI(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		res, err := next(ctx, method, req)
		r, ok := res.(*mcp.CallToolResult)
		if err != nil || !ok || r == nil {
			return res, err
		}
		var oldJSON, newJSON string
		if raw, ok := r.StructuredContent.(json.RawMessage); ok {
			if out, ok := rewriteJSON(raw); ok {
				oldJSON, newJSON = string(raw), string(out)
				r.StructuredContent = out
			}
		}
		for _, c := range r.Content {
			tc, ok := c.(*mcp.TextContent)
			if !ok {
				continue
			}
			if oldJSON != "" && tc.Text == oldJSON {
				tc.Text = newJSON
			} else {
				tc.Text = rewriteForAI(tc.Text)
			}
		}
		return r, nil
	}
}

// jsonString e uma string literal do JSON (aspas, escapes e tudo).
var jsonString = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)

// rewriteJSON reescreve cada string do JSON no lugar, sem mexer na ordem dos
// campos nem no resto. A string trocada sai sem o escape de HTML do
// encoding/json (-> fica ->, nao \u003e).
func rewriteJSON(raw json.RawMessage) (json.RawMessage, bool) {
	changed := false
	out := jsonString.ReplaceAllFunc(raw, func(lit []byte) []byte {
		var s string
		if json.Unmarshal(lit, &s) != nil {
			return lit
		}
		r := rewriteForAI(s)
		if r == s {
			return lit
		}
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		if enc.Encode(r) != nil {
			return lit
		}
		changed = true
		return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
	})
	return out, changed
}
