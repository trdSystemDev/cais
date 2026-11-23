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
	"sync"
	"testing"
	"time"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/backup"
	"github.com/trdSystemDev/cais/internal/bootstrap"
	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/github"
)

// Teste de contrato do RPC: cada metodo da core.API chamado pelo Client tem
// que chegar no core do outro lado com os mesmos argumentos, e o resultado
// (ou o erro) tem que voltar igual. O laco e por reflexao sobre a interface:
// um metodo novo entra no teste sozinho, e falha se o servidor nao o atende
// ou se um campo se perde no caminho.

// call e uma chamada que o core falso recebeu.
type call struct {
	method string
	args   []any
}

// recAPI e um core que so grava o que recebe e devolve o que o teste
// preparou em out (por metodo) e err.
type recAPI struct {
	mu    sync.Mutex
	calls []call
	out   map[string]any
	err   error
	state core.State
}

var _ core.API = (*recAPI)(nil)

func (f *recAPI) record(m string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call{m, args})
}

func (f *recAPI) take() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.calls
	f.calls = nil
	return c
}

// ret grava a chamada e devolve o resultado preparado pro metodo.
func ret[R any](f *recAPI, m string, args ...any) (R, error) {
	f.record(m, args...)
	f.mu.Lock()
	defer f.mu.Unlock()
	r, _ := f.out[m].(R)
	return r, f.err
}

