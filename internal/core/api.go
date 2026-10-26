package core

import "context"

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
}

var _ API = (*Core)(nil)
