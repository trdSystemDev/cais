package core

import (
	"context"

	"github.com/trdSystemDev/cais/internal/apps"
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
	// SetSecret grava um secret que o usuario informa (ex: a chave do
	// GitHub App). Secrets gerados pelo cais nao podem ser sobrescritos.
	SetSecret(ctx context.Context, name, value string) error

	// PlanSetup diz o que o Setup faria agora, sem mudar nada.
	PlanSetup(ctx context.Context) (bootstrap.Plan, error)
	// RunSetup executa o Setup, publicando o progresso em State.Setup.
	RunSetup(ctx context.Context) (Setup, error)
	// SetService liga ou desliga um servico opcional (postgres, mysql,
	// garage) e aplica na hora, pelo Setup.
	SetService(ctx context.Context, name string, on bool) (Setup, error)

	// CheckApps le os apps e os containers deles e publica em State.Apps.
	CheckApps(ctx context.Context) (Apps, error)
	// CreateApp cria o app (repositorio, branch, compose); o codigo so vem
	// no deploy.
	CreateApp(ctx context.Context, a apps.App) (apps.App, error)
	// UpdateApp troca repositorio, branch e compose (vale no proximo deploy).
	UpdateApp(ctx context.Context, a apps.App) error
	// DeployApp baixa o commit de ref (vazio = o mais novo da branch), faz
	// o build com a versao antiga no ar e sobe; se falhar, a anterior volta.
	DeployApp(ctx context.Context, name, ref string) (apps.DeployResult, error)
	// RestartApp reinicia um service do app (ou todos, com service vazio).
	RestartApp(ctx context.Context, name, service string) error
	// StopApp para o app sem apagar nada.
	StopApp(ctx context.Context, name string) error
	// DeleteApp remove o app (containers, imagens, codigo, variaveis e
	// secrets). Com volumes, apaga tambem os dados dos volumes. O database
	// da infra fica (aba Infra).
	DeleteApp(ctx context.Context, name string, volumes bool) error
	// AppLogs le as ultimas linhas de um container do app.
	AppLogs(ctx context.Context, name, container string) (string, error)
	// AppEnv le as variaveis do app (o .env do projeto no deploy).
	AppEnv(ctx context.Context, name string) (map[string]string, error)
	// SetAppEnv troca as variaveis do app; valem no proximo deploy.
	SetAppEnv(ctx context.Context, name string, env map[string]string) error
	// AppSecrets lista os secrets do app (nomes, nunca valores).
	AppSecrets(ctx context.Context, name string) ([]apps.Secret, error)
	// SetAppSecret grava um secret do app (arquivo em ${CAIS_SECRETS}).
	SetAppSecret(ctx context.Context, name, secret, value string) error
	// DeleteAppSecret apaga um secret do app.
	DeleteAppSecret(ctx context.Context, name, secret string) error
	// AppGuide e o texto de ajuda pra escrever o compose de producao do app
	// (labels do Traefik, redes, variaveis e secrets).
	AppGuide(ctx context.Context, name string) (string, error)

	// CreateDatabase cria um database e um usuario com senha nova no
	// Postgres ou MySQL da infra e devolve a credencial.
	CreateDatabase(ctx context.Context, req DatabaseRequest) (DatabaseInfo, error)
	// Databases lista os databases criados pelo cais, com os apps que usam
	// cada um e o espaco ocupado.
	Databases(ctx context.Context) ([]DatabaseInfo, error)
	// LinkAppDatabase liga o app a um database da infra: o deploy grava
	// ${CAIS_SECRETS}/<engine>_<nome>_user e <engine>_<nome>_pass.
	LinkAppDatabase(ctx context.Context, app, engine, name string) error
	// UnlinkAppDatabase desliga o app do database (o database e os dados ficam).
	UnlinkAppDatabase(ctx context.Context, app, engine, name string) error
	// CreateBucket cria um bucket no Garage e uma chave so dele.
	CreateBucket(ctx context.Context, name string) (BucketInfo, error)
	// Buckets lista os buckets criados pelo cais, com os apps que usam
	// cada um e o espaco ocupado.
	Buckets(ctx context.Context) ([]BucketInfo, error)
	// LinkAppBucket liga o app a um bucket: o deploy grava
	// ${CAIS_SECRETS}/s3_<bucket>_access_key e s3_<bucket>_secret_key.
	LinkAppBucket(ctx context.Context, app, bucket string) error
	// UnlinkAppBucket desliga o app do bucket (o bucket e os arquivos ficam).
	UnlinkAppBucket(ctx context.Context, app, bucket string) error
}

// ConfigInfo e o config.yaml atual.
type ConfigInfo struct {
	Exists bool          `json:"exists"`
	Path   string        `json:"path"`
	Config config.Config `json:"config"`
}

var _ API = (*Core)(nil)
