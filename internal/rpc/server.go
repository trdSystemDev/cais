package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"sync/atomic"

	"github.com/trdSystemDev/cais/internal/core"
)

// Serve atende conexoes em ln ate ctx ser cancelado. Cada conexao pode
// fazer varias chamadas em paralelo e receber notificacoes de estado.
//
// As acoes rodam com o ctx do daemon, nao com o da conexao: se a TUI cair
// no meio de um deploy, o deploy termina mesmo assim em vez de ficar pela
// metade no servidor.
func Serve(ctx context.Context, ln net.Listener, api core.API) error {
	s := &server{api: api, ctx: ctx, conns: map[net.Conn]struct{}{}}

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
	ctx context.Context
	wg  sync.WaitGroup

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
