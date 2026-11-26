package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"reflect"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/github"
	"github.com/trdSystemDev/cais/internal/inflight"
)

// ErrShuttingDown e a resposta a uma chamada que chega com o daemon
// desligando (esperando as acoes em andamento terminarem).
const ErrShuttingDown = "o daemon esta reiniciando (esperando as acoes em andamento terminarem); tente de novo em instantes"

// Serve atende conexoes em ln ate ctx ser cancelado. Cada conexao pode
// fazer varias chamadas em paralelo e receber notificacoes de estado.
//
// As acoes nao rodam com o ctx da conexao: se a TUI cair no meio de um
// deploy, o deploy termina mesmo assim em vez de ficar pela metade.
//
// Cancelar ctx (SIGTERM do systemd) desliga sem cortar nada: o socket para
// de aceitar conexao, chamada nova recebe erro e o Serve espera as acoes em
// andamento terminarem (e responderem) antes de fechar as conexoes. As
// acoes nem veem o cancelamento: um deploy cortado no meio deixaria o app
// metade numa versao, metade na outra.
func Serve(ctx context.Context, ln net.Listener, api core.API) error {
	s := &server{api: api, work: context.WithoutCancel(ctx), conns: map[net.Conn]struct{}{}}
	drained := make(chan struct{})

	go func() {
		<-ctx.Done()
		ln.Close()
		if n := s.calls.Count(); n > 0 {
			slog.Info("desligando: esperando as chamadas em andamento terminarem", "acao", "desligamento", "em_andamento", n)
		}
		s.calls.Close()
		s.closeAll()
		close(drained)
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				<-drained
				s.wg.Wait()
				return nil
			}
			return err
		}
		if !s.track(conn) {
			conn.Close()
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.untrack(conn)
			s.handle(conn)
		}()
	}
}

type server struct {
	api core.API
	// work e o ctx das acoes: nunca cancelado (o desligamento espera)
	work context.Context
	wg   sync.WaitGroup
	// calls sao as chamadas em andamento; fecha no desligamento
	calls inflight.Group

	mu       sync.Mutex
	conns    map[net.Conn]struct{}
	shutdown bool
}

func (s *server) track(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shutdown {
		return false
	}
	s.conns[c] = struct{}{}
	return true
}

func (s *server) untrack(c net.Conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
	c.Close()
}

func (s *server) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shutdown = true
	for c := range s.conns {
		c.Close()
	}
}

// conn e o estado de uma conexao: escrita serializada e inscricao unica.
type conn struct {
	net.Conn
	ctx        context.Context // cancelado quando a conexao fecha
	wmu        sync.Mutex
	enc        *json.Encoder
	subscribed atomic.Bool
}

func (c *conn) send(m message) error {
	m.JSONRPC = "2.0"
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.enc.Encode(m)
}

func (s *server) handle(nc net.Conn) {
	// a conexao vive ate ser fechada: no desligamento ela fica aberta
	// (recebendo o estado) ate as acoes em andamento responderem
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &conn{Conn: nc, ctx: ctx, enc: json.NewEncoder(nc)}

	dec := json.NewDecoder(nc)
	for {
		var m message
		if err := dec.Decode(&m); err != nil {
			var syntax *json.SyntaxError
			if errors.As(err, &syntax) {
				// depois de JSON quebrado nao da pra saber onde comeca a
				// proxima mensagem: avisa e encerra a conexao
				c.send(message{Error: &Error{Code: CodeParseError, Message: err.Error()}})
			}
			return
		}
		if m.ID == 0 {
			continue // cliente nao manda notificacoes; ignora
		}
		if m.Method == MethodAppShell {
			s.shell(c, m)
			continue
		}
		if !s.calls.Begin() {
			c.send(message{ID: m.ID, Error: &Error{Code: CodeActionFailed, Message: ErrShuttingDown}})
			continue
		}
		go func() {
			defer s.calls.End() // depois da resposta: o desligamento espera ela sair
			result, rerr := s.dispatch(c, m)
			reply := message{ID: m.ID}
			if rerr != nil {
				reply.Error = rerr
			} else if reply.Result, rerr = marshal(result); rerr != nil {
				reply.Error = rerr
			}
			c.send(reply)
		}()
	}
}

