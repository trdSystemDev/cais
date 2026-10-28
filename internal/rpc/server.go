package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/core"
)

// Serve atende conexoes em ln ate ctx ser cancelado. Cada conexao pode
// fazer varias chamadas em paralelo e receber notificacoes de estado.
//
// As acoes rodam com o ctx do daemon, nao com o da conexao: se a TUI cair
// no meio de um deploy, o deploy termina mesmo assim em vez de ficar pela
// metade no servidor.
func Serve(ctx context.Context, ln net.Listener, api core.API, opts ...Option) error {
	s := &server{api: api, ctx: ctx, conns: map[net.Conn]struct{}{}}
	for _, o := range opts {
		o(s)
	}

	go func() {
		<-ctx.Done()
		ln.Close()
		s.closeAll()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				s.wg.Wait()
				return nil
			}
			return err
		}
		peer, err := s.admit(conn)
		if err != nil {
			s.logf("conexao recusada: %v", err)
			conn.Close()
			continue
		}
		if !s.track(conn) {
			conn.Close()
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.untrack(conn)
			s.handle(conn, peer)
		}()
	}
}

// Peer e quem conectou no socket, segundo o kernel.
type Peer struct {
	UID, GID uint32
	PID      int32
}

// Option ajusta um Serve.
type Option func(*server)

// Allow restringe o socket a esses metodos: os outros sao recusados mesmo
// pra quem conseguiu conectar. E o socket de deploy do CI, que so pode
// pedir o deploy de uma tag.
func Allow(methods ...string) Option {
	return func(s *server) {
		s.allow = map[string]bool{}
		for _, m := range methods {
			s.allow[m] = true
		}
	}
}

// RequirePeer recusa a conexao se check devolver erro. check recebe quem
// conectou (SO_PEERCRED); sem como identificar, a conexao e recusada.
func RequirePeer(check func(Peer) error) Option {
	return func(s *server) { s.peerCheck = check }
}

// Log recebe o registro das conexoes recusadas e das chamadas num socket
// restrito (quem pediu o que).
func Log(fn func(format string, args ...any)) Option {
	return func(s *server) { s.log = fn }
}

type server struct {
	api core.API
	ctx context.Context
	wg  sync.WaitGroup

	allow     map[string]bool // nil = todos os metodos
	peerCheck func(Peer) error
	log       func(format string, args ...any)

	mu       sync.Mutex
	conns    map[net.Conn]struct{}
	shutdown bool
}

func (s *server) logf(format string, args ...any) {
	if s.log != nil {
		s.log(format, args...)
	}
}

// admit identifica quem conectou e aplica o RequirePeer.
func (s *server) admit(c net.Conn) (Peer, error) {
	if s.peerCheck == nil {
		peer, _ := peerCred(c) // so pro registro; sem checagem, todos entram
		return peer, nil
	}
	peer, err := peerCred(c)
	if err != nil {
		return peer, err
	}
	if err := s.peerCheck(peer); err != nil {
		return peer, fmt.Errorf("uid %d: %w", peer.UID, err)
	}
	return peer, nil
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

func (s *server) handle(nc net.Conn, peer Peer) {
	ctx, cancel := context.WithCancel(s.ctx)
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
		go func() {
			if s.allow != nil {
				if !s.allow[m.Method] {
					s.logf("uid %d pediu %s: nao permitido neste socket", peer.UID, m.Method)
					c.send(message{ID: m.ID, Error: &Error{Code: CodeForbidden, Message: "metodo " + m.Method + " nao permitido neste socket"}})
					return
				}
				s.logf("uid %d: %s %s", peer.UID, m.Method, m.Params)
			}
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
		return wrap(s.api.Snapshot(s.ctx))

	case MethodEnvironmentCheck:
		return wrap(s.api.CheckEnvironment(s.ctx))

	case MethodContainerLogs:
		var p logsParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return wrap(s.api.ContainerLogs(c.ctx, p.Stack, p.Container))

	case MethodAppsCheck:
		return wrap(s.api.CheckApps(s.ctx))

	case MethodAppCreate:
		var p appParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return wrap(s.api.CreateApp(s.ctx, p.Name))

	case MethodAppRoutes:
		var p routesParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return ok(s.api.SetAppRoutes(s.ctx, p.Name, p.Routes))

	case MethodAppDeploy:
		var p deployParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return wrap(s.api.DeployApp(s.ctx, p.Name, p.Compose))

	case MethodAppRestart:
		var p appParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return ok(s.api.RestartApp(s.ctx, p.Name, p.Service))

	case MethodAppStop:
		var p appParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return ok(s.api.StopApp(s.ctx, p.Name))

	case MethodAppDatabase:
		var p dbParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return ok(s.api.SetAppDatabase(s.ctx, p.Name, p.Database))

	case MethodAppDatabaseOff:
		var p appParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return ok(s.api.RemoveAppDatabase(s.ctx, p.Name))

	case MethodAppPlan:
		var p planParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return wrap(s.api.PlanApp(s.ctx, p.Name, p.Files))

	case MethodAppApply:
		var p apps.Plan
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return ok(s.api.ApplyApp(s.ctx, p))

	case MethodAppSpec:
		var p specParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return ok(s.api.SetAppSpec(s.ctx, p.Name, p.Spec))

	case MethodAppDeployTag:
		var p tagParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return wrap(s.api.DeployAppTag(s.ctx, p.Name, p.Tag))

	case MethodAppSecrets:
		var p appParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return wrap(s.api.AppSecrets(s.ctx, p.Name))

	case MethodAppSecretSet:
		var p appSecretParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return ok(s.api.SetAppSecret(s.ctx, p.Name, p.Secret, p.Value))

	case MethodAppEnv:
		var p envParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return ok(s.api.SetAppEnv(s.ctx, p.Name, p.Env, p.Apply))

	case MethodAppPlanGitHub:
		var p repoParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return wrap(s.api.PlanAppFromGitHub(s.ctx, p.Repo))

	case MethodAppCIPR:
		var p appParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		link, err := s.api.ProposeAppCI(s.ctx, p.Name)
		return wrap(linkResult{URL: link}, err)

	case MethodCISetup:
		return wrap(s.api.CISetup(s.ctx))

	case MethodAppLogs:
		var p appParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return wrap(s.api.AppLogs(s.ctx, p.Name, p.Container))

	case MethodInfraCheck:
		return wrap(s.api.CheckStacks(s.ctx))

	case MethodConfigGet:
		return wrap(s.api.LoadConfig(s.ctx))

	case MethodConfigSave:
		var cfg config.Config
		if err := params(m, &cfg); err != nil {
			return nil, err
		}
		return ok(s.api.SaveConfig(s.ctx, cfg))

	case MethodSecretSet:
		var p secretParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return ok(s.api.SetSecret(s.ctx, p.Name, p.Value))

	case MethodSetupPlan:
		return wrap(s.api.PlanSetup(s.ctx))

	case MethodSetupRun:
		return wrap(s.api.RunSetup(s.ctx))

	case MethodServiceSet:
		var p serviceParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return wrap(s.api.SetService(s.ctx, p.Name, p.On))

	case MethodRegistryUsers:
		return wrap(s.api.RegistryUsers(s.ctx))

	case MethodRegistryUserAdd:
		var p userParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		pw, err := s.api.AddRegistryUser(s.ctx, p.User)
		return wrap(passwordResult{Password: pw}, err)

	case MethodRegistryUserDel:
		var p userParams
		if err := params(m, &p); err != nil {
			return nil, err
		}
		return ok(s.api.RemoveRegistryUser(s.ctx, p.User))

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
		return nil, actionError(err)
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
