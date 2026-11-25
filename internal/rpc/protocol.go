// Package rpc expoe o core por um socket Unix (lado daemon) e implementa
// core.API do lado cliente (TUI, CLI e futuro MCP).
//
// Protocolo: JSON-RPC 2.0, uma mensagem JSON por linha. Requisicoes tem id
// maior que zero; mensagens sem id sao notificacoes do servidor (hoje so
// "state", com a foto nova do estado).
package rpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Metodos aceitos pelo daemon.
const (
	MethodStateGet         = "state.get"
	MethodStateSubscribe   = "state.subscribe"
	MethodEnvironmentCheck = "environment.check"
	MethodSystemStats      = "system.stats"
	MethodHealthCheck      = "health.check"
	MethodContainerStats   = "containers.stats"
	MethodTrafficStats     = "traffic.stats"
	MethodContainerLogs    = "infra.logs"
	MethodInfraCheck       = "infra.check"
	MethodConfigGet        = "config.get"
	MethodConfigSave       = "config.save" // params: config.Config
	MethodSecretSet        = "secret.set"  // params: secretParams
	MethodSetupPlan        = "setup.plan"
	MethodNotifyTest       = "notify.test"
	MethodHostHarden       = "host.harden"     // params: core.HardenRequest
	MethodHostFix          = "host.fix"        // params: hostFixParams
	MethodUpdate           = "cais.update"     // params: updateParams
	MethodHostConfirm      = "host.confirm"    // params: hostConfirmParams
	MethodGitHubTest       = "github.test"     // params: githubTestParams
	MethodServiceSet       = "service.set"     // params: serviceParams
	MethodServiceControl   = "service.control" // params: serviceControlParams
	MethodAppsCheck        = "apps.check"
	MethodAppCreate        = "app.create"        // params: apps.App
	MethodAppUpdate        = "app.update"        // params: apps.App
	MethodAppDeploy        = "app.deploy"        // params: deployParams
	MethodAppPromoteCheck  = "app.promote_check" // params: appParams
	MethodAppPromote       = "app.promote"       // params: promoteParams
	MethodAppRollback      = "app.rollback"      // params: deployParams (ref = commit)
	MethodAppRestart       = "app.restart"       // params: appParams (service opcional)
	MethodAppStop          = "app.stop"          // params: appParams
	MethodAppDelete        = "app.delete"        // params: deleteParams
	MethodAppLogs          = "app.logs"          // params: appParams (container)
	MethodAppEnvGet        = "app.env.get"       // params: appParams
	MethodAppEnvSet        = "app.env.set"       // params: envParams
	MethodAppGuide         = "app.guide"         // params: appParams
	// MethodAppShell abre o shell de um container: a resposta leva o
	// terminal (mestre do pty) como descritor (SCM_RIGHTS), numa conexao
	// so pra ele.
	MethodAppShell         = "app.shell" // params: core.ShellRequest
	MethodDatabaseCreate   = "db.create" // params: core.DatabaseRequest
	MethodDatabases        = "db.list"
	MethodAppDBLink        = "app.db.link"   // params: appDBParams
	MethodAppDBUnlink      = "app.db.unlink" // params: appDBParams
	MethodBucketCreate     = "bucket.create" // params: bucketParams
	MethodBuckets          = "bucket.list"
	MethodBucketDelete     = "bucket.delete"     // params: bucketParams
	MethodDatabaseDelete   = "db.delete"         // params: appDBParams (sem app)
	MethodAppBucketLink    = "app.bucket.link"   // params: appBucketParams
	MethodAppBucketUnlink  = "app.bucket.unlink" // params: appBucketParams
	MethodBackupRefresh    = "backup.refresh"
	MethodBackupProvider   = "backup.provider" // params: textResult (o destino)
	MethodBackupClient     = "backup.client"   // params: backupClientParams
	MethodBackupLogin      = "backup.login"
	MethodBackupLoginStop  = "backup.login.cancel"
	MethodBackupDisconnect = "backup.disconnect"
	MethodBackupKey        = "backup.key"
	MethodBackupSchedule   = "backup.schedule" // params: backupScheduleParams
	MethodBackupRun        = "backup.run"
	MethodBackupFiles      = "backup.files"
	MethodBackupRestore    = "backup.restore" // params: core.RestoreRequest

	// NotifyState e a notificacao enviada a cada mudanca de estado depois
	// de um state.subscribe.
	NotifyState = "state"
)

