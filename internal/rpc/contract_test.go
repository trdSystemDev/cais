package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/core/coretest"
)

// Teste de contrato do RPC: cada metodo da core.API chamado pelo Client tem
// que chegar no core do outro lado com os mesmos argumentos, e o resultado
// (ou o erro) tem que voltar igual. O laco e por reflexao sobre a interface:
// um metodo novo entra no teste sozinho, e falha se o servidor nao o atende
// ou se um campo se perde no caminho.

// startContract sobe o servidor com o core falso e devolve o cliente.
func startContract(t testing.TB) (*coretest.Recorder, *Client) {
	t.Helper()
	fake := coretest.NewRecorder()
	path := filepath.Join(t.TempDir(), "cais.sock")
	ln, err := Listen(path, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { Serve(ctx, ln, fake); close(done) }()
	dctx, dcancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer dcancel()
	c, err := Dial(dctx, path)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(); cancel(); <-done })
	return fake, c
}

// apiMethods sao os metodos da core.API em ordem alfabetica.
func apiMethods() []reflect.Method {
	t := reflect.TypeFor[core.API]()
	ms := make([]reflect.Method, t.NumMethod())
	for i := range ms {
		ms[i] = t.Method(i)
	}
	return ms
}

// invoke chama o metodo pelo Client com argumentos preenchidos e prepara o
// resultado do core falso. Devolve os argumentos (sem o ctx), o resultado
// preparado (invalido quando o metodo so devolve erro), o que o Client
// devolveu e o erro.
func invoke(t *testing.T, fake *coretest.Recorder, c *Client, m reflect.Method, fill *coretest.Filler) (args []any, want, got reflect.Value, err error) {
	t.Helper()
	mt := m.Type
	in := []reflect.Value{reflect.ValueOf(context.Background())}
	for i := 1; i < mt.NumIn(); i++ { // 0 e o ctx
		v := fill.Value(mt.In(i), fmt.Sprintf("%s.arg%d", m.Name, i))
		in = append(in, v)
		args = append(args, v.Interface())
	}
	if mt.NumOut() == 2 {
		want = fill.Value(mt.Out(0), m.Name+".out")
		fake.SetOut(m.Name, want.Interface())
	}
	outs := reflect.ValueOf(c).MethodByName(m.Name).Call(in)
	if e := outs[len(outs)-1]; !e.IsNil() {
		err = e.Interface().(error)
	}
	if len(outs) == 2 {
		got = outs[0]
	}
	return args, want, got, err
}

// checkCall confere que o core recebeu uma chamada so, do metodo certo e com
// os argumentos que o cliente mandou.
func checkCall(t *testing.T, fake *coretest.Recorder, name string, args []any) {
	t.Helper()
	calls := fake.Take()
	if len(calls) != 1 {
		t.Fatalf("o core recebeu %d chamadas, esperado 1: %+v", len(calls), calls)
	}
	if calls[0].Method != name {
		t.Fatalf("o core recebeu %s", calls[0].Method)
	}
	if !reflect.DeepEqual(calls[0].Args, args) {
		t.Fatalf("argumentos mudaram no caminho\nmandou:  %#v\nchegou:  %#v", args, calls[0].Args)
	}
}

func TestContratoCadaMetodoChegaIgual(t *testing.T) {
	fake, c := startContract(t)
	for _, m := range apiMethods() {
		if m.Name == "Subscribe" {
			continue // canal: TestContratoSubscribe
		}
		t.Run(m.Name, func(t *testing.T) {
			args, want, got, err := invoke(t, fake, c, m, coretest.NewFiller())
			if err != nil {
				t.Fatalf("erro: %v", err)
			}
			checkCall(t, fake, m.Name, args)
			if want.IsValid() && !reflect.DeepEqual(got.Interface(), want.Interface()) {
				t.Fatalf("resultado mudou no caminho\ncore:     %#v\ncliente:  %#v", want.Interface(), got.Interface())
			}
		})
	}
}

