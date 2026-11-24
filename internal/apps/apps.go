// Package apps cuida dos apps: cada um e um repositorio do GitHub com o
// compose de producao pronto. No deploy o cais baixa o commit, grava o .env
// com as variaveis do painel, faz o build e sobe; o compose e do app e o
// cais nao reescreve nada dele.
package apps

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/trdSystemDev/cais/internal/docker"
	"github.com/trdSystemDev/cais/internal/fsx"
	"github.com/trdSystemDev/cais/internal/secrets"
	"github.com/trdSystemDev/cais/internal/stacks"
)

// Arquivos e pastas de cada app em Root/<app>.
const (
	MetaFile   = "app.yaml" // o que o cais sabe do app
	EnvFile    = "env"      // variaveis do painel (vira o .env do projeto)
	SecretsDir = "secrets"  // arquivos de secret (${CAIS_SECRETS}/<nome>)
	SrcDir     = "src"      // codigo do commit no ar
	PrevDir    = "src.prev" // commit anterior, pra voltar se o deploy falhar
	NextDir    = "src.next" // codigo do deploy em andamento (antes da troca)
	FailedDir  = "src.failed"
	// SwapFile marca a troca em andamento (de src.next pra src ate o
	// registro do deploy). Se o daemon para no meio, ela fica, e o Recover
	// sabe que o codigo em src pode nao ter subido.
	SwapFile = "deploy.yaml"
)

// DefaultCompose e o compose usado quando o app nao diz outro.
const DefaultCompose = "docker-compose.yml"

// ErrNotFound indica que o app nao existe.
var ErrNotFound = errors.New("app nao encontrado")

// App e o que o cais guarda de um app (app.yaml). Nada de segredo aqui:
// variaveis ficam em env (0600) e secrets em secrets/.
type App struct {
	Name string `yaml:"-" json:"name"`
	// Repo e o repositorio no GitHub (dono/nome).
	Repo string `yaml:"repo" json:"repo"`
	// Branch e de onde sai o commit do deploy; vazio e a principal do repo.
	Branch string `yaml:"branch,omitempty" json:"branch,omitempty"`
	// Compose e o arquivo de compose de producao, a partir da raiz do repo.
	Compose string `yaml:"compose,omitempty" json:"compose,omitempty"`
	// Commit e o commit no ar (ultimo deploy que deu certo).
	Commit string  `yaml:"commit,omitempty" json:"commit,omitempty"`
	Last   *Deploy `yaml:"last_deploy,omitempty" json:"last_deploy,omitempty"`
	// History sao os ultimos deploys (HistorySize), do mais novo pro mais
	// velho, os que falharam inclusive.
	History []Deploy `yaml:"history,omitempty" json:"history,omitempty"`
	// AutoHold e a ponta da branch quando houve um rollback: o deploy
	// automatico nao sobe esse commit (desfaria o rollback) e volta a
	// funcionar no proximo commit novo.
	AutoHold string `yaml:"auto_hold,omitempty" json:"auto_hold,omitempty"`
	// Databases sao os bancos da infra que o app usa (a credencial fica no
	// cais; o deploy grava <engine>_<nome>_user e <engine>_<nome>_pass).
	Databases []DBLink `yaml:"databases,omitempty" json:"databases,omitempty"`
	// Buckets sao os buckets do Garage que o app usa (o deploy grava
	// s3_<bucket>_access_key e s3_<bucket>_secret_key).
	Buckets []BucketLink `yaml:"buckets,omitempty" json:"buckets,omitempty"`
	// PromoteTo e a producao deste app quando ele e uma homologacao: o
	// commit validado aqui e promovido pra ela (Promote).
	PromoteTo string `yaml:"promote_to,omitempty" json:"promote_to,omitempty"`
	// AutoDeploy faz o deploy sozinho quando a branch ganha um commit novo
	// (o daemon consulta o GitHub; nenhuma porta abre).
	AutoDeploy bool `yaml:"auto_deploy,omitempty" json:"auto_deploy,omitempty"`
}

// ComposeFile e o compose do app, com o padrao.
func (a App) ComposeFile() string {
	if a.Compose == "" {
		return DefaultCompose
	}
	return a.Compose
}