func (s *server) dispatch(c *conn, m message) (any, *Error) {
	if m.Method == MethodStateSubscribe {
		return s.subscribe(c)
	}
	h, found := handlers[m.Method]
	if !found {
		return nil, &Error{Code: CodeMethodNotFound, Message: "metodo desconhecido: " + m.Method}
	}
	return h(s, c, m)
}

// handler atende um metodo do protocolo.
type handler func(s *server, c *conn, m message) (any, *Error)

// Os handlers rodam a acao com o ctx do daemon (s.work): ela nao morre com
// o cliente. So o que existe pra quem esta olhando (logs) usa o da conexao.

// get e um metodo sem parametros que devolve um valor.
func get[R any](fn func(core.API, context.Context) (R, error)) handler {
	return func(s *server, _ *conn, _ message) (any, *Error) {
		return wrap(fn(s.api, s.work))
	}
}

// run e um metodo sem parametros que so devolve erro.
func run(fn func(core.API, context.Context) error) handler {
	return func(s *server, _ *conn, _ message) (any, *Error) {
		return ok(fn(s.api, s.work))
	}
}

// call e um metodo com parametros P que devolve um valor.
func call[P, R any](fn func(core.API, context.Context, P) (R, error)) handler {
	return func(s *server, _ *conn, m message) (any, *Error) {
		var p P
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return wrap(fn(s.api, s.work, p))
	}
}

// do e um metodo com parametros P que so devolve erro.
func do[P any](fn func(core.API, context.Context, P) error) handler {
	return func(s *server, _ *conn, m message) (any, *Error) {
		var p P
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return ok(fn(s.api, s.work, p))
	}
}