// only grava a chamada de um metodo que so devolve erro.
func only(f *recAPI, m string, args ...any) error {
	f.record(m, args...)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

func (f *recAPI) Snapshot(context.Context) (core.State, error) {
	return ret[core.State](f, "Snapshot")
}
func (f *recAPI) Subscribe(context.Context) (<-chan core.State, func(), error) {
	f.record("Subscribe")
	ch := make(chan core.State, 1)
	ch <- f.state
	var once sync.Once
	return ch, func() { once.Do(func() { close(ch) }) }, f.err
}
func (f *recAPI) CheckEnvironment(context.Context) (core.Environment, error) {
	return ret[core.Environment](f, "CheckEnvironment")
}
func (f *recAPI) SystemStats(context.Context) (core.SystemStats, error) {
	return ret[core.SystemStats](f, "SystemStats")
}
func (f *recAPI) ContainerStats(context.Context) (core.ContainerUsage, error) {
	return ret[core.ContainerUsage](f, "ContainerStats")
}
func (f *recAPI) CheckHealth(context.Context) (core.Health, error) {
	return ret[core.Health](f, "CheckHealth")
}
func (f *recAPI) CheckStacks(context.Context) (core.Infra, error) {
	return ret[core.Infra](f, "CheckStacks")
}
func (f *recAPI) ContainerLogs(_ context.Context, stack, container string) (string, error) {
	return ret[string](f, "ContainerLogs", stack, container)
}
func (f *recAPI) LoadConfig(context.Context) (core.ConfigInfo, error) {
	return ret[core.ConfigInfo](f, "LoadConfig")
}
func (f *recAPI) SaveConfig(_ context.Context, c config.Config) error {
	return only(f, "SaveConfig", c)
}
func (f *recAPI) SetSecret(_ context.Context, name, value string) error {
	return only(f, "SetSecret", name, value)
}
func (f *recAPI) TestNotify(context.Context) error { return only(f, "TestNotify") }
func (f *recAPI) TestGitHub(_ context.Context, appID int64, key string) (github.Check, error) {
	return ret[github.Check](f, "TestGitHub", appID, key)
}
func (f *recAPI) PlanSetup(context.Context) (bootstrap.Plan, error) {
	return ret[bootstrap.Plan](f, "PlanSetup")
}
func (f *recAPI) SetService(_ context.Context, name string, on bool) (core.Setup, error) {
	return ret[core.Setup](f, "SetService", name, on)
}
func (f *recAPI) ControlService(_ context.Context, name, action string) error {
	return only(f, "ControlService", name, action)
}
func (f *recAPI) CheckApps(context.Context) (core.Apps, error) {
	return ret[core.Apps](f, "CheckApps")
}
func (f *recAPI) CreateApp(_ context.Context, a apps.App) (apps.App, error) {
	return ret[apps.App](f, "CreateApp", a)
}
func (f *recAPI) UpdateApp(_ context.Context, a apps.App) error {
	return only(f, "UpdateApp", a)
}
func (f *recAPI) DeployApp(_ context.Context, name, ref string) (apps.DeployResult, error) {
	return ret[apps.DeployResult](f, "DeployApp", name, ref)
}
func (f *recAPI) CheckPromotion(_ context.Context, from string) (apps.Promotion, error) {
	return ret[apps.Promotion](f, "CheckPromotion", from)
}
func (f *recAPI) PromoteApp(_ context.Context, from string, confirm bool) (apps.DeployResult, error) {
	return ret[apps.DeployResult](f, "PromoteApp", from, confirm)
}
func (f *recAPI) RollbackApp(_ context.Context, name, commit string) (apps.DeployResult, error) {
	return ret[apps.DeployResult](f, "RollbackApp", name, commit)
}
func (f *recAPI) RestartApp(_ context.Context, name, service string) error {
	return only(f, "RestartApp", name, service)
}
func (f *recAPI) StopApp(_ context.Context, name string) error {
	return only(f, "StopApp", name)
}
func (f *recAPI) DeleteApp(_ context.Context, name string, volumes bool) error {
	return only(f, "DeleteApp", name, volumes)
}
func (f *recAPI) AppLogs(_ context.Context, name, container string) (string, error) {
	return ret[string](f, "AppLogs", name, container)
}
func (f *recAPI) AppEnv(_ context.Context, name string) (map[string]string, error) {
	return ret[map[string]string](f, "AppEnv", name)
}
func (f *recAPI) SetAppEnv(_ context.Context, name string, env map[string]string) error {
	return only(f, "SetAppEnv", name, env)
}
func (f *recAPI) AppSecrets(_ context.Context, name string) ([]apps.Secret, error) {
	return ret[[]apps.Secret](f, "AppSecrets", name)
}
func (f *recAPI) SetAppSecret(_ context.Context, name, secret, value string) error {
	return only(f, "SetAppSecret", name, secret, value)
}
func (f *recAPI) DeleteAppSecret(_ context.Context, name, secret string) error {
	return only(f, "DeleteAppSecret", name, secret)
}
func (f *recAPI) AppGuide(_ context.Context, name string) (string, error) {
	return ret[string](f, "AppGuide", name)
}
func (f *recAPI) CreateDatabase(_ context.Context, req core.DatabaseRequest) (core.DatabaseInfo, error) {
	return ret[core.DatabaseInfo](f, "CreateDatabase", req)
}
func (f *recAPI) Databases(context.Context) ([]core.DatabaseInfo, error) {
	return ret[[]core.DatabaseInfo](f, "Databases")
}
func (f *recAPI) LinkAppDatabase(_ context.Context, app, engine, name string) error {
	return only(f, "LinkAppDatabase", app, engine, name)
}
func (f *recAPI) UnlinkAppDatabase(_ context.Context, app, engine, name string) error {
	return only(f, "UnlinkAppDatabase", app, engine, name)
}
func (f *recAPI) DeleteDatabase(_ context.Context, engine, name string) error {
	return only(f, "DeleteDatabase", engine, name)
}
func (f *recAPI) CreateBucket(_ context.Context, name string) (core.BucketInfo, error) {
	return ret[core.BucketInfo](f, "CreateBucket", name)
}
func (f *recAPI) DeleteBucket(_ context.Context, name string) error {
	return only(f, "DeleteBucket", name)
}
func (f *recAPI) Buckets(context.Context) ([]core.BucketInfo, error) {
	return ret[[]core.BucketInfo](f, "Buckets")
}
func (f *recAPI) LinkAppBucket(_ context.Context, app, bucket string) error {
	return only(f, "LinkAppBucket", app, bucket)
}
func (f *recAPI) UnlinkAppBucket(_ context.Context, app, bucket string) error {
	return only(f, "UnlinkAppBucket", app, bucket)
}
func (f *recAPI) RefreshBackup(context.Context) (core.BackupStatus, error) {
	return ret[core.BackupStatus](f, "RefreshBackup")
}
func (f *recAPI) SetBackupProvider(_ context.Context, provider string) error {
	return only(f, "SetBackupProvider", provider)
}
func (f *recAPI) SetBackupClient(_ context.Context, id, secret string) error {
	return only(f, "SetBackupClient", id, secret)
}
func (f *recAPI) StartDriveLogin(context.Context) (core.DriveLogin, error) {
	return ret[core.DriveLogin](f, "StartDriveLogin")
}
func (f *recAPI) CancelDriveLogin(context.Context) error { return only(f, "CancelDriveLogin") }
func (f *recAPI) DisconnectDrive(context.Context) error  { return only(f, "DisconnectDrive") }
func (f *recAPI) NewBackupKey(context.Context) (string, error) {
	return ret[string](f, "NewBackupKey")
}
func (f *recAPI) SetBackupSchedule(_ context.Context, at string, days int) error {
	return only(f, "SetBackupSchedule", at, days)
}
func (f *recAPI) RunBackup(context.Context) (core.BackupRun, error) {
	return ret[core.BackupRun](f, "RunBackup")
}
func (f *recAPI) BackupFiles(context.Context) ([]backup.File, error) {
	return ret[[]backup.File](f, "BackupFiles")
}
func (f *recAPI) RestoreBackup(_ context.Context, req core.RestoreRequest) (core.RestoreResult, error) {
	return ret[core.RestoreResult](f, "RestoreBackup", req)
}

// filler preenche qualquer valor com dados nao zero e diferentes entre si:
// cada string leva o caminho do campo e cada numero um contador, entao dois
// campos trocados no caminho (Name no lugar de Service) nunca batem.
type filler struct {
	n    int
	base time.Time
}

func newFiller() *filler {
	return &filler{base: time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)}
}