// Deploy e o registro do ultimo deploy.
type Deploy struct {
	At     time.Time `yaml:"at" json:"at"`
	Commit string    `yaml:"commit" json:"commit"`
	// Error e o motivo da falha; vazio quando subiu.
	Error string `yaml:"error,omitempty" json:"error,omitempty"`
	// RolledBack indica que a falha voltou a versao anterior no ar.
	RolledBack bool `yaml:"rolled_back,omitempty" json:"rolled_back,omitempty"`
	// Warnings sao os avisos sobre o compose (nao impedem o deploy).
	Warnings []string `yaml:"warnings,omitempty" json:"warnings,omitempty"`
	// Problems sao o que tira o compose do formato do cais: o deploy parou
	// antes do build. Cada um diz o motivo e o jeito certo.
	Problems []string `yaml:"problems,omitempty" json:"problems,omitempty"`
	// PromotedFrom e a homologacao de onde veio o commit (vazio: deploy
	// direto).
	PromotedFrom string `yaml:"promoted_from,omitempty" json:"promoted_from,omitempty"`
	// Auto diz que o deploy foi disparado sozinho por um commit novo na
	// branch.
	Auto bool `yaml:"auto,omitempty" json:"auto,omitempty"`
	// Rollback e a volta manual pra um deploy anterior (Manager.Rollback).
	Rollback bool `yaml:"rollback,omitempty" json:"rollback,omitempty"`
	// Images diz que subiu com as imagens guardadas desse commit, sem build.
	Images bool `yaml:"images,omitempty" json:"images,omitempty"`
	// Interrupted diz que o daemon parou no meio da troca (queda, kill,
	// reboot) e o Recover terminou o deploy voltando a versao anterior.
	Interrupted bool `yaml:"interrupted,omitempty" json:"interrupted,omitempty"`
	// Transient diz que a falha parece passageira (rede, registry fora do
	// ar, limite do Docker Hub) e nao culpa do commit: o deploy automatico
	// tenta de novo (AutoRetryAfter).
	Transient bool `yaml:"transient,omitempty" json:"transient,omitempty"`
}

// OK diz se o deploy subiu.
func (d Deploy) OK() bool { return d.Error == "" }

// AutoRetryAfter sao as esperas antes de cada nova tentativa automatica de
// um commit cujo deploy falhou por motivo passageiro (Deploy.Transient).
// Esgotadas, o commit espera o proximo (ou um deploy manual).
var AutoRetryAfter = []time.Duration{2 * time.Minute, 10 * time.Minute, 30 * time.Minute}

// AutoRetryAt diz se, e a partir de quando, o deploy automatico tenta de
// novo o commit do ultimo deploy, que falhou sem culpa do commit:
// interrompido (o daemon parou no meio) tenta mais uma vez; falha
// passageira, uma vez a cada espera de AutoRetryAfter.
func (a App) AutoRetryAt() (time.Time, bool) {
	l := a.Last
	if l == nil || l.OK() {
		return time.Time{}, false
	}
	// tentativas seguidas que falharam com esse commit (o historico comeca
	// pelo ultimo deploy)
	n := 0
	for _, d := range a.History {
		if d.Commit != l.Commit || d.OK() {
			break
		}
		n++
	}
	switch {
	case l.Interrupted && n == 1:
		return l.At, true
	case l.Transient && !l.Interrupted && n >= 1 && n <= len(AutoRetryAfter):
		return l.At.Add(AutoRetryAfter[n-1]), true
	}
	return time.Time{}, false
}

// Tried diz se o deploy automatico deve deixar o commit de lado agora: o
// ultimo deploy ja foi dele e nao e hora de tentar de novo. Um commit que
// falhou por culpa dele nao e tentado de novo (seria um deploy por minuto).
func (a App) Tried(commit string, now time.Time) bool {
	if a.Last == nil || a.Last.Commit != commit {
		return false
	}
	at, ok := a.AutoRetryAt()
	return !ok || now.Before(at)
}

