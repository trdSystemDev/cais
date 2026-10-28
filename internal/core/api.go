package core

import (
	"context"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/bootstrap"
	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/detect"
)

// API e o contrato que toda interface usa pra falar com o nucleo. *Core
// implementa direto (mesmo processo); rpc.Client implementa via socket do
// daemon. A interface nao sabe -- e nao deve saber -- qual das duas tem.
//
// Toda acao nova do core entra aqui, no rpc (servidor e cliente) e, quando
// existir, no MCP.
type API interface {
	// Snapshot devolve a foto atual do estado.
	Snapshot(ctx context.Context) (State, error)

	// Subscribe entrega o estado atual e depois cada mudanca (sempre a mais
	// recente). O canal fecha quando a funcao devolvida e chamada ou quando
	// a conexao com o daemon cai.
	Subscribe(ctx context.Context) (<-chan State, func(), error)

	// CheckEnvironment checa Docker e sistema e devolve o resultado.
	CheckEnvironment(ctx context.Context) (Environment, error)
	// CheckStacks pergunta ao docker o estado das stacks base.
	CheckStacks(ctx context.Context) (Infra, error)

	ContainerLogs(ctx context.Context, stack, container string) (string, error)

	// LoadConfig le o config.yaml. Sem arquivo, devolve Exists falso e a
	// config padrao (ponto de partida do Setup).
	LoadConfig(ctx context.Context) (ConfigInfo, error)
	// SaveConfig valida (inclusive as stacks) e grava o config.yaml.
	SaveConfig(ctx context.Context, c config.Config) error
	// SetSecret grava um secret que o usuario informa (ex: o client secret
	// do GitHub). Secrets gerados pelo cais nao podem ser sobrescritos.
	SetSecret(ctx context.Context, name, value string) error

	// PlanSetup diz o que o Setup faria agora, sem mudar nada.
	PlanSetup(ctx context.Context) (bootstrap.Plan, error)
	// RunSetup executa o Setup, publicando o progresso em State.Setup.
	RunSetup(ctx context.Context) (Setup, error)
	// SetService liga ou desliga um servico opcional (postgres, mysql,
	// garage) e aplica na hora, pelo Setup.
	SetService(ctx context.Context, name string, on bool) (Setup, error)

	// RegistryUsers lista quem pode entrar no registry.
	RegistryUsers(ctx context.Context) ([]string, error)
	// AddRegistryUser cria o acesso de uma pessoa e devolve a senha, que
	// nao fica guardada: e a unica vez que ela aparece.
	AddRegistryUser(ctx context.Context, user string) (string, error)
	// RemoveRegistryUser tira o acesso de uma pessoa.
	RemoveRegistryUser(ctx context.Context, user string) error

	// CheckApps le os apps e os containers deles e publica em State.Apps.
	CheckApps(ctx context.Context) (Apps, error)
	// CreateApp cria um app vazio (pasta, secrets/); o primeiro deploy sobe.
	CreateApp(ctx context.Context, name string) (apps.App, error)
	// SetAppRoutes troca as rotas (dominio -> service:porta) do app. Um
	// dominio pertence a um app so; app no ar e atualizado na hora.
	SetAppRoutes(ctx context.Context, name string, routes []apps.Route) error
	// DeployApp valida e sobe um compose novo do app. Se falhar com uma
	// versao no ar, ela volta e o erro diz isso.
	DeployApp(ctx context.Context, name, compose string) (apps.DeployResult, error)
	// RestartApp reinicia um service do app (ou todos, com service vazio).
	RestartApp(ctx context.Context, name, service string) error
	// StopApp para o app sem apagar nada.
	StopApp(ctx context.Context, name string) error
	// AppLogs le as ultimas linhas de um container do app.
	AppLogs(ctx context.Context, name, container string) (string, error)
	// SetAppDatabase cria database e usuario do app no Postgres/MySQL
	// compartilhado e injeta a conexao. env: VAR -> host, port, name, user,
	// password, password_file ou url (vazio = DB_HOST, DB_PORT, ...).
	SetAppDatabase(ctx context.Context, name, engine string, services []string, env map[string]string) error
	// RemoveAppDatabase desliga o app do banco sem apagar os dados.
	RemoveAppDatabase(ctx context.Context, name string) error

	// PlanApp analisa os arquivos do repositorio (so os de detect.Wanted) e
	// propoe a configuracao do app. Nao grava nada.
	PlanApp(ctx context.Context, name string, files detect.Files) (apps.Plan, error)
	// ApplyApp aplica um plano revisado: app, spec, rotas e banco.
	ApplyApp(ctx context.Context, p apps.Plan) error
	// SetAppSpec troca a spec (vale a partir do proximo deploy por tag).
	SetAppSpec(ctx context.Context, name string, spec apps.Spec) error
	// DeployAppTag gera o compose da spec com a tag e faz o deploy.
	DeployAppTag(ctx context.Context, name, tag string) (apps.DeployResult, error)
	// AppSecrets lista os secrets do app (nomes, nunca valores).
	AppSecrets(ctx context.Context, name string) ([]apps.Secret, error)
	// SetAppSecret grava um secret do app; com o app no ar, recria os containers.
	SetAppSecret(ctx context.Context, name, secret, value string) error
}

// ConfigInfo e o config.yaml atual.
type ConfigInfo struct {
	Exists bool          `json:"exists"`
	Path   string        `json:"path"`
	Config config.Config `json:"config"`
}

var _ API = (*Core)(nil)
