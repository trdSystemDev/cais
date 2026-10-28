package rpc

import (
	"context"
	"encoding/json"
	"net"
	"sync"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/bootstrap"
	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/detect"
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

func (c *Client) ContainerLogs(ctx context.Context, stack, container string) (out string, err error) {
	err = c.call(ctx, MethodContainerLogs, logsParams{Stack: stack, Container: container}, &out)
	return
}

func (c *Client) CheckApps(ctx context.Context) (a core.Apps, err error) {
	err = c.call(ctx, MethodAppsCheck, nil, &a)
	return
}

func (c *Client) CreateApp(ctx context.Context, name string) (a apps.App, err error) {
	err = c.call(ctx, MethodAppCreate, appParams{Name: name}, &a)
	return
}

func (c *Client) SetAppRoutes(ctx context.Context, name string, routes []apps.Route) error {
	return c.call(ctx, MethodAppRoutes, routesParams{Name: name, Routes: routes}, nil)
}

// DeployApp bloqueia ate o deploy terminar (espera os healthchecks). Se o
// cliente desistir, o deploy continua no daemon.
func (c *Client) DeployApp(ctx context.Context, name, compose string) (r apps.DeployResult, err error) {
	err = c.call(ctx, MethodAppDeploy, deployParams{Name: name, Compose: compose}, &r)
	return
}

func (c *Client) RestartApp(ctx context.Context, name, service string) error {
	return c.call(ctx, MethodAppRestart, appParams{Name: name, Service: service}, nil)
}

func (c *Client) StopApp(ctx context.Context, name string) error {
	return c.call(ctx, MethodAppStop, appParams{Name: name}, nil)
}

func (c *Client) AppLogs(ctx context.Context, name, container string) (out string, err error) {
	err = c.call(ctx, MethodAppLogs, appParams{Name: name, Container: container}, &out)
	return
}

func (c *Client) SetAppDatabase(ctx context.Context, name, engine string, services []string, env map[string]string) error {
	return c.call(ctx, MethodAppDatabase, dbParams{Name: name, Engine: engine, Services: services, Env: env}, nil)
}

func (c *Client) RemoveAppDatabase(ctx context.Context, name string) error {
	return c.call(ctx, MethodAppDatabaseOff, appParams{Name: name}, nil)
}

func (c *Client) PlanApp(ctx context.Context, name string, files detect.Files) (p apps.Plan, err error) {
	err = c.call(ctx, MethodAppPlan, planParams{Name: name, Files: files}, &p)
	return
}

func (c *Client) ApplyApp(ctx context.Context, p apps.Plan) error {
	return c.call(ctx, MethodAppApply, p, nil)
}

func (c *Client) SetAppSpec(ctx context.Context, name string, spec apps.Spec) error {
	return c.call(ctx, MethodAppSpec, specParams{Name: name, Spec: spec}, nil)
}

// DeployAppTag bloqueia ate o deploy terminar; se o cliente desistir, o
// deploy continua no daemon.
func (c *Client) DeployAppTag(ctx context.Context, name, tag string) (r apps.DeployResult, err error) {
	err = c.call(ctx, MethodAppDeployTag, tagParams{Name: name, Tag: tag}, &r)
	return
}

func (c *Client) AppSecrets(ctx context.Context, name string) (list []apps.Secret, err error) {
	err = c.call(ctx, MethodAppSecrets, appParams{Name: name}, &list)
	return
}

func (c *Client) SetAppSecret(ctx context.Context, name, secret, value string) error {
	return c.call(ctx, MethodAppSecretSet, appSecretParams{Name: name, Secret: secret, Value: value}, nil)
}

func (c *Client) CISetup(ctx context.Context) (info core.CIInfo, err error) {
	err = c.call(ctx, MethodCISetup, nil, &info)
	return
}