// Codigos de erro do JSON-RPC 2.0 (os negativos sao reservados pela spec).
const (
	CodeParseError     = -32700
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeActionFailed   = -32000
)

// ErrDisconnected e devolvido pelo cliente quando a conexao com o daemon
// caiu ou foi fechada.
var ErrDisconnected = errors.New("conexao com o daemon encerrada")

// Error e o erro devolvido pelo daemon numa resposta.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	// Data e o resultado parcial de uma acao que falhou, quando ha (ex: o
	// deploy recusado traz a lista problems, um motivo por item).
	Data json.RawMessage `json:"data,omitempty"`
}

// Error de uma acao que falhou e so a mensagem dela, que ja diz o que
// fazer; os codigos de protocolo continuam aparecendo porque indicam
// cliente e daemon desencontrados.
func (e *Error) Error() string {
	if e.Code == CodeActionFailed {
		return e.Message
	}
	return fmt.Sprintf("daemon: %s (codigo %d)", e.Message, e.Code)
}

// message cobre requisicao, resposta e notificacao numa struct so: cada
// tipo usa um subconjunto dos campos.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      uint64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

type secretParams struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type serviceParams struct {
	Name string `json:"name"`
	On   bool   `json:"on"`
}

type serviceControlParams struct {
	Name   string `json:"name"`
	Action string `json:"action"` // start, stop ou restart
}

type logsParams struct {
	Stack     string `json:"stack"`
	Container string `json:"container"`
}

type appParams struct {
	Name      string `json:"name"`
	Service   string `json:"service,omitempty"`
	Container string `json:"container,omitempty"`
}

type deleteParams struct {
	Name string `json:"name"`
	// Volumes apaga tambem os volumes do compose (os dados do app).
	Volumes bool `json:"volumes,omitempty"`
}

type promoteParams struct {
	Name string `json:"name"` // a homologacao
	// Confirm aceita voltar a producao pra um commit mais velho.
	Confirm bool `json:"confirm,omitempty"`
}

type deployParams struct {
	Name string `json:"name"`
	// Ref e a branch ou o commit; vazio e o mais novo da branch do app.
	Ref string `json:"ref,omitempty"`
}

type envParams struct {
	Name string            `json:"name"`
	Env  map[string]string `json:"env"`
}

type appDBParams struct {
	App    string `json:"app"`
	Engine string `json:"engine"`
	Name   string `json:"name"`
}

type bucketParams struct {
	Name string `json:"name"`
}

type appBucketParams struct {
	App    string `json:"app"`
	Bucket string `json:"bucket"`
}

type backupClientParams struct {
	ID     string `json:"id"`
	Secret string `json:"secret"`
}

type backupScheduleParams struct {
	Time string `json:"time"`
	Days int    `json:"days"`
}

// updateParams: Version e a tag (vazia: o release mais novo).
type updateParams struct {
	Version string `json:"version,omitempty"`
}

// hostFixParams e o item da checagem de seguranca a corrigir.
type hostFixParams struct {
	ID string `json:"id"`
}

// hostConfirmParams: Since e o inicio da sessao que confirma.
type hostConfirmParams struct {
	Since time.Time `json:"since"`
}

type textResult struct {
	Text string `json:"text"`
}

// githubTestParams e o GitHub App a testar (zero/vazio: o salvo).
type githubTestParams struct {
	AppID int64  `json:"app_id,omitempty"`
	Key   string `json:"key,omitempty"`
}
