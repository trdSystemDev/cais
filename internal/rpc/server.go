package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"reflect"
	"sync"
	"sync/atomic"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/core"
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
			log.Printf("desligando: esperando %d chamadas em andamento terminarem", n)
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
	switch m.Method {
	case MethodStateGet:
		return wrap(s.api.Snapshot(s.work))

	case MethodEnvironmentCheck:
		return wrap(s.api.CheckEnvironment(s.work))

	case MethodContainerLogs:
		var p logsParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return wrap(s.api.ContainerLogs(c.ctx, p.Stack, p.Container))

	case MethodAppsCheck:
		return wrap(s.api.CheckApps(s.work))

	case MethodAppCreate:
		var p apps.App
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return wrap(s.api.CreateApp(s.work, p))

	case MethodAppUpdate:
		var p apps.App
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return ok(s.api.UpdateApp(s.work, p))

	case MethodAppRollback:
		var p deployParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return wrap(s.api.RollbackApp(s.work, p.Name, p.Ref))

	case MethodAppDeploy:
		var p deployParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return wrap(s.api.DeployApp(s.work, p.Name, p.Ref))

	case MethodAppPromoteCheck:
		var p appParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return wrap(s.api.CheckPromotion(s.work, p.Name))

	case MethodAppPromote:
		var p promoteParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return wrap(s.api.PromoteApp(s.work, p.Name, p.Confirm))

	case MethodAppRestart:
		var p appParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return ok(s.api.RestartApp(s.work, p.Name, p.Service))

	case MethodAppStop:
		var p appParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return ok(s.api.StopApp(s.work, p.Name))

	case MethodAppDelete:
		var p deleteParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return ok(s.api.DeleteApp(s.work, p.Name, p.Volumes))

	case MethodAppLogs:
		var p appParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return wrap(s.api.AppLogs(s.work, p.Name, p.Container))

	case MethodAppEnvGet:
		var p appParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return wrap(s.api.AppEnv(s.work, p.Name))

	case MethodAppEnvSet:
		var p envParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return ok(s.api.SetAppEnv(s.work, p.Name, p.Env))

	case MethodAppSecrets:
		var p appParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return wrap(s.api.AppSecrets(s.work, p.Name))

	case MethodAppSecretSet:
		var p appSecretParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return ok(s.api.SetAppSecret(s.work, p.Name, p.Secret, p.Value))

	case MethodAppSecretDel:
		var p appSecretParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return ok(s.api.DeleteAppSecret(s.work, p.Name, p.Secret))

	case MethodAppGuide:
		var p appParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		text, err := s.api.AppGuide(s.work, p.Name)
		return wrap(textResult{Text: text}, err)

	case MethodDatabaseCreate:
		var p core.DatabaseRequest
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return wrap(s.api.CreateDatabase(s.work, p))

	case MethodDatabases:
		return wrap(s.api.Databases(s.work))

	case MethodAppDBLink:
		var p appDBParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return ok(s.api.LinkAppDatabase(s.work, p.App, p.Engine, p.Name))

	case MethodAppDBUnlink:
		var p appDBParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return ok(s.api.UnlinkAppDatabase(s.work, p.App, p.Engine, p.Name))

	case MethodBucketCreate:
		var p bucketParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return wrap(s.api.CreateBucket(s.work, p.Name))

	case MethodBuckets:
		return wrap(s.api.Buckets(s.work))

	case MethodBucketDelete:
		var p bucketParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return ok(s.api.DeleteBucket(s.work, p.Name))

	case MethodDatabaseDelete:
		var p appDBParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return ok(s.api.DeleteDatabase(s.work, p.Engine, p.Name))

	case MethodAppBucketLink:
		var p appBucketParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return ok(s.api.LinkAppBucket(s.work, p.App, p.Bucket))

	case MethodBackupRefresh:
		return wrap(s.api.RefreshBackup(s.work))

	case MethodBackupClient:
		var p backupClientParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return ok(s.api.SetBackupClient(s.work, p.ID, p.Secret))

	case MethodBackupLogin:
		return wrap(s.api.StartDriveLogin(s.work))

	case MethodBackupLoginStop:
		return ok(s.api.CancelDriveLogin(s.work))

	case MethodBackupDisconnect:
		return ok(s.api.DisconnectDrive(s.work))

	case MethodBackupKey:
		key, err := s.api.NewBackupKey(s.work)
		return wrap(textResult{Text: key}, err)

	case MethodBackupSchedule:
		var p backupScheduleParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return ok(s.api.SetBackupSchedule(s.work, p.Time, p.Days))

	case MethodBackupRun:
		return wrap(s.api.RunBackup(s.work))

	case MethodBackupFiles:
		return wrap(s.api.BackupFiles(s.work))

	case MethodBackupRestore:
		var p core.RestoreRequest
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return wrap(s.api.RestoreBackup(s.work, p))

	case MethodAppBucketUnlink:
		var p appBucketParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return ok(s.api.UnlinkAppBucket(s.work, p.App, p.Bucket))

	case MethodInfraCheck:
		return wrap(s.api.CheckStacks(s.work))

	case MethodConfigGet:
		return wrap(s.api.LoadConfig(s.work))

	case MethodConfigSave:
		var cfg config.Config
		if err := params(m, &cfg); err != nil {
			return nil, err
		}
		return ok(s.api.SaveConfig(s.work, cfg))

	case MethodSecretSet:
		var p secretParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return ok(s.api.SetSecret(s.work, p.Name, p.Value))

	case MethodSetupPlan:
		return wrap(s.api.PlanSetup(s.work))

	case MethodSetupRun:
		return wrap(s.api.RunSetup(s.work))

	case MethodServiceSet:
		var p serviceParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return wrap(s.api.SetService(s.work, p.Name, p.On))

	case MethodStateSubscribe:
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

	default:
		return nil, &Error{Code: CodeMethodNotFound, Message: "metodo desconhecido: " + m.Method}
	}
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
