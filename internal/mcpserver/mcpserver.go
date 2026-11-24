// Package mcpserver expoe o cais como servidor MCP: cada ferramenta chama
// uma acao da core.API (as mesmas da TUI e do CLI, com as mesmas regras).
//
// Nada que apaga dados nem o Setup da VPS entra aqui: excluir app, banco ou
// bucket, ligar ou desligar servico e a config da VPS ficam so com
// o humano, pela TUI ou pelo CLI.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/core"
)

const instructions = `cais administra uma VPS com Docker: proxy (Traefik), bancos Postgres/MySQL
compartilhados, storage S3 (Garage) e os apps. Cada app e um repositorio do
GitHub com o compose de producao pronto; o cais baixa um commit, faz o build
com a versao antiga no ar e sobe. Se o up falhar, a versao anterior volta.

Fluxo de um app novo:
1. app_create (nome, repositorio, branch, arquivo de compose).
2. app_guide: o modelo de compose de producao (labels do Traefik, redes e o
   entrypoint com o cais run, que injeta as variaveis no processo do app).
   O compose mora no repositorio do app: escreva la e faca push.
3. Banco/bucket se precisar: database_create ou bucket_create e depois
   app_link_database ou app_link_bucket (o app recebe a credencial como
   variaveis, ex: POSTGRES_<NOME>_URL).
4. app_env_set: todas as variaveis do app, senhas e chaves inclusive.
5. app_deploy. Se o compose estiver fora do formato do cais, o deploy recusa
   antes do build e diz o motivo e a correcao: corrija no repositorio e tente
   de novo. Depois do deploy, apps_list e app_logs mostram como ficou.
   Se a versao nova subiu mas esta com bug, app_rollback volta pra anterior
   (sem build) enquanto a correcao nao chega.

Homologacao: e um app com promote_to apontando pra producao (mesmo
repositorio, compose proprio, ex: docker-compose.homolog.yaml, e rotas com o
nome dela). Valide nela e use app_promote_check e app_promote pra levar o
commit pra producao; o deploy direto da producao pula a validacao.
auto_deploy faz o deploy sozinho a cada commit novo na branch do app.

Variaveis valem no proximo deploy. O cais run as injeta no processo do app:
nada vai pra config do container (docker inspect). Valores nunca sao
devolvidos aqui, so os nomes.

Excluir app, banco ou bucket, o Setup da VPS e ligar ou desligar
servicos (Postgres, MySQL, Garage) nao estao disponiveis aqui, de proposito:
peca ao usuario pra fazer pela TUI ou pelo CLI do cais. Se database_create ou
bucket_create disser que a stack esta desligada, e isso que falta.`

// New monta o servidor MCP sobre a API do cais.
func New(api core.API, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "cais", Version: version}, &mcp.ServerOptions{Instructions: instructions})
	t := tools{api}
	t.register(s)
	return s
}

type tools struct{ api core.API }

var (
	no  = false
	yes = true
)

