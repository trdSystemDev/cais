package core

import (
	"context"

	"github.com/trdSystemDev/cais/internal/bootstrap"
	"github.com/trdSystemDev/cais/internal/config"
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
}

// ConfigInfo e o config.yaml atual.
type ConfigInfo struct {
	Exists bool          `json:"exists"`
	Path   string        `json:"path"`
	Config config.Config `json:"config"`
}

var _ API = (*Core)(nil)