// handlers sao os metodos do protocolo (menos o state.subscribe, que mexe
// na conexao). Metodo novo: a constante em protocol.go, uma linha aqui e o
// metodo no client.go; o teste de contrato confere os tres.
var handlers = map[string]handler{
	// estado e ambiente
	MethodStateGet:         get(core.API.Snapshot),
	MethodEnvironmentCheck: get(core.API.CheckEnvironment),
	MethodSystemStats:      get(core.API.SystemStats),
	MethodContainerStats:   get(core.API.ContainerStats),
	MethodTrafficStats:     get(core.API.TrafficStats),
	MethodHealthCheck:      get(core.API.CheckHealth),
	MethodHealthUpdate:     get(core.API.CheckUpdate),
	MethodInfraCheck:       get(core.API.CheckStacks),
	MethodContainerLogs: func(s *server, c *conn, m message) (any, *Error) {
		var p logsParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return wrap(s.api.ContainerLogs(c.ctx, p.Stack, p.Container, p.Logs))
	},

	// setup
	MethodConfigGet:  get(core.API.LoadConfig),
	MethodConfigSave: do(core.API.SaveConfig),
	MethodSetupPlan:  get(core.API.PlanSetup),
	MethodNotifyTest: run(core.API.TestNotify),
	MethodHostHarden: do(core.API.HardenHost),
	MethodHostFix: call(func(a core.API, ctx context.Context, p hostFixParams) (textResult, error) {
		text, err := a.FixHostItem(ctx, p.ID)
		return textResult{Text: text}, err
	}),
	MethodGitHubLink: call(func(a core.API, ctx context.Context, p githubLinkParams) (textResult, error) {
		link, err := a.GitHubConnectLink(ctx, p.Org)
		return textResult{Text: link}, err
	}),
	MethodGitHubConnect: call(func(a core.API, ctx context.Context, p githubConnectParams) (core.GitHubConnected, error) {
		return a.ConnectGitHub(ctx, p.Code)
	}),
	MethodUpdate: call(func(a core.API, ctx context.Context, p updateParams) (core.UpdateResult, error) {
		return a.UpdateCais(ctx, p.Version)
	}),
	MethodTailscaleConnect: get(core.API.ConnectTailscale),
	MethodSSHClose:         run(core.API.CloseSSHPublic),
	MethodHostConfirm: do(func(a core.API, ctx context.Context, p hostConfirmParams) error {
		return a.ConfirmHost(ctx, p.Since)
	}),
	MethodSecretSet: do(func(a core.API, ctx context.Context, p secretParams) error {
		return a.SetSecret(ctx, p.Name, p.Value)
	}),
	MethodGitHubTest: call(func(a core.API, ctx context.Context, p githubTestParams) (github.Check, error) {
		return a.TestGitHub(ctx, p.AppID, p.Key)
	}),
	MethodServiceSet: call(func(a core.API, ctx context.Context, p serviceParams) (core.Setup, error) {
		return a.SetService(ctx, p.Name, p.On)
	}),
	MethodServiceControl: do(func(a core.API, ctx context.Context, p serviceControlParams) error {
		return a.ControlService(ctx, p.Name, p.Action)
	}),

	// apps
	MethodAppsCheck: get(core.API.CheckApps),
	MethodAppCreate: call(core.API.CreateApp),
	MethodAppUpdate: do(core.API.UpdateApp),
	MethodAppDeploy: call(func(a core.API, ctx context.Context, p deployParams) (apps.DeployResult, error) {
		return a.DeployApp(ctx, p.Name, p.Ref)
	}),
	MethodAppRollback: call(func(a core.API, ctx context.Context, p deployParams) (apps.DeployResult, error) {
		return a.RollbackApp(ctx, p.Name, p.Ref)
	}),
	MethodAppPromoteCheck: call(func(a core.API, ctx context.Context, p appParams) (apps.Promotion, error) {
		return a.CheckPromotion(ctx, p.Name)
	}),
	MethodAppPromote: call(func(a core.API, ctx context.Context, p promoteParams) (apps.DeployResult, error) {
		return a.PromoteApp(ctx, p.Name, p.Confirm)
	}),
	MethodAppRestart: do(func(a core.API, ctx context.Context, p appParams) error {
		return a.RestartApp(ctx, p.Name, p.Service)
	}),
	MethodAppStop: do(func(a core.API, ctx context.Context, p appParams) error {
		return a.StopApp(ctx, p.Name)
	}),
	MethodAppDelete: do(func(a core.API, ctx context.Context, p deleteParams) error {
		return a.DeleteApp(ctx, p.Name, p.Volumes)
	}),
	MethodAppLogs: call(func(a core.API, ctx context.Context, p appParams) (string, error) {
		return a.AppLogs(ctx, p.Name, p.Container, p.Logs)
	}),
	MethodAppEnvGet: call(func(a core.API, ctx context.Context, p appParams) (map[string]string, error) {
		return a.AppEnv(ctx, p.Name)
	}),
	MethodAppEnvSet: do(func(a core.API, ctx context.Context, p envParams) error {
		return a.SetAppEnv(ctx, p.Name, p.Env)
	}),
	MethodAppGuide: call(func(a core.API, ctx context.Context, p appParams) (textResult, error) {
		text, err := a.AppGuide(ctx, p.Name)
		return textResult{Text: text}, err
	}),

	// bancos e buckets
	MethodDatabaseCreate: call(core.API.CreateDatabase),
	MethodDatabases:      get(core.API.Databases),
	MethodDatabaseDelete: do(func(a core.API, ctx context.Context, p appDBParams) error {
		return a.DeleteDatabase(ctx, p.Engine, p.Name)
	}),
	MethodAppDBLink: do(func(a core.API, ctx context.Context, p appDBParams) error {
		return a.LinkAppDatabase(ctx, p.App, p.Engine, p.Name)
	}),
	MethodAppDBUnlink: do(func(a core.API, ctx context.Context, p appDBParams) error {
		return a.UnlinkAppDatabase(ctx, p.App, p.Engine, p.Name)
	}),
	MethodBuckets: get(core.API.Buckets),
	MethodBucketCreate: call(func(a core.API, ctx context.Context, p bucketParams) (core.BucketInfo, error) {
		return a.CreateBucket(ctx, p.Name)
	}),
	MethodBucketDelete: do(func(a core.API, ctx context.Context, p bucketParams) error {
		return a.DeleteBucket(ctx, p.Name)
	}),
	MethodAppBucketLink: do(func(a core.API, ctx context.Context, p appBucketParams) error {
		return a.LinkAppBucket(ctx, p.App, p.Bucket)
	}),
	MethodAppBucketUnlink: do(func(a core.API, ctx context.Context, p appBucketParams) error {
		return a.UnlinkAppBucket(ctx, p.App, p.Bucket)
	}),

	// backup
	MethodBackupRefresh:    get(core.API.RefreshBackup),
	MethodBackupLogin:      get(core.API.StartDriveLogin),
	MethodBackupLoginStop:  run(core.API.CancelDriveLogin),
	MethodBackupDisconnect: run(core.API.DisconnectDrive),
	MethodBackupRun:        get(core.API.RunBackup),
	MethodBackupFiles:      get(core.API.BackupFiles),
	MethodBackupRestore:    call(core.API.RestoreBackup),
	MethodBackupKey: get(func(a core.API, ctx context.Context) (textResult, error) {
		key, err := a.NewBackupKey(ctx)
		return textResult{Text: key}, err
	}),
	MethodBackupProvider: do(func(a core.API, ctx context.Context, p textResult) error {
		return a.SetBackupProvider(ctx, p.Text)
	}),
	MethodBackupClient: do(func(a core.API, ctx context.Context, p backupClientParams) error {
		return a.SetBackupClient(ctx, p.ID, p.Secret)
	}),
	MethodBackupSchedule: do(func(a core.API, ctx context.Context, p backupScheduleParams) error {
		return a.SetBackupSchedule(ctx, p.Time, p.Days)
	}),
}

