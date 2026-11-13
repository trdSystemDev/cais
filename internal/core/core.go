package core

import (
	"context"
	"io"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/backup"
	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/docker"
)

// Deps sao as dependencias externas do Core. Campos vazios recebem a
// implementacao real; os testes trocam por fakes.
type Deps struct {
	CheckDocker func(context.Context) docker.Status
	Now         func() time.Time

	// Caminhos no disco (padrao: internal/config/paths.go).
	ConfigPath string
	SecretsDir string
	StackDir   func(name string) string

	// Comandos do docker usados pelo Setup.
	ComposeUp     func(ctx context.Context, file string, recreate bool) error
	ComposeDown   func(ctx context.Context, file string) error
	ContainerLogs func(context.Context, string) (string, error)
	ComposePS     func(ctx context.Context, file string) ([]docker.Container, error)
	// Backup: pastas do estado (o que entra no backup e onde ficam os
	// temporarios), o OAuth do Google e o Drive (trocados nos testes) e o
	// exec com stdin/stdout ligados (dump e restore).
	DataDir        string
	BackupsDir     string
	GoogleEndpoint oauth2.Endpoint
	DriveAPI       string
	DriveUpload    string
	ExecStream     func(ctx context.Context, container string, stdin io.Reader, stdout io.Writer, args ...string) error
	// PruneBuildCache e a limpeza diaria do cache de build.
	PruneBuildCache func(ctx context.Context) error

	// GitHubURL troca a API do GitHub (testes); vazio e o GitHub.
	GitHubURL string

	// Apps: pasta dos apps, de onde vem o codigo (padrao: o GitHub App) e
	// as operacoes de projeto compose deles.
	AppsDir   string
	AppSource apps.Source
	AppDocker apps.Docker
	// AppSettle e quanto o deploy observa os containers depois do up
	// (padrao 15s; negativo nao observa).
	AppSettle time.Duration
	Exec      func(ctx context.Context, container string, args ...string) (string, error)
	// ExecInput e o Exec com stdin: SQL com senha nunca vai na linha de comando.
	ExecInput func(ctx context.Context, container string, input []byte, args ...string) (string, error)
}

// Core guarda o estado e executa as acoes. E seguro pra uso concorrente:
// a TUI e o MCP podem chamar acoes ao mesmo tempo.
type Core struct {
	deps Deps

	// infra serializa as acoes que mexem na VPS (Setup, bancos): uma
	// segunda chamada falha na hora em vez de esperar.
	infra sync.Mutex

	mu          sync.Mutex
	state       State
	subs        map[int]chan State
	nextSub     int
	checks      int // checagens de ambiente em andamento
	infraChecks int // checagens das stacks em andamento
	appChecks   int // checagens dos apps em andamento

	// appLocks tem uma trava por app: dois deploys do mesmo app nunca rodam
	// juntos, mas apps diferentes sim.
	appLocks sync.Map // nome -> *sync.Mutex
	// appsCheck serializa o CheckApps: duas checagens juntas (fim de dois
	// deploys) publicariam fora de ordem e a mais velha apagaria a nova
	appsCheck sync.Mutex
	// autoErrs e o ultimo erro da consulta do deploy automatico por app
	// (pra registrar no log so quando muda); protegido por mu
	autoErrs map[string]string

	backup backupRuntime
}

// New cria o Core. Nenhuma acao roda aqui: quem decide quando checar o
// ambiente e a interface (ou o cliente MCP).
func New(d Deps) *Core {
	if d.CheckDocker == nil {
		d.CheckDocker = docker.Check
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.ConfigPath == "" {
		d.ConfigPath = config.ConfigFile()
	}
	if d.SecretsDir == "" {
		d.SecretsDir = config.SecretsDir()
	}
	if d.StackDir == nil {
		d.StackDir = config.StackDir
	}
	if d.ComposeUp == nil {
		d.ComposeUp = docker.ComposeUp
	}
	if d.ComposeDown == nil {
		d.ComposeDown = docker.ComposeDown
	}
	if d.ContainerLogs == nil {
		d.ContainerLogs = docker.ContainerLogs
	}
	if d.ComposePS == nil {
		d.ComposePS = docker.ComposePS
	}
	if d.DataDir == "" {
		d.DataDir = config.DataDir()
	}
	if d.BackupsDir == "" {
		d.BackupsDir = config.BackupsDir()
	}
	if d.GoogleEndpoint.TokenURL == "" {
		d.GoogleEndpoint = backup.GoogleEndpoint
	}
	if d.ExecStream == nil {
		d.ExecStream = docker.ExecStream
	}
	if d.PruneBuildCache == nil {
		d.PruneBuildCache = docker.PruneBuildCache
	}
	if d.AppsDir == "" {
		d.AppsDir = filepath.Join(config.DataDir(), "apps")
	}
	if d.AppSettle == 0 {
		d.AppSettle = 15 * time.Second
	}
	if d.AppDocker == nil {
		d.AppDocker = docker.CLI{}
	}
	if d.Exec == nil {
		d.Exec = docker.Exec
	}
	if d.ExecInput == nil {
		d.ExecInput = docker.ExecInput
	}
	return &Core{deps: d, subs: map[int]chan State{}}
}

// Snapshot devolve a foto atual do estado. Localmente nunca falha; o erro
// existe pelo contrato da API (via socket pode falhar).
func (c *Core) Snapshot(context.Context) (State, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state, nil
}

// Subscribe devolve um canal que recebe o estado atual na hora e depois
// cada mudanca. Se o leitor atrasar, fotos intermediarias sao descartadas
// e ele recebe so a mais recente -- interface reativa so precisa do ultimo
// estado, e um leitor lento nunca trava o Core. A funcao devolvida cancela
// a inscricao e fecha o canal; pode ser chamada mais de uma vez.
func (c *Core) Subscribe(context.Context) (<-chan State, func(), error) {
	ch := make(chan State, 1)

	c.mu.Lock()
	id := c.nextSub
	c.nextSub++
	c.subs[id] = ch
	ch <- c.state
	c.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			c.mu.Lock()
			delete(c.subs, id)
			close(ch)
			c.mu.Unlock()
		})
	}, nil
}

// update aplica fn no estado, publica a nova foto pra todos os inscritos e
// a devolve. fn roda com o lock, entao tambem pode mexer em campos internos
// do Core (ex: contadores) de forma atomica com o estado.
func (c *Core) update(fn func(*State)) State {
	c.mu.Lock()
	defer c.mu.Unlock()

	fn(&c.state)
	c.state.Version++
	s := c.state

	for _, ch := range c.subs {
		// so o Core envia nesses canais, e sempre com o lock: depois de
		// esvaziar, o envio seguinte nunca bloqueia
		select {
		case <-ch:
		default:
		}
		ch <- s
	}
	return s
}