func (f *filler) value(t reflect.Type, path string) reflect.Value {
	v := reflect.New(t).Elem()
	f.fill(v, path, 0)
	return v
}

func (f *filler) fill(v reflect.Value, path string, depth int) {
	if depth > 6 { // tipo recursivo: para aqui
		return
	}
	f.n++
	if v.Type() == reflect.TypeOf(time.Time{}) {
		v.Set(reflect.ValueOf(f.base.Add(time.Duration(f.n) * time.Second)))
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(fmt.Sprintf("%s-%d", path, f.n))
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(int64(f.n))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(uint64(f.n))
	case reflect.Float32, reflect.Float64:
		v.SetFloat(float64(f.n) + 0.5)
	case reflect.Pointer:
		p := reflect.New(v.Type().Elem())
		f.fill(p.Elem(), path, depth+1)
		v.Set(p)
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 1, 1)
		f.fill(s.Index(0), path+"[0]", depth+1)
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k := reflect.New(v.Type().Key()).Elem()
		f.fill(k, path+".k", depth+1)
		e := reflect.New(v.Type().Elem()).Elem()
		f.fill(e, path+".v", depth+1)
		m.SetMapIndex(k, e)
		v.Set(m)
	case reflect.Struct:
		t := v.Type()
		for i := range t.NumField() {
			sf := t.Field(i)
			if !sf.IsExported() || strings.Split(sf.Tag.Get("json"), ",")[0] == "-" {
				continue // nao viaja no JSON
			}
			f.fill(v.Field(i), path+"."+sf.Name, depth+1)
		}
	}
	// interface, func e chan ficam zero: nao viajam no JSON
}

