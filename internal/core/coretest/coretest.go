// Package coretest tem um core falso pros testes de quem usa a core.API
// (rpc, CLI): grava cada chamada com os argumentos e devolve o que o teste
// preparou. E um gerador de valores com todos os campos preenchidos.
package coretest

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/backup"
	"github.com/trdSystemDev/cais/internal/bootstrap"
	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/docker"
	"github.com/trdSystemDev/cais/internal/github"
	"github.com/trdSystemDev/cais/internal/host"
)

// Call e uma chamada que o core falso recebeu (sem o ctx).
type Call struct {
	Method string
	Args   []any
}

// Recorder e um core.API que so grava o que recebe e devolve o que o teste
// preparou (SetOut, SetErr).
type Recorder struct {
	// State e o estado que o Subscribe entrega.
	State core.State

	mu    sync.Mutex
	calls []Call
	out   map[string]any
	err   error
}

// NewRecorder cria o core falso, sem resultados preparados.
func NewRecorder() *Recorder { return &Recorder{out: map[string]any{}} }

// SetOut prepara o que o metodo devolve.
func (f *Recorder) SetOut(method string, v any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.out[method] = v
}

// SetErr faz todo metodo devolver err (nil volta ao normal).
func (f *Recorder) SetErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

var _ core.API = (*Recorder)(nil)

func (f *Recorder) record(m string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, Call{m, args})
}

// Take devolve as chamadas recebidas desde a ultima vez e esquece.
func (f *Recorder) Take() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.calls
	f.calls = nil
	return c
}

// ret grava a chamada e devolve o resultado preparado pro metodo.
func ret[R any](f *Recorder, m string, args ...any) (R, error) {
	f.record(m, args...)
	f.mu.Lock()
	defer f.mu.Unlock()
	r, _ := f.out[m].(R)
	return r, f.err
}

