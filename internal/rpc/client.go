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

// result chama method e devolve o resultado decodificado (num erro, o que
// a acao conseguiu devolver antes de falhar).
func result[R any](c *Client, ctx context.Context, method string, in any) (R, error) {
	var out R
	err := c.call(ctx, method, in, &out)
	return out, err
}

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

func (c *Client) Snapshot(ctx context.Context) (core.State, error) {
	return result[core.State](c, ctx, MethodStateGet, nil)
}

func (c *Client) CheckEnvironment(ctx context.Context) (core.Environment, error) {
	return result[core.Environment](c, ctx, MethodEnvironmentCheck, nil)
}

func (c *Client) SystemStats(ctx context.Context) (core.SystemStats, error) {
	return result[core.SystemStats](c, ctx, MethodSystemStats, nil)
}

func (c *Client) CheckHealth(ctx context.Context) (core.Health, error) {
	return result[core.Health](c, ctx, MethodHealthCheck, nil)
}

func (c *Client) ContainerStats(ctx context.Context) (core.ContainerUsage, error) {
	return result[core.ContainerUsage](c, ctx, MethodContainerStats, nil)
}

func (c *Client) CheckStacks(ctx context.Context) (core.Infra, error) {
	return result[core.Infra](c, ctx, MethodInfraCheck, nil)
}