// startContract sobe o servidor com o core falso e devolve o cliente.
func startContract(t *testing.T) (*recAPI, *Client) {
	t.Helper()
	fake := &recAPI{out: map[string]any{}}
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
func invoke(t *testing.T, fake *recAPI, c *Client, m reflect.Method, fill *filler) (args []any, want, got reflect.Value, err error) {
	t.Helper()
	mt := m.Type
	in := []reflect.Value{reflect.ValueOf(context.Background())}
	for i := 1; i < mt.NumIn(); i++ { // 0 e o ctx
		v := fill.value(mt.In(i), fmt.Sprintf("%s.arg%d", m.Name, i))
		in = append(in, v)
		args = append(args, v.Interface())
	}
	if mt.NumOut() == 2 {
		want = fill.value(mt.Out(0), m.Name+".out")
		fake.mu.Lock()
		fake.out[m.Name] = want.Interface()
		fake.mu.Unlock()
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
func checkCall(t *testing.T, fake *recAPI, name string, args []any) {
	t.Helper()
	calls := fake.take()
	if len(calls) != 1 {
		t.Fatalf("o core recebeu %d chamadas, esperado 1: %+v", len(calls), calls)
	}
	if calls[0].method != name {
		t.Fatalf("o core recebeu %s", calls[0].method)
	}
	if !reflect.DeepEqual(calls[0].args, args) {
		t.Fatalf("argumentos mudaram no caminho\nmandou:  %#v\nchegou:  %#v", args, calls[0].args)
	}
}

func TestContratoCadaMetodoChegaIgual(t *testing.T) {
	fake, c := startContract(t)
	for _, m := range apiMethods() {
		if m.Name == "Subscribe" {
			continue // canal: TestContratoSubscribe
		}
		t.Run(m.Name, func(t *testing.T) {
			args, want, got, err := invoke(t, fake, c, m, newFiller())
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
			fake.mu.Lock()
			fake.err = errors.New(msg)
			fake.mu.Unlock()
			t.Cleanup(func() { fake.mu.Lock(); fake.err = nil; fake.mu.Unlock() })

			_, want, got, err := invoke(t, fake, c, m, newFiller())
			fake.take()
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
	fake.state = newFiller().value(reflect.TypeFor[core.State](), "State").Interface().(core.State)

	ch, stop, err := c.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	select {
	case st := <-ch:
		if !reflect.DeepEqual(st, fake.state) {
			t.Fatalf("estado mudou no caminho\ncore:     %#v\ncliente:  %#v", fake.state, st)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("o estado nao chegou")
	}
	if calls := fake.take(); len(calls) != 1 || calls[0].method != "Subscribe" {
		t.Fatalf("chamadas no core: %+v", calls)
	}
}

// O core falso tem que gravar o nome certo de cada metodo: um nome errado
// (copia e cola) faria o teste de contrato conferir a chamada errada.
func TestContratoFakeGravaONomeCerto(t *testing.T) {
	fake := &recAPI{out: map[string]any{}}
	v := reflect.ValueOf(fake)
	for _, m := range apiMethods() {
		in := []reflect.Value{reflect.ValueOf(context.Background())}
		for i := 1; i < m.Type.NumIn(); i++ {
			in = append(in, reflect.New(m.Type.In(i)).Elem())
		}
		v.MethodByName(m.Name).Call(in)
		calls := fake.take()
		if len(calls) != 1 || calls[0].method != m.Name {
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
		fake.take()
		switch {
		case resp.Error != nil && resp.Error.Code == CodeMethodNotFound:
			t.Errorf("%s: o servidor nao atende (metodo desconhecido)", method)
		case takesParams && (resp.Error == nil || resp.Error.Code != CodeInvalidParams):
			t.Errorf("%s: aceitou campo desconhecido: %s", method, line)
		}
	}
}