// anotacoes: leitura, mudanca que se repete sem efeito novo, e mudanca
func readOnly() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &no}
}
func idempotent() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{DestructiveHint: &no, IdempotentHint: true, OpenWorldHint: &no}
}
func action() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{DestructiveHint: &no, OpenWorldHint: &yes}
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// entradas das ferramentas: o schema sai das tags (json e jsonschema)
type (
	noArgs  struct{}
	appArgs struct {
		App string `json:"app" jsonschema:"nome do app"`
	}
	logsArgs struct {
		App       string `json:"app" jsonschema:"nome do app"`
		Container string `json:"container" jsonschema:"nome do container, como aparece em apps_list (ex: portal-web-1)"`
	}
	infraLogsArgs struct {
		Stack     string `json:"stack" jsonschema:"stack da infra: proxy, postgres, mysql ou garage"`
		Container string `json:"container" jsonschema:"nome do container, como aparece em infra_status"`
	}
	envSetArgs struct {
		App   string            `json:"app" jsonschema:"nome do app"`
		Set   map[string]string `json:"set,omitempty" jsonschema:"variaveis a criar ou trocar (NOME: valor)"`
		Unset []string          `json:"unset,omitempty" jsonschema:"variaveis a remover"`
	}
	// envNames sao so os nomes: o valor de uma variavel nunca volta pela IA
	envNames struct {
		Vars  []string `json:"vars" jsonschema:"variaveis gravadas no app"`
		Infra []string `json:"infra,omitempty" jsonschema:"variaveis que os bancos e buckets ligados entregam no deploy"`
	}
	appCreateArgs struct {
		Name       string `json:"name" jsonschema:"nome do app: minusculas, numeros e -"`
		Repo       string `json:"repo" jsonschema:"repositorio do GitHub: dono/repo ou o link"`
		Branch     string `json:"branch,omitempty" jsonschema:"branch do deploy (padrao: a principal do repositorio)"`
		Compose    string `json:"compose,omitempty" jsonschema:"compose de producao a partir da raiz do repositorio (padrao docker-compose.yml)"`
		PromoteTo  string `json:"promote_to,omitempty" jsonschema:"o app novo e a homologacao deste app (a producao, mesmo repositorio)"`
		AutoDeploy bool   `json:"auto_deploy,omitempty" jsonschema:"deploy automatico a cada commit novo na branch"`
	}
	appUpdateArgs struct {
		App        string `json:"app" jsonschema:"nome do app"`
		Repo       string `json:"repo,omitempty" jsonschema:"novo repositorio (vazio mantem)"`
		Branch     string `json:"branch,omitempty" jsonschema:"nova branch (vazio mantem)"`
		Compose    string `json:"compose,omitempty" jsonschema:"novo arquivo de compose (vazio mantem)"`
		PromoteTo  string `json:"promote_to,omitempty" jsonschema:"vira homologacao deste app (off desfaz; vazio mantem)"`
		AutoDeploy *bool  `json:"auto_deploy,omitempty" jsonschema:"deploy automatico a cada commit novo na branch (ausente mantem)"`
	}
	promoteArgs struct {
		App     string `json:"app" jsonschema:"a homologacao"`
		Confirm bool   `json:"confirm,omitempty" jsonschema:"aceita voltar a producao pra um commit mais velho (so com o ok do usuario)"`
	}
	rollbackArgs struct {
		App    string `json:"app" jsonschema:"nome do app"`
		Commit string `json:"commit,omitempty" jsonschema:"commit do historico pra voltar (vazio: o deploy anterior ao no ar)"`
	}
	deployArgs struct {
		App string `json:"app" jsonschema:"nome do app"`
		Ref string `json:"ref,omitempty" jsonschema:"branch ou commit (vazio: o mais novo da branch do app)"`
	}
	restartArgs struct {
		App     string `json:"app" jsonschema:"nome do app"`
		Service string `json:"service,omitempty" jsonschema:"service do compose (vazio: todos)"`
	}
	dbCreateArgs struct {
		Engine     string   `json:"engine" jsonschema:"postgres ou mysql"`
		Name       string   `json:"name" jsonschema:"nome do database e do usuario: minusculas, numeros e _"`
		Extensions []string `json:"extensions,omitempty" jsonschema:"extensoes do Postgres (ex: postgis)"`
	}
	dbLinkArgs struct {
		App    string `json:"app" jsonschema:"nome do app"`
		Engine string `json:"engine" jsonschema:"postgres ou mysql"`
		Name   string `json:"name" jsonschema:"nome do database"`
	}
	bucketArgs struct {
		Name string `json:"name" jsonschema:"nome do bucket: 3 a 63 minusculas, numeros e -"`
	}
	bucketLinkArgs struct {
		App    string `json:"app" jsonschema:"nome do app"`
		Bucket string `json:"bucket" jsonschema:"nome do bucket"`
	}
)

func (t tools) register(s *mcp.Server) {
	t.registerRead(s)
	t.registerApps(s)
	t.registerAppOps(s)
	t.registerData(s)
}