func (c *Client) LoadConfig(ctx context.Context) (core.ConfigInfo, error) {
	return result[core.ConfigInfo](c, ctx, MethodConfigGet, nil)
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

func (c *Client) TestGitHub(ctx context.Context, appID int64, key string) (github.Check, error) {
	return result[github.Check](c, ctx, MethodGitHubTest, githubTestParams{AppID: appID, Key: key})
}

func (c *Client) PlanSetup(ctx context.Context) (bootstrap.Plan, error) {
	return result[bootstrap.Plan](c, ctx, MethodSetupPlan, nil)
}

// SetService bloqueia ate o Setup que aplica a mudanca terminar.
func (c *Client) SetService(ctx context.Context, name string, on bool) (core.Setup, error) {
	return result[core.Setup](c, ctx, MethodServiceSet, serviceParams{Name: name, On: on})
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

func (c *Client) ContainerLogs(ctx context.Context, stack, container string) (string, error) {
	return result[string](c, ctx, MethodContainerLogs, logsParams{Stack: stack, Container: container})
}

func (c *Client) CheckApps(ctx context.Context) (core.Apps, error) {
	return result[core.Apps](c, ctx, MethodAppsCheck, nil)
}

func (c *Client) CreateApp(ctx context.Context, a apps.App) (apps.App, error) {
	return result[apps.App](c, ctx, MethodAppCreate, a)
}

func (c *Client) UpdateApp(ctx context.Context, a apps.App) error {
	return c.call(ctx, MethodAppUpdate, a, nil)
}

// DeployApp bloqueia ate o deploy terminar (build e healthchecks). Se o
// cliente desistir, o deploy continua no daemon.
func (c *Client) DeployApp(ctx context.Context, name, ref string) (apps.DeployResult, error) {
	return result[apps.DeployResult](c, ctx, MethodAppDeploy, deployParams{Name: name, Ref: ref})
}

func (c *Client) CheckPromotion(ctx context.Context, from string) (apps.Promotion, error) {
	return result[apps.Promotion](c, ctx, MethodAppPromoteCheck, appParams{Name: from})
}

// PromoteApp bloqueia ate o deploy da producao terminar.
func (c *Client) PromoteApp(ctx context.Context, from string, confirm bool) (apps.DeployResult, error) {
	return result[apps.DeployResult](c, ctx, MethodAppPromote, promoteParams{Name: from, Confirm: confirm})
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

func (c *Client) AppLogs(ctx context.Context, name, container string) (string, error) {
	return result[string](c, ctx, MethodAppLogs, appParams{Name: name, Container: container})
}

func (c *Client) AppEnv(ctx context.Context, name string) (map[string]string, error) {
	return result[map[string]string](c, ctx, MethodAppEnvGet, appParams{Name: name})
}

func (c *Client) SetAppEnv(ctx context.Context, name string, env map[string]string) error {
	return c.call(ctx, MethodAppEnvSet, envParams{Name: name, Env: env}, nil)
}

func (c *Client) AppGuide(ctx context.Context, name string) (string, error) {
	var r textResult
	err := c.call(ctx, MethodAppGuide, appParams{Name: name}, &r)
	return r.Text, err
}

func (c *Client) CreateDatabase(ctx context.Context, req core.DatabaseRequest) (core.DatabaseInfo, error) {
	return result[core.DatabaseInfo](c, ctx, MethodDatabaseCreate, req)
}

func (c *Client) LinkAppDatabase(ctx context.Context, app, engine, name string) error {
	return c.call(ctx, MethodAppDBLink, appDBParams{App: app, Engine: engine, Name: name}, nil)
}

func (c *Client) UnlinkAppDatabase(ctx context.Context, app, engine, name string) error {
	return c.call(ctx, MethodAppDBUnlink, appDBParams{App: app, Engine: engine, Name: name}, nil)
}

func (c *Client) Databases(ctx context.Context) ([]core.DatabaseInfo, error) {
	return result[[]core.DatabaseInfo](c, ctx, MethodDatabases, nil)
}

func (c *Client) CreateBucket(ctx context.Context, name string) (core.BucketInfo, error) {
	return result[core.BucketInfo](c, ctx, MethodBucketCreate, bucketParams{Name: name})
}

func (c *Client) DeleteDatabase(ctx context.Context, engine, name string) error {
	return c.call(ctx, MethodDatabaseDelete, appDBParams{Engine: engine, Name: name}, nil)
}

func (c *Client) DeleteBucket(ctx context.Context, name string) error {
	return c.call(ctx, MethodBucketDelete, bucketParams{Name: name}, nil)
}

func (c *Client) Buckets(ctx context.Context) ([]core.BucketInfo, error) {
	return result[[]core.BucketInfo](c, ctx, MethodBuckets, nil)
}

func (c *Client) LinkAppBucket(ctx context.Context, app, bucket string) error {
	return c.call(ctx, MethodAppBucketLink, appBucketParams{App: app, Bucket: bucket}, nil)
}

func (c *Client) UnlinkAppBucket(ctx context.Context, app, bucket string) error {
	return c.call(ctx, MethodAppBucketUnlink, appBucketParams{App: app, Bucket: bucket}, nil)
}

func (c *Client) RefreshBackup(ctx context.Context) (core.BackupStatus, error) {
	return result[core.BackupStatus](c, ctx, MethodBackupRefresh, nil)
}

func (c *Client) SetBackupProvider(ctx context.Context, provider string) error {
	return c.call(ctx, MethodBackupProvider, textResult{Text: provider}, nil)
}

func (c *Client) SetBackupClient(ctx context.Context, id, secret string) error {
	return c.call(ctx, MethodBackupClient, backupClientParams{ID: id, Secret: secret}, nil)
}

func (c *Client) StartDriveLogin(ctx context.Context) (core.DriveLogin, error) {
	return result[core.DriveLogin](c, ctx, MethodBackupLogin, nil)
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

func (c *Client) RunBackup(ctx context.Context) (core.BackupRun, error) {
	return result[core.BackupRun](c, ctx, MethodBackupRun, nil)
}

func (c *Client) BackupFiles(ctx context.Context) ([]backup.File, error) {
	return result[[]backup.File](c, ctx, MethodBackupFiles, nil)
}

func (c *Client) RestoreBackup(ctx context.Context, req core.RestoreRequest) (core.RestoreResult, error) {
	return result[core.RestoreResult](c, ctx, MethodBackupRestore, req)
}

func (c *Client) RollbackApp(ctx context.Context, name, commit string) (apps.DeployResult, error) {
	return result[apps.DeployResult](c, ctx, MethodAppRollback, deployParams{Name: name, Ref: commit})
}