// Erro do core chega no cliente com a mesma mensagem. O resultado parcial
// que o core devolve junto (o deploy que falhou: commit, avisos) tambem
// chega: e o que a TUI mostra.
func TestContratoErroChegaIgual(t *testing.T) {
	fake, c := startContract(t)
	for _, m := range apiMethods() {
		if m.Name == "Subscribe" {
			continue
		}
		t.Run(m.Name, func(t *testing.T) {
			msg := "falhou de proposito em " + m.Name
			fake.SetErr(errors.New(msg))
			t.Cleanup(func() { fake.SetErr(nil) })

			_, want, got, err := invoke(t, fake, c, m, coretest.NewFiller())
			fake.Take()
			if err == nil || !strings.Contains(err.Error(), msg) {
				t.Fatalf("erro %v; esperado com %q", err, msg)
			}
			if want.IsValid() && want.Type() == reflect.TypeFor[apps.DeployResult]() &&
				!reflect.DeepEqual(got.Interface(), want.Interface()) {
				t.Fatalf("resultado parcial do deploy se perdeu\ncore:     %#v\ncliente:  %#v", want.Interface(), got.Interface())
			}
		})
	}
}

func TestContratoSubscribe(t *testing.T) {
	fake, c := startContract(t)
	fake.State = coretest.NewFiller().Value(reflect.TypeFor[core.State](), "State").Interface().(core.State)

	ch, stop, err := c.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	select {
	case st := <-ch:
		if !reflect.DeepEqual(st, fake.State) {
			t.Fatalf("estado mudou no caminho\ncore:     %#v\ncliente:  %#v", fake.State, st)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("o estado nao chegou")
	}
	if calls := fake.Take(); len(calls) != 1 || calls[0].Method != "Subscribe" {
		t.Fatalf("chamadas no core: %+v", calls)
	}
}

// O core falso tem que gravar o nome certo de cada metodo: um nome errado
// (copia e cola) faria o teste de contrato conferir a chamada errada.
func TestContratoFakeGravaONomeCerto(t *testing.T) {
	fake := coretest.NewRecorder()
	v := reflect.ValueOf(fake)
	for _, m := range apiMethods() {
		in := []reflect.Value{reflect.ValueOf(context.Background())}
		for i := 1; i < m.Type.NumIn(); i++ {
			in = append(in, reflect.New(m.Type.In(i)).Elem())
		}
		v.MethodByName(m.Name).Call(in)
		calls := fake.Take()
		if len(calls) != 1 || calls[0].Method != m.Name {
			t.Errorf("%s gravou %+v", m.Name, calls)
		}
	}
	if n := len(apiMethods()); n != 54 {
		t.Logf("core.API tem %d metodos", n) // so informa: metodo novo entra no laco sozinho
	}
}

// protocolMethods le de protocol.go cada constante Method* e se ela recebe
// parametros (o comentario "params:" ao lado).
func protocolMethods(t *testing.T) map[string]bool {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "protocol.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok || len(vs.Names) != 1 || !strings.HasPrefix(vs.Names[0].Name, "Method") || len(vs.Values) != 1 {
			return true
		}
		lit, ok := vs.Values[0].(*ast.BasicLit)
		if !ok {
			return true
		}
		name, _ := strconv.Unquote(lit.Value)
		out[name] = vs.Comment != nil && strings.Contains(vs.Comment.Text(), "params:")
		return true
	})
	if len(out) < 50 {
		t.Fatalf("achei so %d metodos em protocol.go", len(out))
	}
	return out
}

// Todo metodo do protocolo tem resposta do servidor, e quem recebe
// parametros recusa campo desconhecido: cliente e daemon de versoes
// diferentes falham alto, nao em silencio.
func TestContratoParametroDesconhecidoFalhaAlto(t *testing.T) {
	fake, _ := startContract(t)
	path := filepath.Join(t.TempDir(), "raw.sock")
	ln, err := Listen(path, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { Serve(ctx, ln, fake); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	r := bufio.NewReader(conn)

	id := uint64(0)
	for method, takesParams := range protocolMethods(t) {
		if method == MethodStateSubscribe {
			continue // sem parametros; inscreve a conexao (TestContratoSubscribe)
		}
		id++
		req, _ := json.Marshal(message{JSONRPC: "2.0", ID: id, Method: method, Params: json.RawMessage(`{"campo_que_nao_existe":1}`)})
		if _, err := conn.Write(append(req, '\n')); err != nil {
			t.Fatal(err)
		}
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		line, err := r.ReadBytes('\n')
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		var resp message
		if err := json.Unmarshal(line, &resp); err != nil {
			t.Fatalf("%s: resposta %q", method, line)
		}
		fake.Take()
		switch {
		case resp.Error != nil && resp.Error.Code == CodeMethodNotFound:
			t.Errorf("%s: o servidor nao atende (metodo desconhecido)", method)
		case takesParams && (resp.Error == nil || resp.Error.Code != CodeInvalidParams):
			t.Errorf("%s: aceitou campo desconhecido: %s", method, line)
		}
	}
}

var typeOfState = reflect.TypeFor[core.State]()
