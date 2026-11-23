package rpc

import (
	"context"
	"encoding/json"
	"net"
	"sync"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/backup"
	"github.com/trdSystemDev/cais/internal/bootstrap"
	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/github"
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

func (c *Client) TestNotify(ctx context.Context) error {
	return c.call(ctx, MethodNotifyTest, nil, nil)
}

func (c *Client) TestGitHub(ctx context.Context, appID int64, key string) (r github.Check, err error) {
	err = c.call(ctx, MethodGitHubTest, githubTestParams{AppID: appID, Key: key}, &r)
	return
}

func (c *Client) PlanSetup(ctx context.Context) (p bootstrap.Plan, err error) {
	err = c.call(ctx, MethodSetupPlan, nil, &p)
	return
}

// SetService bloqueia ate o Setup que aplica a mudanca terminar.
func (c *Client) SetService(ctx context.Context, name string, on bool) (st core.Setup, err error) {
	err = c.call(ctx, MethodServiceSet, serviceParams{Name: name, On: on}, &st)
	return
}

func (c *Client) ControlService(ctx context.Context, name, action string) error {
	return c.call(ctx, MethodServiceControl, serviceControlParams{Name: name, Action: action}, nil)
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
			if out != nil && len(m.Error.Data) > 0 {
				json.Unmarshal(m.Error.Data, out)
			}
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

func (c *Client) CreateApp(ctx context.Context, a apps.App) (out apps.App, err error) {
	err = c.call(ctx, MethodAppCreate, a, &out)
	return
}

func (c *Client) UpdateApp(ctx context.Context, a apps.App) error {
	return c.call(ctx, MethodAppUpdate, a, nil)
}

// DeployApp bloqueia ate o deploy terminar (build e healthchecks). Se o
// cliente desistir, o deploy continua no daemon.
func (c *Client) DeployApp(ctx context.Context, name, ref string) (r apps.DeployResult, err error) {
	err = c.call(ctx, MethodAppDeploy, deployParams{Name: name, Ref: ref}, &r)
	return
}

func (c *Client) CheckPromotion(ctx context.Context, from string) (p apps.Promotion, err error) {
	err = c.call(ctx, MethodAppPromoteCheck, appParams{Name: from}, &p)
	return
}

// PromoteApp bloqueia ate o deploy da producao terminar.
func (c *Client) PromoteApp(ctx context.Context, from string, confirm bool) (r apps.DeployResult, err error) {
	err = c.call(ctx, MethodAppPromote, promoteParams{Name: from, Confirm: confirm}, &r)
	return
}

func (c *Client) RestartApp(ctx context.Context, name, service string) error {
	return c.call(ctx, MethodAppRestart, appParams{Name: name, Service: service}, nil)
}

func (c *Client) StopApp(ctx context.Context, name string) error {
	return c.call(ctx, MethodAppStop, appParams{Name: name}, nil)
}

func (c *Client) DeleteApp(ctx context.Context, name string, volumes bool) error {
	return c.call(ctx, MethodAppDelete, deleteParams{Name: name, Volumes: volumes}, nil)
}

func (c *Client) AppLogs(ctx context.Context, name, container string) (out string, err error) {
	err = c.call(ctx, MethodAppLogs, appParams{Name: name, Container: container}, &out)
	return
}

func (c *Client) AppEnv(ctx context.Context, name string) (env map[string]string, err error) {
	err = c.call(ctx, MethodAppEnvGet, appParams{Name: name}, &env)
	return
}

func (c *Client) SetAppEnv(ctx context.Context, name string, env map[string]string) error {
	return c.call(ctx, MethodAppEnvSet, envParams{Name: name, Env: env}, nil)
}

func (c *Client) AppSecrets(ctx context.Context, name string) (list []apps.Secret, err error) {
	err = c.call(ctx, MethodAppSecrets, appParams{Name: name}, &list)
	return
}

func (c *Client) SetAppSecret(ctx context.Context, name, secret, value string) error {
	return c.call(ctx, MethodAppSecretSet, appSecretParams{Name: name, Secret: secret, Value: value}, nil)
}

func (c *Client) DeleteAppSecret(ctx context.Context, name, secret string) error {
	return c.call(ctx, MethodAppSecretDel, appSecretParams{Name: name, Secret: secret}, nil)
}

func (c *Client) AppGuide(ctx context.Context, name string) (string, error) {
	var r textResult
	err := c.call(ctx, MethodAppGuide, appParams{Name: name}, &r)
	return r.Text, err
}

func (c *Client) CreateDatabase(ctx context.Context, req core.DatabaseRequest) (info core.DatabaseInfo, err error) {
	err = c.call(ctx, MethodDatabaseCreate, req, &info)
	return
}

func (c *Client) LinkAppDatabase(ctx context.Context, app, engine, name string) error {
	return c.call(ctx, MethodAppDBLink, appDBParams{App: app, Engine: engine, Name: name}, nil)
}

func (c *Client) UnlinkAppDatabase(ctx context.Context, app, engine, name string) error {
	return c.call(ctx, MethodAppDBUnlink, appDBParams{App: app, Engine: engine, Name: name}, nil)
}

func (c *Client) Databases(ctx context.Context) (list []core.DatabaseInfo, err error) {
	err = c.call(ctx, MethodDatabases, nil, &list)
	return
}

func (c *Client) CreateBucket(ctx context.Context, name string) (info core.BucketInfo, err error) {
	err = c.call(ctx, MethodBucketCreate, bucketParams{Name: name}, &info)
	return
}

func (c *Client) DeleteDatabase(ctx context.Context, engine, name string) error {
	return c.call(ctx, MethodDatabaseDelete, appDBParams{Engine: engine, Name: name}, nil)
}

func (c *Client) DeleteBucket(ctx context.Context, name string) error {
	return c.call(ctx, MethodBucketDelete, bucketParams{Name: name}, nil)
}

func (c *Client) Buckets(ctx context.Context) (list []core.BucketInfo, err error) {
	err = c.call(ctx, MethodBuckets, nil, &list)
	return
}

func (c *Client) LinkAppBucket(ctx context.Context, app, bucket string) error {
	return c.call(ctx, MethodAppBucketLink, appBucketParams{App: app, Bucket: bucket}, nil)
}

func (c *Client) UnlinkAppBucket(ctx context.Context, app, bucket string) error {
	return c.call(ctx, MethodAppBucketUnlink, appBucketParams{App: app, Bucket: bucket}, nil)
}

func (c *Client) RefreshBackup(ctx context.Context) (st core.BackupStatus, err error) {
	err = c.call(ctx, MethodBackupRefresh, nil, &st)
	return
}

func (c *Client) SetBackupProvider(ctx context.Context, provider string) error {
	return c.call(ctx, MethodBackupProvider, textResult{Text: provider}, nil)
}

func (c *Client) SetBackupClient(ctx context.Context, id, secret string) error {
	return c.call(ctx, MethodBackupClient, backupClientParams{ID: id, Secret: secret}, nil)
}

func (c *Client) StartDriveLogin(ctx context.Context) (login core.DriveLogin, err error) {
	err = c.call(ctx, MethodBackupLogin, nil, &login)
	return
}

func (c *Client) CancelDriveLogin(ctx context.Context) error {
	return c.call(ctx, MethodBackupLoginStop, nil, nil)
}

func (c *Client) DisconnectDrive(ctx context.Context) error {
	return c.call(ctx, MethodBackupDisconnect, nil, nil)
}

func (c *Client) NewBackupKey(ctx context.Context) (string, error) {
	var r textResult
	err := c.call(ctx, MethodBackupKey, nil, &r)
	return r.Text, err
}

func (c *Client) SetBackupSchedule(ctx context.Context, at string, days int) error {
	return c.call(ctx, MethodBackupSchedule, backupScheduleParams{Time: at, Days: days}, nil)
}

func (c *Client) RunBackup(ctx context.Context) (run core.BackupRun, err error) {
	err = c.call(ctx, MethodBackupRun, nil, &run)
	return
}

func (c *Client) BackupFiles(ctx context.Context) (files []backup.File, err error) {
	err = c.call(ctx, MethodBackupFiles, nil, &files)
	return
}

func (c *Client) RestoreBackup(ctx context.Context, req core.RestoreRequest) (res core.RestoreResult, err error) {
	err = c.call(ctx, MethodBackupRestore, req, &res)
	return
}

func (c *Client) RollbackApp(ctx context.Context, name, commit string) (res apps.DeployResult, err error) {
	err = c.call(ctx, MethodAppRollback, deployParams{Name: name, Ref: commit}, &res)
	return
}