// subscribe inscreve a conexao no estado: cada mudanca vira notificacao.
func (s *server) subscribe(c *conn) (any, *Error) {
	if !c.subscribed.CompareAndSwap(false, true) {
		return map[string]bool{"subscribed": true}, nil // ja inscrito
	}
	states, unsubscribe, err := s.api.Subscribe(c.ctx)
	if err != nil {
		c.subscribed.Store(false)
		return nil, actionError(err)
	}
	go forwardStates(c, states, unsubscribe)
	return map[string]bool{"subscribed": true}, nil
}

// forwardStates repassa cada foto do core como notificacao ate a conexao
// fechar.
func forwardStates(c *conn, states <-chan core.State, unsubscribe func()) {
	defer unsubscribe()
	for {
		select {
		case <-c.ctx.Done():
			return
		case st, ok := <-states:
			if !ok {
				return
			}
			params, err := json.Marshal(st)
			if err != nil {
				continue
			}
			if c.send(message{Method: NotifyState, Params: params}) != nil {
				return
			}
		}
	}
}

func wrap[T any](v T, err error) (any, *Error) {
	if err != nil {
		e := actionError(err)
		// o que a acao conseguiu dizer antes de falhar vai junto
		if !reflect.ValueOf(&v).Elem().IsZero() {
			e.Data, _ = json.Marshal(v)
		}
		return nil, e
	}
	return v, nil
}

// ok e o wrap das acoes que so devolvem erro.
func ok(err error) (any, *Error) {
	if err != nil {
		return nil, actionError(err)
	}
	return map[string]bool{"ok": true}, nil
}

// params decodifica os parametros de uma requisicao. Campo desconhecido e
// erro: cliente e daemon de versoes diferentes falham alto, nao em silencio.
func params(m message, out any) *Error {
	dec := json.NewDecoder(bytes.NewReader(m.Params))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return &Error{Code: CodeInvalidParams, Message: "parametros invalidos para " + m.Method + ": " + err.Error()}
	}
	return nil
}

func actionError(err error) *Error {
	return &Error{Code: CodeActionFailed, Message: err.Error()}
}

func marshal(v any) (json.RawMessage, *Error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, actionError(err)
	}
	return b, nil
}

// shell atende o app.shell: abre o shell e manda o terminal (o mestre do
// pty) como descritor junto com a resposta. O daemon fecha a copia dele: o
// shell vive enquanto o cliente segura o terminal.
func (s *server) shell(c *conn, m message) {
	fail := func(e *Error) { c.send(message{ID: m.ID, Error: e}) }
	var p core.ShellRequest
	if err := params(m, &p); err != nil {
		fail(err)
		return
	}
	opener, isOpener := s.api.(core.ShellOpener)
	uc, isUnix := c.Conn.(*net.UnixConn)
	if !isOpener || !isUnix {
		fail(&Error{Code: CodeActionFailed, Message: "este daemon nao abre shell"})
		return
	}
	f, err := opener.OpenShell(s.work, p)
	if err != nil {
		fail(actionError(err))
		return
	}
	defer f.Close()
	b, _ := json.Marshal(message{JSONRPC: "2.0", ID: m.ID, Result: json.RawMessage(`{"ok":true}`)})
	c.wmu.Lock()
	defer c.wmu.Unlock()
	uc.WriteMsgUnix(append(b, '\n'), syscall.UnixRights(int(f.Fd())), nil)
}