// registerRead registra as ferramentas so de leitura: estado, logs, listas.
func (t tools) registerRead(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "status", Annotations: readOnly(),
		Description: "Estado geral da VPS: ambiente (Docker), Setup, stacks da infra e apps com os containers."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, core.State, error) {
			st, err := t.api.Snapshot(ctx)
			return nil, st, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "infra_status", Annotations: readOnly(),
		Description: "Pergunta ao Docker o estado das stacks da infra (proxy, postgres, mysql, garage) e dos containers delas."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, core.Infra, error) {
			infra, err := t.api.CheckStacks(ctx)
			return nil, infra, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "infra_logs", Annotations: readOnly(),
		Description: "Ultimas linhas do log de um container da infra."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in infraLogsArgs) (*mcp.CallToolResult, any, error) {
			out, err := t.api.ContainerLogs(ctx, in.Stack, in.Container)
			return text(out), nil, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "apps_list", Annotations: readOnly(),
		Description: "Lista os apps: repositorio, branch, compose, commit no ar, ultimo deploy, containers (estado e saude), bancos e buckets ligados."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, core.Apps, error) {
			list, err := t.api.CheckApps(ctx)
			return nil, list, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "app_logs", Annotations: readOnly(),
		Description: "Ultimas linhas do log de um container do app (o nome do container sai de apps_list)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in logsArgs) (*mcp.CallToolResult, any, error) {
			out, err := t.api.AppLogs(ctx, in.App, in.Container)
			return text(out), nil, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "app_guide", Annotations: readOnly(),
		Description: "Modelo do compose de producao do app no formato do cais: labels do Traefik, redes da infra, o entrypoint com o cais run e os nomes das variaveis. Use antes de escrever ou corrigir o compose."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in appArgs) (*mcp.CallToolResult, any, error) {
			out, err := t.api.AppGuide(ctx, in.App)
			return text(out), nil, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "app_env_get", Annotations: readOnly(),
		Description: "Nomes das variaveis do app e das que os bancos e buckets ligados entregam. O valor nunca e devolvido (pode ser senha)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in appArgs) (*mcp.CallToolResult, envNames, error) {
			env, err := t.api.AppEnv(ctx, in.App)
			if err != nil {
				return nil, envNames{}, err
			}
			out := envNames{Vars: slices.Sorted(maps.Keys(env))}
			if a, err := t.app(ctx, in.App); err == nil {
				out.Infra = apps.InfraVars(a)
			}
			return nil, out, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "databases_list", Annotations: readOnly(),
		Description: "Databases criados pelo cais no Postgres/MySQL da infra: host, variaveis que o app recebe, apps ligados e espaco ocupado (sem a senha)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, []core.DatabaseInfo, error) {
			list, err := t.api.Databases(ctx)
			return nil, list, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "buckets_list", Annotations: readOnly(),
		Description: "Buckets do Garage criados pelo cais: endpoint, variaveis que o app recebe, apps ligados e espaco ocupado (sem a chave)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, []core.BucketInfo, error) {
			list, err := t.api.Buckets(ctx)
			return nil, list, err
		})
}

