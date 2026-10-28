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

	"github.com/trdSystemDev/cais/internal/apps"
)

// Metodos aceitos pelo daemon.
const (
	MethodStateGet         = "state.get"
	MethodStateSubscribe   = "state.subscribe"
	MethodEnvironmentCheck = "environment.check"
	MethodContainerLogs    = "infra.logs"
	MethodInfraCheck       = "infra.check"
	MethodConfigGet        = "config.get"
	MethodConfigSave       = "config.save" // params: config.Config
	MethodSecretSet        = "secret.set"  // params: secretParams
	MethodSetupPlan        = "setup.plan"
	MethodSetupRun         = "setup.run"
	MethodServiceSet       = "service.set" // params: serviceParams
	MethodRegistryUsers    = "registry.users"
	MethodRegistryUserAdd  = "registry.user.add"    // params: userParams
	MethodRegistryUserDel  = "registry.user.remove" // params: userParams
	MethodAppsCheck        = "apps.check"
	MethodAppCreate        = "app.create"  // params: appParams
	MethodAppRoutes        = "app.routes"  // params: routesParams
	MethodAppDeploy        = "app.deploy"  // params: deployParams
	MethodAppRestart       = "app.restart" // params: appParams (service opcional)
	MethodAppStop          = "app.stop"    // params: appParams
	MethodAppLogs          = "app.logs"    // params: appParams (container)
	MethodAppDatabase      = "app.db"      // params: dbParams
	MethodAppDatabaseOff   = "app.db.off"  // params: appParams

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

type userParams struct {
	User string `json:"user"`
}

type passwordResult struct {
	Password string `json:"password"`
}

type serviceParams struct {
	Name string `json:"name"`
	On   bool   `json:"on"`
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

type routesParams struct {
	Name   string       `json:"name"`
	Routes []apps.Route `json:"routes"`
}

type deployParams struct {
	Name    string `json:"name"`
	Compose string `json:"compose"`
}

type dbParams struct {
	Name     string            `json:"name"`
	Engine   string            `json:"engine"`
	Services []string          `json:"services,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
}