// transientMarks sao trechos de erro de rede e de registry: o build ou o
// up que falha com eles provavelmente passa na proxima tentativa.
var transientMarks = []string{
	"toomanyrequests", "too many requests", "rate limit",
	"i/o timeout", "tls handshake timeout", "context deadline exceeded", "timeout exceeded",
	"connection reset", "connection refused", "broken pipe", "unexpected eof",
	"no such host", "temporary failure in name resolution", "network is unreachable", "no route to host",
	"502 bad gateway", "503 service unavailable", "504 gateway",
	"etimedout", "econnreset", "econnrefused", "eai_again", "enotfound",
	"failed to do request", "failed to resolve source metadata",
}

// TimeoutError e o deploy cancelado por passar do Manager.Timeout.
type TimeoutError struct {
	Limit time.Duration
	Err   error // o que o comando cancelado devolveu
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("o deploy passou do limite de %s e foi cancelado (download ou build travado? o limite e o --deploy-timeout do daemon): %v", e.Limit, e.Err)
}

func (e *TimeoutError) Unwrap() error { return e.Err }

// transient diz se o erro do deploy parece passageiro. Compose fora do
// formato nunca e: o commit e que tem que mudar. Passar do limite e (rede
// ou registry travado); um build que sempre passa desiste depois das
// tentativas do AutoRetryAfter.
func transient(err error) bool {
	var ce *ComposeError
	var te *TimeoutError
	switch {
	case err == nil || errors.As(err, &ce):
		return false
	case errors.As(err, &te):
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, m := range transientMarks {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}

// HistorySize e quantos deploys o app guarda no historico.
const HistorySize = 20

// KeepImages e de quantos deploys (os ultimos que deram certo, um por
// commit) as imagens ficam guardadas: voltar pra eles nao refaz o build.
const KeepImages = 5

// Source e de onde vem o codigo (o GitHub, pelo GitHub App).
type Source interface {
	// Resolve devolve o commit de ref (branch ou sha); ref vazio e a
	// branch principal do repositorio.
	Resolve(ctx context.Context, repo, ref string) (string, error)
	// Download extrai o codigo do commit em dir (que nao existe ainda).
	Download(ctx context.Context, repo, commit, dir string) error
	// Compare diz onde head esta em relacao a base (branch ou commit).
	Compare(ctx context.Context, repo, base, head string) (Comparison, error)
	// Tree devolve a arvore de arquivos do commit: dois commits com a
	// mesma arvore tem o mesmo codigo (ex: squash merge).
	Tree(ctx context.Context, repo, commit string) (string, error)
}

// Comparison e a relacao entre dois commits, como o compare do GitHub:
// Status "ahead" (head tem commits que base nao tem), "behind" (head ja esta
// no historico de base), "identical" ou "diverged".
type Comparison struct {
	Status   string `json:"status"`
	AheadBy  int    `json:"ahead_by"`
	BehindBy int    `json:"behind_by"`
}

// InHistory diz se head ja faz parte do historico de base.
func (c Comparison) InHistory() bool { return c.Status == "behind" || c.Status == "identical" }

// Docker sao as operacoes de projeto compose que os apps usam.
type Docker interface {
	Config(ctx context.Context, p docker.Project) ([]byte, error)
	Build(ctx context.Context, p docker.Project) error
	Up(ctx context.Context, p docker.Project) error
	PS(ctx context.Context, p docker.Project) ([]docker.Container, error)
	Restart(ctx context.Context, p docker.Project, service string) error
	Stop(ctx context.Context, p docker.Project) error
	// Rollout troca os containers do service sem queda: os novos sobem ao
	// lado dos antigos e os antigos so saem com os novos healthy; com drain,
	// os antigos ficam unhealthy (saem do Traefik) antes de parar.
	Rollout(ctx context.Context, p docker.Project, service, image string, drain bool) error
	// PruneImages remove as imagens sem uso do projeto (versoes que o
	// build trocou).
	PruneImages(ctx context.Context, project string) error
	// Imagens guardadas por commit (rollback sem build).
	TagImage(ctx context.Context, src, dst string) error
	HasImage(ctx context.Context, ref string) bool
	ImageTags(ctx context.Context, project string) ([]string, error)
	RemoveImages(ctx context.Context, refs []string) error
	// Down remove o projeto pelo nome; volumes apaga tambem os dados.
	Down(ctx context.Context, name string, volumes bool) error
}

// Manager cuida dos apps em Root (/var/lib/cais/apps). Nao tem trava:
// quem chama (o core) garante uma acao por app de cada vez.
type Manager struct {
	Root   string
	Source Source
	Docker Docker
	Now    func() time.Time
	// Networks sao as redes da infra que os apps podem usar agora (as das
	// stacks ligadas); a proxy_network vale sempre.
	Networks []string
	// DBCredentials da usuario e senha dos bancos da infra ligados ao app.
	DBCredentials DBCredentials
	// BucketCredentials da a chave dos buckets do Garage ligados ao app.
	BucketCredentials BucketCredentials
	// Settle e quanto tempo o deploy observa os containers depois do up:
	// sem healthcheck, "running" na hora do up nao garante que o app fica
	// de pe (migracao que falha, crash na subida). Zero nao observa.
	Settle time.Duration
	// Timeout e o limite de um deploy (download, build, troca e
	// observacao): download ou build travado prenderia o app pra sempre
	// (nenhum deploy, nem o automatico, passa da trava). Zero nao limita.
	// A volta da versao anterior roda depois do limite, com RollbackTimeout.
	Timeout time.Duration
	// Builds e a fila global de build (nil: sem fila). O tempo esperando
	// nela nao conta no Timeout.
	Builds *BuildQueue
}

// RollbackTimeout e o limite da volta da versao anterior quando um deploy
// falha (inclusive por passar do Timeout): normalmente sobe as imagens
// guardadas, sem build.
var RollbackTimeout = 10 * time.Minute

var (
	appName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	repoRe  = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})/[A-Za-z0-9._-]{1,100}$`)
	refRe   = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)
	fileRe  = regexp.MustCompile(`^[A-Za-z0-9._/-]+\.ya?ml$`)
)

// CheckName recusa nomes invalidos e os das stacks: o nome do app vira o
// projeto compose, e "-p proxy" assumiria a stack do proxy.
func CheckName(name string) error {
	if !appName.MatchString(name) {
		return fmt.Errorf("nome de app invalido %q (minusculas, numeros e -, ate 40)", name)
	}
	if _, ok := stacks.Get(name); ok || name == "cais" {
		return fmt.Errorf("%q e nome reservado do cais", name)
	}
	return nil
}

// Check confere o app antes de gravar.
func Check(a App) error {
	var errs []error
	if err := CheckName(a.Name); err != nil {
		errs = append(errs, err)
	}
	if !repoRe.MatchString(a.Repo) {
		errs = append(errs, fmt.Errorf("repositorio invalido %q (dono/nome, ex: trdSystemDev/meu-app)", a.Repo))
	}
	if a.Branch != "" && (!refRe.MatchString(a.Branch) || path.Clean(a.Branch) != a.Branch) {
		errs = append(errs, fmt.Errorf("branch invalida %q", a.Branch))
	}
	if a.PromoteTo != "" {
		if err := CheckName(a.PromoteTo); err != nil {
			errs = append(errs, fmt.Errorf("producao: %w", err))
		} else if a.PromoteTo == a.Name {
			errs = append(errs, errors.New("o app nao pode ser a homologacao dele mesmo"))
		}
	}
	if c := a.Compose; c != "" {
		if !fileRe.MatchString(c) || path.Clean(c) != c || c[0] == '/' || c == ".." || len(c) > 2 && c[:3] == "../" {
			errs = append(errs, fmt.Errorf("arquivo de compose invalido %q (caminho .yml a partir da raiz do repositorio)", c))
		}
	}
	return errors.Join(errs...)
}

func (m Manager) dir(name string) string { return filepath.Join(m.Root, name) }

// SecretsPath e a pasta dos secrets do app (o ${CAIS_SECRETS} do compose).
func (m Manager) SecretsPath(name string) string { return filepath.Join(m.dir(name), SecretsDir) }

func (m Manager) secrets(name string) secrets.FileStore {
	return secrets.FileStore{Dir: m.SecretsPath(name)}
}

// Project e o projeto compose do app no ar (ou numa pasta de codigo).
func (m Manager) project(a App, src string) docker.Project {
	return docker.Project{Name: a.Name, Files: []string{filepath.Join(m.dir(a.Name), src, a.ComposeFile())}}
}

func (m Manager) deployed(a App) bool {
	_, err := os.Stat(filepath.Join(m.dir(a.Name), SrcDir, a.ComposeFile()))
	return err == nil
}

// Create cria o app (pasta, secrets/ e app.yaml). O codigo so vem no deploy.
func (m Manager) Create(a App) (App, error) {
	if err := Check(a); err != nil {
		return App{}, err
	}
	if _, err := os.Stat(m.dir(a.Name)); err == nil {
		return App{}, fmt.Errorf("app %q ja existe", a.Name)
	}
	if err := m.checkLink(a); err != nil {
		return App{}, err
	}
	if err := os.MkdirAll(m.SecretsPath(a.Name), 0o700); err != nil {
		return App{}, err
	}
	a.Commit, a.Last, a.Databases, a.Buckets = "", nil, nil, nil
	return a, m.save(a)
}

// Update troca repositorio, branch, compose, a producao (homologacao) e o
// deploy automatico do app (o codigo novo vale no proximo deploy).
func (m Manager) Update(a App) error {
	cur, err := m.Get(a.Name)
	if err != nil {
		return err
	}
	if err := Check(a); err != nil {
		return err
	}
	if err := m.checkLink(a); err != nil {
		return err
	}
	cur.Repo, cur.Branch, cur.Compose, cur.PromoteTo, cur.AutoDeploy = a.Repo, a.Branch, a.Compose, a.PromoteTo, a.AutoDeploy
	return m.save(cur)
}

// checkLink confere o vinculo de homologacao: a producao existe, usa o
// mesmo repositorio e nao e homologacao de outro (sem corrente); e um app
// que ja tem homologacao nao vira homologacao.
func (m Manager) checkLink(a App) error {
	all, err := m.List()
	if err != nil {
		return err
	}
	for _, o := range all {
		if o.Name != a.Name && o.PromoteTo == a.Name && a.PromoteTo != "" {
			return fmt.Errorf("%s ja e a producao de %s: nao pode ser homologacao de outro app", a.Name, o.Name)
		}
	}
	if a.PromoteTo == "" {
		return nil
	}
	prod, err := m.Get(a.PromoteTo)
	if err != nil {
		return fmt.Errorf("producao %s: %w", a.PromoteTo, err)
	}
	switch {
	case prod.PromoteTo != "":
		return fmt.Errorf("%s ja e homologacao de %s: promova direto pra %s", prod.Name, prod.PromoteTo, prod.PromoteTo)
	case !strings.EqualFold(prod.Repo, a.Repo):
		return fmt.Errorf("a homologacao usa o mesmo repositorio da producao (%s usa %s)", prod.Name, prod.Repo)
	}
	return nil
}

// Get le o app.yaml.
func (m Manager) Get(name string) (App, error) {
	if err := CheckName(name); err != nil {
		return App{}, err
	}
	data, err := os.ReadFile(filepath.Join(m.dir(name), MetaFile))
	if errors.Is(err, os.ErrNotExist) {
		return App{}, fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	if err != nil {
		return App{}, err
	}
	var a App
	if err := yaml.Unmarshal(data, &a); err != nil {
		return App{}, fmt.Errorf("%s/%s: %w", name, MetaFile, err)
	}
	a.Name = name
	return a, nil
}

// List devolve todos os apps em ordem alfabetica.
func (m Manager) List() ([]App, error) {
	entries, err := os.ReadDir(m.Root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []App
	for _, e := range entries {
		if !e.IsDir() || CheckName(e.Name()) != nil {
			continue
		}
		a, err := m.Get(e.Name())
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func (m Manager) save(a App) error {
	data, err := yaml.Marshal(a)
	if err != nil {
		return err
	}
	data = append([]byte("# gerado pelo cais; mude pelo cais\n"), data...)
	return fsx.WriteAtomic(filepath.Join(m.dir(a.Name), MetaFile), data, 0o644)
}