// only grava a chamada de um metodo que so devolve erro.
func only(f *Recorder, m string, args ...any) error {
	f.record(m, args...)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

func (f *Recorder) Snapshot(context.Context) (core.State, error) {
	return ret[core.State](f, "Snapshot")
}
func (f *Recorder) Subscribe(context.Context) (<-chan core.State, func(), error) {
	f.record("Subscribe")
	ch := make(chan core.State, 1)
	ch <- f.State
	var once sync.Once
	return ch, func() { once.Do(func() { close(ch) }) }, f.err
}
func (f *Recorder) CheckEnvironment(context.Context) (core.Environment, error) {
	return ret[core.Environment](f, "CheckEnvironment")
}
func (f *Recorder) SystemStats(context.Context) (core.SystemStats, error) {
	return ret[core.SystemStats](f, "SystemStats")
}
func (f *Recorder) TrafficStats(context.Context) (core.Traffic, error) {
	return ret[core.Traffic](f, "TrafficStats")
}
func (f *Recorder) ContainerStats(context.Context) (core.ContainerUsage, error) {
	return ret[core.ContainerUsage](f, "ContainerStats")
}
func (f *Recorder) CheckHealth(context.Context) (core.Health, error) {
	return ret[core.Health](f, "CheckHealth")
}
func (f *Recorder) CheckUpdate(context.Context) (core.Health, error) {
	return ret[core.Health](f, "CheckUpdate")
}
func (f *Recorder) CheckStacks(context.Context) (core.Infra, error) {
	return ret[core.Infra](f, "CheckStacks")
}
func (f *Recorder) ContainerLogs(_ context.Context, stack, container string, opt docker.LogsOptions) (string, error) {
	return ret[string](f, "ContainerLogs", stack, container, opt)
}
func (f *Recorder) LoadConfig(context.Context) (core.ConfigInfo, error) {
	return ret[core.ConfigInfo](f, "LoadConfig")
}
func (f *Recorder) SaveConfig(_ context.Context, c config.Config) error {
	return only(f, "SaveConfig", c)
}
func (f *Recorder) SetSecret(_ context.Context, name, value string) error {
	return only(f, "SetSecret", name, value)
}
func (f *Recorder) TestNotify(context.Context) error { return only(f, "TestNotify") }
func (f *Recorder) HardenHost(_ context.Context, req core.HardenRequest) error {
	return only(f, "HardenHost", req)
}
func (f *Recorder) GitHubConnectLink(_ context.Context, org string) (string, error) {
	return ret[string](f, "GitHubConnectLink", org)
}
func (f *Recorder) ConnectGitHub(_ context.Context, code string) (core.GitHubConnected, error) {
	return ret[core.GitHubConnected](f, "ConnectGitHub", code)
}
func (f *Recorder) UpdateCais(_ context.Context, version string) (core.UpdateResult, error) {
	return ret[core.UpdateResult](f, "UpdateCais", version)
}
func (f *Recorder) FixHostItem(_ context.Context, id string) (string, error) {
	return ret[string](f, "FixHostItem", id)
}
func (f *Recorder) ConnectTailscale(context.Context) (host.Tailscale, error) {
	return ret[host.Tailscale](f, "ConnectTailscale")
}
func (f *Recorder) CloseSSHPublic(context.Context) error { return only(f, "CloseSSHPublic") }
func (f *Recorder) ConfirmHost(_ context.Context, since time.Time) error {
	return only(f, "ConfirmHost", since)
}
func (f *Recorder) TestGitHub(_ context.Context, appID int64, key string) (github.Check, error) {
	return ret[github.Check](f, "TestGitHub", appID, key)
}
func (f *Recorder) PlanSetup(context.Context) (bootstrap.Plan, error) {
	return ret[bootstrap.Plan](f, "PlanSetup")
}
func (f *Recorder) SetService(_ context.Context, name string, on bool) (core.Setup, error) {
	return ret[core.Setup](f, "SetService", name, on)
}
func (f *Recorder) ControlService(_ context.Context, name, action string) error {
	return only(f, "ControlService", name, action)
}
func (f *Recorder) CheckApps(context.Context) (core.Apps, error) {
	return ret[core.Apps](f, "CheckApps")
}
func (f *Recorder) CreateApp(_ context.Context, a apps.App) (apps.App, error) {
	return ret[apps.App](f, "CreateApp", a)
}
func (f *Recorder) UpdateApp(_ context.Context, a apps.App) error {
	return only(f, "UpdateApp", a)
}
func (f *Recorder) DeployApp(_ context.Context, name, ref string) (apps.DeployResult, error) {
	return ret[apps.DeployResult](f, "DeployApp", name, ref)
}
func (f *Recorder) CheckPromotion(_ context.Context, from string) (apps.Promotion, error) {
	return ret[apps.Promotion](f, "CheckPromotion", from)
}
func (f *Recorder) PromoteApp(_ context.Context, from string, confirm bool) (apps.DeployResult, error) {
	return ret[apps.DeployResult](f, "PromoteApp", from, confirm)
}
func (f *Recorder) RollbackApp(_ context.Context, name, commit string) (apps.DeployResult, error) {
	return ret[apps.DeployResult](f, "RollbackApp", name, commit)
}
func (f *Recorder) RestartApp(_ context.Context, name, service string) error {
	return only(f, "RestartApp", name, service)
}
func (f *Recorder) StopApp(_ context.Context, name string) error {
	return only(f, "StopApp", name)
}
func (f *Recorder) DeleteApp(_ context.Context, name string, volumes bool) error {
	return only(f, "DeleteApp", name, volumes)
}
func (f *Recorder) AppLogs(_ context.Context, name, container string, opt docker.LogsOptions) (string, error) {
	return ret[string](f, "AppLogs", name, container, opt)
}
func (f *Recorder) AppEnv(_ context.Context, name string) (map[string]string, error) {
	return ret[map[string]string](f, "AppEnv", name)
}
func (f *Recorder) SetAppEnv(_ context.Context, name string, env map[string]string) error {
	return only(f, "SetAppEnv", name, env)
}
func (f *Recorder) AppGuide(_ context.Context, name string) (string, error) {
	return ret[string](f, "AppGuide", name)
}
func (f *Recorder) CreateDatabase(_ context.Context, req core.DatabaseRequest) (core.DatabaseInfo, error) {
	return ret[core.DatabaseInfo](f, "CreateDatabase", req)
}
func (f *Recorder) Databases(context.Context) ([]core.DatabaseInfo, error) {
	return ret[[]core.DatabaseInfo](f, "Databases")
}
func (f *Recorder) LinkAppDatabase(_ context.Context, app, engine, name string) error {
	return only(f, "LinkAppDatabase", app, engine, name)
}
func (f *Recorder) UnlinkAppDatabase(_ context.Context, app, engine, name string) error {
	return only(f, "UnlinkAppDatabase", app, engine, name)
}
func (f *Recorder) DeleteDatabase(_ context.Context, engine, name string) error {
	return only(f, "DeleteDatabase", engine, name)
}
func (f *Recorder) CreateBucket(_ context.Context, name string) (core.BucketInfo, error) {
	return ret[core.BucketInfo](f, "CreateBucket", name)
}
func (f *Recorder) DeleteBucket(_ context.Context, name string) error {
	return only(f, "DeleteBucket", name)
}
func (f *Recorder) Buckets(context.Context) ([]core.BucketInfo, error) {
	return ret[[]core.BucketInfo](f, "Buckets")
}
func (f *Recorder) LinkAppBucket(_ context.Context, app, bucket string) error {
	return only(f, "LinkAppBucket", app, bucket)
}
func (f *Recorder) UnlinkAppBucket(_ context.Context, app, bucket string) error {
	return only(f, "UnlinkAppBucket", app, bucket)
}
func (f *Recorder) RefreshBackup(context.Context) (core.BackupStatus, error) {
	return ret[core.BackupStatus](f, "RefreshBackup")
}
func (f *Recorder) SetBackupProvider(_ context.Context, provider string) error {
	return only(f, "SetBackupProvider", provider)
}

func (f *Recorder) StartDriveLogin(context.Context) (core.DriveLogin, error) {
	return ret[core.DriveLogin](f, "StartDriveLogin")
}
func (f *Recorder) CancelDriveLogin(context.Context) error { return only(f, "CancelDriveLogin") }
func (f *Recorder) DisconnectDrive(context.Context) error  { return only(f, "DisconnectDrive") }
func (f *Recorder) NewBackupKey(context.Context) (string, error) {
	return ret[string](f, "NewBackupKey")
}
func (f *Recorder) SetBackupSchedule(_ context.Context, at string, days int) error {
	return only(f, "SetBackupSchedule", at, days)
}
func (f *Recorder) RunBackup(context.Context) (core.BackupRun, error) {
	return ret[core.BackupRun](f, "RunBackup")
}
func (f *Recorder) BackupFiles(context.Context) ([]backup.File, error) {
	return ret[[]backup.File](f, "BackupFiles")
}
func (f *Recorder) RestoreBackup(_ context.Context, req core.RestoreRequest) (core.RestoreResult, error) {
	return ret[core.RestoreResult](f, "RestoreBackup", req)
}

// Filler preenche qualquer valor com dados nao zero e diferentes entre si:
// cada string leva o caminho do campo e cada numero um contador, entao dois
// campos trocados no caminho (Name no lugar de Service) nunca batem.
type Filler struct {
	n    int
	base time.Time
}

// NewFiller cria o gerador de valores.
func NewFiller() *Filler {
	return &Filler{base: time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)}
}

// Value devolve um valor do tipo t com todos os campos preenchidos.
func (f *Filler) Value(t reflect.Type, path string) reflect.Value {
	v := reflect.New(t).Elem()
	f.fill(v, path, 0)
	return v
}

func (f *Filler) fill(v reflect.Value, path string, depth int) {
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
