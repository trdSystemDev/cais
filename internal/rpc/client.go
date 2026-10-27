package rpc

import (
	"context"
	"encoding/json"
	"net"
	"sync"

	"github.com/trdSystemDev/cais/internal/bootstrap"
	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/core"
)

// Client fala com o daemon pelo socket e implementa core.API: quem usa
// (TUI, CLI, MCP) nao percebe diferenca entre ele e um *core.Core local.
//
// Uma conexao serve todas as chamadas e inscricoes. Se ela cair, chamadas
// pendentes e futuras devolvem ErrDisconnected e os canais de Subscribe
// sao fechados.
type Client struct {
	conn net.Conn
	wmu  sync.Mutex
	enc  *json.Encoder

	mu         sync.Mutex
	nextID     uint64
	pending    map[uint64]chan message
	subs       map[int]chan core.State
	nextSub    int
	last       *core.State // ultima foto recebida, entregue a novos inscritos
	subscribed bool        // ja pediu state.subscribe ao daemon
	closed     bool
}

var _ core.API = (*Client)(nil)

// Dial conecta no daemon pelo socket em path.
func Dial(ctx context.Context, path string) (*Client, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	c := &Client{
		conn:    conn,
		enc:     json.NewEncoder(conn),
		pending: map[uint64]chan message{},
		subs:    map[int]chan core.State{},
	}
	go c.readLoop()
	return c, nil
}

// Close encerra a conexao. Inscricoes abertas tem o canal fechado.
func (c *Client) Close() error {
	return c.conn.Close()
}

func (c *Client) Snapshot(ctx context.Context) (st core.State, err error) {
	err = c.call(ctx, MethodStateGet, nil, &st)
	return
}

func (c *Client) CheckEnvironment(ctx context.Context) (env core.Environment, err error) {
	err = c.call(ctx, MethodEnvironmentCheck, nil, &env)
	return
}

func (c *Client) CheckStacks(ctx context.Context) (infra core.Infra, err error) {
	err = c.call(ctx, MethodInfraCheck, nil, &infra)
	return
}

func (c *Client) LoadConfig(ctx context.Context) (info core.ConfigInfo, err error) {
	err = c.call(ctx, MethodConfigGet, nil, &info)
	return
}

func (c *Client) SaveConfig(ctx context.Context, cfg config.Config) error {
	return c.call(ctx, MethodConfigSave, cfg, nil)
}

func (c *Client) SetSecret(ctx context.Context, name, value string) error {
	return c.call(ctx, MethodSecretSet, secretParams{Name: name, Value: value}, nil)
}

func (c *Client) PlanSetup(ctx context.Context) (p bootstrap.Plan, err error) {
	err = c.call(ctx, MethodSetupPlan, nil, &p)
	return
}

// RunSetup bloqueia ate o Setup terminar (pode levar minutos); o progresso
// chega pelo Subscribe. Se o cliente desistir, o Setup continua no daemon.
func (c *Client) RunSetup(ctx context.Context) (st core.Setup, err error) {
	err = c.call(ctx, MethodSetupRun, nil, &st)
	return
}

// SetService bloqueia ate o Setup que aplica a mudanca terminar.
func (c *Client) SetService(ctx context.Context, name string, on bool) (st core.Setup, err error) {
	err = c.call(ctx, MethodServiceSet, serviceParams{Name: name, On: on}, &st)
	return
}

func (c *Client) RegistryUsers(ctx context.Context) (users []string, err error) {
	err = c.call(ctx, MethodRegistryUsers, nil, &users)
	return
}

func (c *Client) AddRegistryUser(ctx context.Context, user string) (string, error) {
	var r passwordResult
	err := c.call(ctx, MethodRegistryUserAdd, userParams{User: user}, &r)
	return r.Password, err
}

func (c *Client) RemoveRegistryUser(ctx context.Context, user string) error {
	return c.call(ctx, MethodRegistryUserDel, userParams{User: user}, nil)
}

// Subscribe tem a mesma semantica do core: recebe a foto atual (se o
// cliente ja tiver uma) e depois sempre a mais recente. Varias inscricoes
// no mesmo cliente compartilham uma unica inscricao no daemon.
func (c *Client) Subscribe(ctx context.Context) (<-chan core.State, func(), error) {
	ch := make(chan core.State, 1)

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, nil, ErrDisconnected
	}
	id := c.nextSub
	c.nextSub++
	c.subs[id] = ch
	if c.last != nil {
		ch <- *c.last
	}
	needRemote := !c.subscribed
	c.subscribed = true
	c.mu.Unlock()

	unsubscribe := func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		// readLoop pode ter fechado todos ao perder a conexao: so fecha
		// quem ainda esta no mapa, assim ninguem fecha o canal duas vezes
		if _, ok := c.subs[id]; ok {
			delete(c.subs, id)
			close(ch)
		}
	}

	if needRemote {
		if err := c.call(ctx, MethodStateSubscribe, nil, nil); err != nil {
			c.mu.Lock()
			c.subscribed = false
			c.mu.Unlock()
			unsubscribe()
			return nil, nil, err
		}
	}
	return ch, unsubscribe, nil
}

func (c *Client) call(ctx context.Context, method string, in, out any) error {
	var params json.RawMessage
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		params = b
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrDisconnected
	}
	c.nextID++
	id := c.nextID
	reply := make(chan message, 1)
	c.pending[id] = reply
	c.mu.Unlock()

	c.wmu.Lock()
	err := c.enc.Encode(message{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	c.wmu.Unlock()
	if err != nil {
		c.forget(id)
		return ErrDisconnected
	}

	select {
	case m, ok := <-reply:
		if !ok {
			return ErrDisconnected
		}
		if m.Error != nil {
			return m.Error
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(m.Result, out)
	case <-ctx.Done():
		c.forget(id)
		return ctx.Err()
	}
}

func (c *Client) forget(id uint64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *Client) readLoop() {
	dec := json.NewDecoder(c.conn)
	for {
		var m message
		if err := dec.Decode(&m); err != nil {
			break
		}
		if m.ID != 0 {
			c.mu.Lock()
			reply, ok := c.pending[m.ID]
			delete(c.pending, m.ID)
			c.mu.Unlock()
			if ok {
				reply <- m
			}
			continue
		}
		if m.Method == NotifyState {
			var st core.State
			if json.Unmarshal(m.Params, &st) == nil {
				c.publish(st)
			}
		}
	}

	c.mu.Lock()
	c.closed = true
	for id, reply := range c.pending {
		close(reply)
		delete(c.pending, id)
	}
	for id, ch := range c.subs {
		close(ch)
		delete(c.subs, id)
	}
	c.mu.Unlock()
	c.conn.Close()
}

// publish entrega a foto a todos os inscritos locais, descartando a
// anterior se ainda nao foi lida. Todo envio nos canais acontece com c.mu,
// entao depois de esvaziar o envio nunca bloqueia.
func (c *Client) publish(st core.State) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last != nil && st.Version < c.last.Version {
		return // foto atrasada (ex: resposta inicial depois de uma mudanca)
	}
	c.last = &st
	for _, ch := range c.subs {
		select {
		case <-ch:
		default:
		}
		ch <- st
	}
}
