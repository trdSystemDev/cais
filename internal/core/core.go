package core

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/backup"
	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/docker"
	"github.com/trdSystemDev/cais/internal/github"
	"github.com/trdSystemDev/cais/internal/inflight"
	"github.com/trdSystemDev/cais/internal/notify"
	"github.com/trdSystemDev/cais/internal/sysstat"
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
	ComposeUp   func(ctx context.Context, file string, recreate bool) error
	ComposeDown func(ctx context.Context, file string) error
	// Parar e Reiniciar um servico ligado (Iniciar e o ComposeUp).
	ComposeStop    func(ctx context.Context, file string) error
	ComposeRestart func(ctx context.Context, file string) error
	ContainerLogs  func(context.Context, string) (string, error)
	ComposePS      func(ctx context.Context, file string) ([]docker.Container, error)
	// Backup: pastas do estado (o que entra no backup e onde ficam os
	// temporarios), o OAuth do Google e o Drive, o login da Microsoft e o
	// Graph do OneDrive (trocados nos testes) e o exec com stdin/stdout
	// ligados (dump e restore).
	DataDir           string
	BackupsDir        string
	GoogleEndpoint    oauth2.Endpoint
	DriveAPI          string
	DriveUpload       string
	MicrosoftEndpoint oauth2.Endpoint
	GraphAPI          string
	ExecStream        func(ctx context.Context, container string, stdin io.Reader, stdout io.Writer, args ...string) error
	// PruneBuildCache e a limpeza diaria do cache de build.
	PruneBuildCache func(ctx context.Context) error

	// BrevoURL troca a API de e-mail da Brevo (testes); vazio e a Brevo.
	BrevoURL string
	// Hostname e o nome da maquina, que identifica a VPS (backups,
	// avisos). Padrao: os.Hostname.
	Hostname func() (string, error)

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
	// DeployTimeout e o limite de um deploy (padrao 30min; negativo nao
	// limita): build ou download travado prenderia o app pra sempre.
	DeployTimeout time.Duration
	Exec          func(ctx context.Context, container string, args ...string) (string, error)
	// ExecInput e o Exec com stdin: SQL com senha nunca vai na linha de comando.
	ExecInput func(ctx context.Context, container string, input []byte, args ...string) (string, error)
	// ReadSystem le o uso da maquina (padrao: /proc e os discos da raiz,
	// do cais e do docker).
	ReadSystem func() (sysstat.Sample, error)
	// DockerStats le o uso de cada container rodando (padrao: docker stats).
	DockerStats func(ctx context.Context) ([]docker.ContainerStats, error)
	// Version e a versao do cais rodando (comparada com o release mais
	// novo); LatestRelease pergunta a tag dele (padrao: o GitHub).
	Version       string
	LatestRelease func(ctx context.Context) (string, error)
}

// DefaultDeployTimeout e o limite padrao de um deploy. Junto com a volta da
// versao anterior (apps.RollbackTimeout) cabe no TimeoutStopSec da unidade
// do systemd, que espera os deploys em andamento.
const DefaultDeployTimeout = 30 * time.Minute

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
	// gh guarda as sessoes do GitHub App por repositorio e o limite da API
	gh github.Sessions

	backup backupRuntime

	sys        systemRuntime
	containers containersRuntime
	health     healthRuntime

	// work e o trabalho que o daemon dispara sozinho (deploy automatico,
	// backup, recuperacao): o Close espera ele terminar
	work inflight.Group
}

// vpsName e o nome que identifica a VPS nos backups e nos avisos: o
// hostname da maquina.
func (c *Core) vpsName() string {
	if h, err := c.deps.Hostname(); err == nil && h != "" {
		return strings.ToLower(h)
	}
	return "vps"
}

// Close desliga o trabalho de fundo: nada novo comeca, e espera o deploy
// automatico, o backup e a recuperacao em andamento terminarem. Cortar um
// deploy no meio deixaria o app metade numa versao, metade na outra.
func (c *Core) Close() {
	if n := c.work.Count(); n > 0 {
		log.Printf("desligando: esperando %d deploys/backups em andamento terminarem", n)
	}
	c.work.Close()
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
	if d.ComposeStop == nil {
		d.ComposeStop = docker.ComposeStop
	}
	if d.ComposeRestart == nil {
		d.ComposeRestart = docker.ComposeRestart
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
	if d.MicrosoftEndpoint.TokenURL == "" {
		d.MicrosoftEndpoint = backup.MicrosoftEndpoint
	}
	if d.ExecStream == nil {
		d.ExecStream = docker.ExecStream
	}
	if d.PruneBuildCache == nil {
		d.PruneBuildCache = docker.PruneBuildCache
	}
	if d.Hostname == nil {
		d.Hostname = os.Hostname
	}
	if d.BrevoURL == "" {
		d.BrevoURL = notify.BrevoAPI
	}
	if d.AppsDir == "" {
		d.AppsDir = filepath.Join(config.DataDir(), "apps")
	}
	if d.AppSettle == 0 {
		d.AppSettle = 15 * time.Second
	}
	if d.DeployTimeout == 0 {
		d.DeployTimeout = DefaultDeployTimeout
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
	if d.DockerStats == nil {
		d.DockerStats = docker.Stats
	}
	if d.LatestRelease == nil {
		d.LatestRelease = latestRelease
	}
	if d.ReadSystem == nil {
		dataDir := d.DataDir
		d.ReadSystem = func() (sysstat.Sample, error) {
			return sysstat.Read("/proc", "/", dataDir, "/var/lib/docker")
		}
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