// registerApps registra cadastro, deploy, promocao e rollback de apps.
func (t tools) registerApps(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "app_create", Annotations: action(),
		Description: "Cadastra um app (nao faz deploy). O codigo vem do GitHub pelo GitHub App do cais no deploy."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in appCreateArgs) (*mcp.CallToolResult, apps.App, error) {
			a, err := t.api.CreateApp(ctx, apps.App{Name: in.Name, Repo: in.Repo, Branch: in.Branch, Compose: in.Compose,
				PromoteTo: in.PromoteTo, AutoDeploy: in.AutoDeploy})
			return nil, a, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "app_update", Annotations: idempotent(),
		Description: "Troca repositorio, branch, arquivo de compose, a producao (quando o app e homologacao) ou o deploy automatico (vale no proximo deploy). Campo vazio mantem o atual."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in appUpdateArgs) (*mcp.CallToolResult, any, error) {
			a, err := t.app(ctx, in.App)
			if err != nil {
				return nil, nil, err
			}
			if in.Repo != "" {
				a.Repo = in.Repo
			}
			if in.Branch != "" {
				a.Branch = in.Branch
			}
			if in.Compose != "" {
				a.Compose = in.Compose
			}
			switch in.PromoteTo {
			case "":
			case "off":
				a.PromoteTo = ""
			default:
				a.PromoteTo = in.PromoteTo
			}
			if in.AutoDeploy != nil {
				a.AutoDeploy = *in.AutoDeploy
			}
			if err := t.api.UpdateApp(ctx, a); err != nil {
				return nil, nil, err
			}
			mode := "manual"
			if a.AutoDeploy {
				mode = "automatico a cada commit na branch"
			}
			out := fmt.Sprintf("%s: repositorio %s, branch %s, compose %s, deploy %s (vale no proximo deploy).", a.Name, a.Repo, cmpOr(a.Branch, "a principal"), a.ComposeFile(), mode)
			if a.PromoteTo != "" {
				out += " Homologacao de " + a.PromoteTo + "."
			}
			return text(out), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "app_promote_check", Annotations: readOnly(),
		Description: "Diz se o commit no ar na homologacao pode ir pra producao: state ready (pode), same (ja esta), older (voltaria a producao: pede confirmacao do usuario) ou blocked (reason diz o que fazer, ex: merge). unvalidated sao commits na branch da producao que nao passaram pela homologacao."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in appArgs) (*mcp.CallToolResult, apps.Promotion, error) {
			p, err := t.api.CheckPromotion(ctx, in.App)
			return nil, p, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "app_promote", Annotations: action(),
		Description: "Sobe na producao o commit validado (no ar e de pe) na homologacao, pelo deploy normal: build com a versao no ar e volta automatica se falhar. O commit tem que estar na branch da producao (merge antes). Voltar a producao pra um commit mais velho so com confirm, depois de perguntar ao usuario. Pode levar minutos."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in promoteArgs) (*mcp.CallToolResult, apps.DeployResult, error) {
			res, err := t.api.PromoteApp(ctx, in.App, in.Confirm)
			return nil, res, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "app_deploy", Annotations: action(),
		Description: "Deploy do app: baixa o commit, faz o build com a versao antiga no ar e sobe esperando os healthchecks. Se o up falhar, a versao anterior volta. Compose fora do formato do cais e recusado antes do build, com o motivo e a correcao. Pode levar minutos."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in deployArgs) (*mcp.CallToolResult, apps.DeployResult, error) {
			res, err := t.api.DeployApp(ctx, in.App, in.Ref)
			if err != nil && len(res.Problems) > 0 && !strings.Contains(err.Error(), res.Problems[0]) {
				err = fmt.Errorf("%w\n- %s", err, strings.Join(res.Problems, "\n- "))
			}
			return nil, res, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "app_rollback", Annotations: action(),
		Description: "Volta o app pra um deploy anterior que deu certo (commit vazio: o anterior ao que esta no ar; o historico esta em apps_list). Usa as imagens guardadas dos ultimos deploys, sem build, e troca sem queda. As variaveis sao as de agora. Com auto_deploy, o automatico espera um commit novo na branch."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in rollbackArgs) (*mcp.CallToolResult, apps.DeployResult, error) {
			res, err := t.api.RollbackApp(ctx, in.App, in.Commit)
			return nil, res, err
		})
}

// registerAppOps registra a operacao do dia a dia dos apps: restart, stop e variaveis.
func (t tools) registerAppOps(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "app_restart", Annotations: idempotent(),
		Description: "Reinicia o app, ou so um service do compose."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in restartArgs) (*mcp.CallToolResult, any, error) {
			if err := t.api.RestartApp(ctx, in.App, in.Service); err != nil {
				return nil, nil, err
			}
			return text(in.App + " reiniciado."), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "app_stop", Annotations: idempotent(),
		Description: "Para os containers do app sem apagar nada; app_restart volta."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in appArgs) (*mcp.CallToolResult, any, error) {
			if err := t.api.StopApp(ctx, in.App); err != nil {
				return nil, nil, err
			}
			return text(in.App + " parado. Volta com app_restart."), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "app_env_set", Annotations: idempotent(),
		Description: "Cria, troca ou remove variaveis do app, mantendo as outras. Senhas e chaves tambem: o cais run injeta no processo, nada vai pro docker inspect. Valem no proximo deploy. O retorno traz so os nomes."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in envSetArgs) (*mcp.CallToolResult, any, error) {
			if len(in.Set) == 0 && len(in.Unset) == 0 {
				return nil, nil, errors.New("informe set ou unset")
			}
			env, err := t.api.AppEnv(ctx, in.App)
			if err != nil {
				return nil, nil, err
			}
			for k, v := range in.Set {
				env[k] = v
			}
			for _, k := range in.Unset {
				delete(env, k)
			}
			if err := t.api.SetAppEnv(ctx, in.App, env); err != nil {
				return nil, nil, err
			}
			names := make([]string, 0, len(env))
			for k := range env {
				names = append(names, k)
			}
			sort.Strings(names)
			return text(fmt.Sprintf("Variaveis de %s gravadas (valem no proximo deploy): %s.", in.App, strings.Join(names, ", "))), nil, nil
		})
}

// registerData registra bancos e buckets: criar e ligar aos apps.
func (t tools) registerData(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "database_create", Annotations: action(),
		Description: "Cria um database e um usuario de mesmo nome com senha nova no Postgres ou MySQL da infra. A senha fica no cais; o app recebe ao ligar com app_link_database."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in dbCreateArgs) (*mcp.CallToolResult, core.DatabaseInfo, error) {
			info, err := t.api.CreateDatabase(ctx, core.DatabaseRequest{Engine: in.Engine, Name: in.Name, Extensions: in.Extensions})
			return nil, info, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "app_link_database", Annotations: idempotent(),
		Description: "Liga o app a um database da infra: o app recebe as variaveis <ENGINE>_<NOME>_USER, _PASS e _URL (URL pronta; o database tem o nome do usuario; host postgres:5432 ou mysql:3306). Vale no proximo deploy."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in dbLinkArgs) (*mcp.CallToolResult, any, error) {
			if err := t.api.LinkAppDatabase(ctx, in.App, in.Engine, in.Name); err != nil {
				return nil, nil, err
			}
			l := apps.DBLink{Engine: in.Engine, Name: in.Name}
			return text(fmt.Sprintf("%s ligado a %s: variaveis %s (no proximo deploy).", in.App, l.ID(), strings.Join(l.Vars(), ", "))), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "app_unlink_database", Annotations: idempotent(),
		Description: "Desliga o app do database: as variaveis dele saem no proximo deploy; o database e os dados ficam."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in dbLinkArgs) (*mcp.CallToolResult, any, error) {
			if err := t.api.UnlinkAppDatabase(ctx, in.App, in.Engine, in.Name); err != nil {
				return nil, nil, err
			}
			return text(fmt.Sprintf("%s desligado de %s/%s (o database fica).", in.App, in.Engine, in.Name)), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "bucket_create", Annotations: action(),
		Description: "Cria um bucket privado no Garage e uma chave so dele. A chave fica no cais; o app recebe ao ligar com app_link_bucket."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in bucketArgs) (*mcp.CallToolResult, core.BucketInfo, error) {
			info, err := t.api.CreateBucket(ctx, in.Name)
			return nil, info, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "app_link_bucket", Annotations: idempotent(),
		Description: "Liga o app a um bucket: o app recebe as variaveis S3_<BUCKET>_ACCESS_KEY, _SECRET_KEY e _ENDPOINT (- vira _; endpoint http://garage:3900, regiao garage, path-style, rede garage_network). Vale no proximo deploy."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in bucketLinkArgs) (*mcp.CallToolResult, any, error) {
			if err := t.api.LinkAppBucket(ctx, in.App, in.Bucket); err != nil {
				return nil, nil, err
			}
			l := apps.BucketLink{Name: in.Bucket}
			return text(fmt.Sprintf("%s ligado ao bucket %s: variaveis %s (no proximo deploy).", in.App, in.Bucket, strings.Join(l.Vars(), ", "))), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "app_unlink_bucket", Annotations: idempotent(),
		Description: "Desliga o app do bucket: as variaveis dele saem no proximo deploy; o bucket e os arquivos ficam."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in bucketLinkArgs) (*mcp.CallToolResult, any, error) {
			if err := t.api.UnlinkAppBucket(ctx, in.App, in.Bucket); err != nil {
				return nil, nil, err
			}
			return text(fmt.Sprintf("%s desligado do bucket %s (o bucket fica).", in.App, in.Bucket)), nil, nil
		})
}

// app le o app pelo nome no estado dos apps.
func (t tools) app(ctx context.Context, name string) (apps.App, error) {
	list, err := t.api.CheckApps(ctx)
	if err != nil {
		return apps.App{}, err
	}
	i := slices.IndexFunc(list.List, func(a core.AppStatus) bool { return a.Name == name })
	if i < 0 {
		return apps.App{}, fmt.Errorf("app %s nao existe (veja apps_list)", name)
	}
	return list.List[i].App, nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
