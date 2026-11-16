// Package apps cuida dos apps: cada um e um repositorio do GitHub com o
// compose de producao pronto. No deploy o cais baixa o commit, grava o .env
// com as variaveis do painel, faz o build e sobe; o compose e do app e o
// cais nao reescreve nada dele.
package apps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
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

// DeployResult e o que um deploy fez.
type DeployResult struct {
	App      string   `json:"app"`
	Commit   string   `json:"commit"`
	Warnings []string `json:"warnings,omitempty"`
	// Problems e por que o compose foi recusado (vazio se passou).
	Problems []string `json:"problems,omitempty"`
	// Images diz que subiu com as imagens guardadas do commit, sem build.
	Images bool `json:"images,omitempty"`
}

// Deploy baixa o commit de ref (vazio = o mais novo da branch do app),
// grava o .env, faz o build com a versao antiga ainda no ar, troca e sobe
// esperando os healthchecks. Se o up falhar e havia versao no ar, ela
// volta. Os avisos sobre o compose nao impedem o deploy.
func (m Manager) Deploy(ctx context.Context, name, ref string) (DeployResult, error) {
	return m.deploy(ctx, name, ref, Deploy{})
}

// AutoDeploy e o Deploy disparado por um commit novo na branch: fica
// registrado como automatico.
func (m Manager) AutoDeploy(ctx context.Context, name, commit string) (DeployResult, error) {
	return m.deploy(ctx, name, commit, Deploy{Auto: true})
}

// deploy faz o Deploy; tag tem o que o registro do deploy leva alem do
// resultado (origem da promocao, automatico).
func (m Manager) deploy(ctx context.Context, name, ref string, tag Deploy) (DeployResult, error) {
	// back e o ctx do que roda mesmo depois do limite do deploy: a volta da
	// versao anterior e a limpeza das imagens
	back := context.WithoutCancel(ctx)
	if m.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, m.Timeout)
		defer cancel()
	}
	// expired diz o motivo de um erro que veio do limite (o docker morto
	// pelo ctx so diz "signal: killed")
	expired := func(err error) error {
		if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return &TimeoutError{Limit: m.Timeout, Err: err}
		}
		return err
	}
	a, err := m.Get(name)
	if err != nil {
		return DeployResult{}, err
	}
	if ref == "" {
		ref = a.Branch
	}
	if ref != "" && !refRe.MatchString(ref) {
		return DeployResult{}, fmt.Errorf("commit ou branch invalido %q", ref)
	}
	// um deploy anterior que o daemon nao terminou deixa as pastas no meio
	// da troca: a versao boa volta pro src antes de tudo (a deste deploy
	// sobe por cima)
	if _, err := m.recover(ctx, a, false); err != nil {
		return DeployResult{}, err
	}
	if a, err = m.Get(name); err != nil {
		return DeployResult{}, err
	}
	commit, err := m.Source.Resolve(ctx, a.Repo, ref)
	if err != nil {
		return DeployResult{}, expired(err)
	}
	res := DeployResult{App: name, Commit: commit}

	// o codigo novo vai pra uma pasta propria: o app no ar nao e tocado ate
	// o build terminar
	dir := m.dir(name)
	next := NextDir
	os.RemoveAll(filepath.Join(dir, next))
	if err := m.Source.Download(ctx, a.Repo, commit, filepath.Join(dir, next)); err != nil {
		os.RemoveAll(filepath.Join(dir, next))
		return res, expired(err)
	}
	fail := func(err error) (DeployResult, error) {
		err = expired(err)
		os.RemoveAll(filepath.Join(dir, next))
		m.record(a, Deploy{At: m.Now(), Commit: commit, Error: err.Error(), Warnings: res.Warnings, Problems: res.Problems,
			PromotedFrom: tag.PromotedFrom, Auto: tag.Auto, Rollback: tag.Rollback, Transient: transient(err)})
		return res, err
	}
	if _, err := os.Stat(filepath.Join(dir, next, a.ComposeFile())); err != nil {
		return fail(fmt.Errorf("o commit %s nao tem o compose %s", short(commit), a.ComposeFile()))
	}
	if err := m.writeDotEnv(a, next, commit); err != nil {
		return fail(err)
	}
	if err := m.infraSecrets(a); err != nil {
		return fail(err)
	}
	p := m.project(a, next)
	cfg, err := m.Docker.Config(ctx, p)
	if err != nil {
		return fail(err)
	}
	res.Problems, res.Warnings = CheckCompose(cfg, Rules{App: name, Src: filepath.Join(dir, next),
		SecretsDir: m.SecretsPath(name), DotEnv: m.dotEnvPath(a, next), Networks: m.Networks, Apps: m.otherApps(name)})
	if len(res.Problems) > 0 {
		return fail(&ComposeError{App: name, Problems: res.Problems})
	}
	// voltar pra um deploy anterior usa as imagens guardadas dele (sem
	// build); sem elas, ou num deploy normal, o build faz
	built := builtImages(cfg, p.Name)
	res.Images = tag.Rollback && m.restoreImages(ctx, built, commit)
	if !res.Images {
		if err := m.Docker.Build(ctx, p); err != nil {
			return fail(err)
		}
	}

	// troca: src vira src.prev e o novo vira src. Da marca ate o registro
	// do deploy, uma parada do daemon deixa o app entre as duas versoes; a
	// marca e o que o Recover usa pra voltar a boa.
	last := Deploy{Commit: commit, Warnings: res.Warnings, PromotedFrom: tag.PromotedFrom, Auto: tag.Auto,
		Rollback: tag.Rollback, Images: res.Images}
	if err := m.markSwap(a, last); err != nil {
		return fail(err)
	}
	defer os.Remove(m.swapPath(name)) // depois do registro
	hadPrev := m.deployed(a)
	os.RemoveAll(filepath.Join(dir, PrevDir))
	if hadPrev {
		if err := os.Rename(filepath.Join(dir, SrcDir), filepath.Join(dir, PrevDir)); err != nil {
			return fail(err)
		}
	}
	if err := os.Rename(filepath.Join(dir, next), filepath.Join(dir, SrcDir)); err != nil {
		if hadPrev {
			os.Rename(filepath.Join(dir, PrevDir), filepath.Join(dir, SrcDir)) // o no ar volta pro lugar
		}
		return fail(err)
	}

	// services atras do Traefik com healthcheck trocam sem queda; o up
	// depois sobe o resto (e nao recria os que ja trocaram)
	p = m.project(a, SrcDir)
	var upErr error
	for _, s := range rollingServices(cfg, p.Name) {
		if upErr = m.Docker.Rollout(ctx, p, s.name, s.image, s.drain); upErr != nil {
			break
		}
	}
	if upErr == nil {
		upErr = m.Docker.Up(ctx, p)
	}
	if upErr == nil {
		upErr = m.settle(ctx, a)
	}
	last.At = m.Now()
	if upErr != nil {
		upErr = expired(upErr)
		last.Error, last.Transient = upErr.Error(), transient(upErr)
		if hadPrev {
			// a volta roda mesmo com o limite do deploy estourado
			rctx, cancel := context.WithTimeout(back, RollbackTimeout)
			rerr := m.rollback(rctx, a)
			cancel()
			if rerr != nil {
				upErr = fmt.Errorf("%w\n\ne a volta pra versao anterior tambem falhou: %v", upErr, rerr)
			} else {
				last.RolledBack = true
				upErr = fmt.Errorf("%w\n\na versao anterior (%s) foi restaurada e continua no ar", upErr, short(a.Commit))
			}
		}
		m.record(a, last)
		return res, upErr
	}
	a.Commit = commit
	m.record(a, last)
	// as imagens deste commit ficam guardadas (rollback sem build) e as de
	// deploys velhos saem; o que sobrar sem tag e lixo. Falhar aqui nao
	// desfaz um deploy que deu certo.
	m.keepImages(back, name, built, commit)
	m.Docker.PruneImages(back, p.Name)
	return res, nil
}

type rollingService struct {
	name, image string
	drain       bool // healthcheck segue a convencao do docker.DrainFile
}

// rollingServices sao os services que o deploy troca sem queda: atras do
// Traefik (traefik.enable) e com healthcheck no compose, que e o que faz o
// Traefik so mandar trafego pro container novo quando ele esta pronto. Os
// outros (banco, worker, sem healthcheck) sao recriados pelo up normal.
func rollingServices(configJSON []byte, project string) []rollingService {
	var c composeConfig
	if json.Unmarshal(configJSON, &c) != nil {
		return nil
	}
	var out []rollingService
	for _, n := range sortedKeys(c.Services) {
		s := c.Services[n]
		if s.Labels["traefik.enable"] != "true" || !s.Healthcheck.active() {
			continue
		}
		image := s.Image
		if image == "" {
			image = project + "-" + n // nome que o compose da a imagem do build
		}
		out = append(out, rollingService{n, image, s.Healthcheck.drains()})
	}
	return out
}

// otherApps sao os nomes dos outros apps (pra regra dos nomes do Traefik).
func (m Manager) otherApps(name string) []string {
	all, _ := m.List()
	var out []string
	for _, a := range all {
		if a.Name != name {
			out = append(out, a.Name)
		}
	}
	return out
}

// settle observa os containers por m.Settle: reiniciando, parado com erro ou
// unhealthy e falha do deploy (e a versao anterior volta). Parado com codigo
// 0 e servico de uma vez so (init, migracao separada) e esta certo.
func (m Manager) settle(ctx context.Context, a App) error {
	if m.Settle <= 0 {
		return nil
	}
	tick := min(2*time.Second, m.Settle)
	deadline := time.Now().Add(m.Settle)
	for {
		cs, err := m.Docker.PS(ctx, m.project(a, SrcDir))
		if err != nil {
			return err
		}
		for _, c := range cs {
			switch {
			case c.State == "restarting":
				return fmt.Errorf("o container %s reinicia sem parar (%s); veja o motivo em cais app logs %s %s", c.Name, c.Status, a.Name, c.Name)
			case (c.State == "exited" || c.State == "dead") && c.ExitCode != 0:
				return fmt.Errorf("o container %s parou com erro (codigo %d); veja o motivo em cais app logs %s %s", c.Name, c.ExitCode, a.Name, c.Name)
			case c.Health == "unhealthy":
				return fmt.Errorf("o container %s ficou unhealthy; veja cais app logs %s %s", c.Name, a.Name, c.Name)
			}
		}
		if !time.Now().Before(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(tick):
		}
	}
}

// rollback volta src.prev pro lugar e sobe de novo.
func (m Manager) rollback(ctx context.Context, a App) error {
	dir := m.dir(a.Name)
	failed := filepath.Join(dir, FailedDir)
	os.RemoveAll(failed)
	if err := os.Rename(filepath.Join(dir, SrcDir), failed); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(dir, PrevDir), filepath.Join(dir, SrcDir)); err != nil {
		return err
	}
	os.RemoveAll(failed)
	return m.upCurrent(ctx, a)
}

// upCurrent sobe o codigo de src, que e o do commit a.Commit. O build da
// versao e refeito so sem as imagens guardadas dela (as imagens do compose
// sao trocadas a cada build), e com o cache das camadas.
func (m Manager) upCurrent(ctx context.Context, a App) error {
	p := m.project(a, SrcDir)
	// as imagens guardadas da versao no ar voltam sem build; sem elas, o
	// build refaz (com o cache das camadas)
	cfg, err := m.Docker.Config(ctx, p)
	if err != nil || !m.restoreImages(ctx, builtImages(cfg, p.Name), a.Commit) {
		if err := m.Docker.Build(ctx, p); err != nil {
			return err
		}
	}
	return m.Docker.Up(ctx, p)
}

func (m Manager) swapPath(name string) string { return filepath.Join(m.dir(name), SwapFile) }

// markSwap grava a marca da troca com o registro do deploy em andamento.
func (m Manager) markSwap(a App, d Deploy) error {
	data, err := yaml.Marshal(d)
	if err != nil {
		return err
	}
	return fsx.WriteAtomic(m.swapPath(a.Name), data, 0o644)
}

// Interrupted diz se o app tem um deploy que o daemon nao terminou (a
// marca da troca ficou): o Recover termina.
func (m Manager) Interrupted(name string) bool {
	_, err := os.Stat(m.swapPath(name))
	return err == nil
}

// srcCommit e o commit do codigo numa pasta do app (o CAIS_COMMIT do .env
// que o deploy grava); vazio se a pasta nao existe.
func (m Manager) srcCommit(a App, src string) string {
	data, err := os.ReadFile(m.dotEnvPath(a, src))
	if err != nil {
		return ""
	}
	env, err := ParseEnv(data)
	if err != nil {
		return ""
	}
	return env["CAIS_COMMIT"]
}

// Recover termina o deploy que o daemon nao terminou (parou no meio da
// troca: queda, kill, reboot): volta a versao que estava no ar, como o
// deploy que falha faz, e registra o deploy como interrompido. Devolve se
// havia o que recuperar.
func (m Manager) Recover(ctx context.Context, name string) (bool, error) {
	a, err := m.Get(name)
	if err != nil {
		return false, err
	}
	return m.recover(ctx, a, true)
}

// recover faz o Recover. A versao boa e a do commit no ar (a.Commit, so
// muda no registro de um deploy que deu certo): esta em src (a troca nem
// tinha comecado, ou ja tinha voltado) ou em src.prev. Sem up (no comeco de
// um deploy novo) so arruma as pastas: o deploy novo sobe por cima.
func (m Manager) recover(ctx context.Context, a App, up bool) (bool, error) {
	data, err := os.ReadFile(m.swapPath(a.Name))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var d Deploy
	yaml.Unmarshal(data, &d) // marca ilegivel: ainda assim recupera

	dir := m.dir(a.Name)
	os.RemoveAll(filepath.Join(dir, NextDir))
	os.RemoveAll(filepath.Join(dir, FailedDir))
	good := false
	switch {
	case a.Commit == "": // primeiro deploy: nao tem versao anterior
	case m.srcCommit(a, SrcDir) == a.Commit:
		good = true
	case m.srcCommit(a, PrevDir) == a.Commit:
		if err := os.RemoveAll(filepath.Join(dir, SrcDir)); err != nil {
			return true, err
		}
		if err := os.Rename(filepath.Join(dir, PrevDir), filepath.Join(dir, SrcDir)); err != nil {
			return true, err
		}
		good = true
	}

	d.At, d.Interrupted, d.RolledBack = m.Now(), true, false
	d.Error = fmt.Sprintf("o deploy de %s foi interrompido no meio (o daemon parou)", short(d.Commit))
	var upErr error
	switch {
	case !good:
		d.Error += "; nao ha versao anterior pra voltar: faca o deploy de novo"
	case !up:
		d.Error += fmt.Sprintf("; o codigo de %s voltou pro lugar antes do deploy seguinte", short(a.Commit))
	default:
		if upErr = m.upCurrent(ctx, a); upErr != nil {
			d.Error += fmt.Sprintf("; voltar pra versao anterior (%s) falhou: %v", short(a.Commit), upErr)
		} else {
			d.RolledBack = true
			d.Error += fmt.Sprintf("; a versao anterior (%s) voltou e continua no ar", short(a.Commit))
		}
	}
	m.record(a, d)
	os.Remove(m.swapPath(a.Name))
	return true, upErr
}

func (m Manager) record(a App, d Deploy) {
	cur, err := m.Get(a.Name)
	if err != nil {
		return
	}
	if d.Error == "" {
		cur.Commit = d.Commit
		if !d.Rollback {
			cur.AutoHold = "" // deploy normal: o automatico segue
		}
	}
	cur.Last = &d
	cur.History = append([]Deploy{d}, cur.History...)
	if len(cur.History) > HistorySize {
		cur.History = cur.History[:HistorySize]
	}
	m.save(cur)
}

// Rollback volta o app pra um deploy anterior que deu certo (commit
// vazio: o anterior ao que esta no ar), com as imagens guardadas dele
// quando existem, pela troca normal (sem queda, e volta sozinho se falhar).
// As variaveis e os secrets sao os de agora. O deploy automatico fica
// parado ate a branch ganhar um commit novo: senao subiria de novo o que
// acabou de sair.
func (m Manager) Rollback(ctx context.Context, name, commit string) (DeployResult, error) {
	a, err := m.Get(name)
	if err != nil {
		return DeployResult{}, err
	}
	target := ""
	for _, d := range a.History {
		if !d.OK() {
			continue
		}
		if commit == "" && d.Commit != a.Commit {
			target = d.Commit
			break
		}
		if commit != "" && (d.Commit == commit || len(commit) >= 7 && strings.HasPrefix(d.Commit, commit)) {
			target = d.Commit
			break
		}
	}
	switch {
	case target == "" && commit == "":
		return DeployResult{}, fmt.Errorf("%s nao tem um deploy anterior que deu certo pra voltar", name)
	case target == "":
		return DeployResult{}, fmt.Errorf("o commit %s nao esta no historico de deploys que deram certo de %s (pra qualquer commit: cais app deploy %s -ref %s)", commit, name, name, commit)
	case target == a.Commit:
		return DeployResult{}, fmt.Errorf("%s ja esta no ar", short(target))
	}
	head, err := m.Source.Resolve(ctx, a.Repo, a.Branch)
	if err != nil {
		head = a.Commit
	}
	res, err := m.deploy(ctx, name, target, Deploy{Rollback: true})
	if err != nil {
		return res, err
	}
	if cur, err := m.Get(name); err == nil {
		cur.AutoHold = head
		m.save(cur)
	}
	return res, nil
}

func short(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

// Status lista os containers do app. Antes do primeiro deploy, nenhum.
func (m Manager) Status(ctx context.Context, name string) ([]docker.Container, error) {
	a, err := m.Get(name)
	if err != nil {
		return nil, err
	}
	if !m.deployed(a) {
		return nil, nil
	}
	return m.Docker.PS(ctx, m.project(a, SrcDir))
}

// Restart reinicia um service do app (ou todos, com service vazio).
func (m Manager) Restart(ctx context.Context, name, service string) error {
	a, err := m.requireDeployed(name)
	if err != nil {
		return err
	}
	return m.Docker.Restart(ctx, m.project(a, SrcDir), service)
}

// Stop para o app sem apagar nada; Reiniciar ou o proximo deploy volta.
func (m Manager) Stop(ctx context.Context, name string) error {
	a, err := m.requireDeployed(name)
	if err != nil {
		return err
	}
	return m.Docker.Stop(ctx, m.project(a, SrcDir))
}

// Delete remove o app: containers, redes e imagens dele, e a pasta (codigo,
// variaveis e secrets). Com volumes, apaga tambem os volumes do compose (os
// dados). Database criado na infra nao e do app e fica.
func (m Manager) Delete(ctx context.Context, name string, volumes bool) error {
	if _, err := m.Get(name); err != nil {
		return err
	}
	if err := m.Docker.Down(ctx, name, volumes); err != nil {
		return err
	}
	return os.RemoveAll(m.dir(name))
}

func (m Manager) requireDeployed(name string) (App, error) {
	a, err := m.Get(name)
	if err != nil {
		return a, err
	}
	if !m.deployed(a) {
		return a, fmt.Errorf("app %s ainda nao teve deploy", name)
	}
	return a, nil
}

// Secret e o estado de um secret do app: so o nome, nunca o valor.
type Secret struct {
	Name string `json:"name"`
	// Infra e o banco (postgres/portal) ou bucket (s3/fotos) da infra cuja
	// credencial esta no arquivo: gravado pelo cais, desliga pelo banco ou
	// bucket, nao apagando o secret.
	Infra string `json:"infra,omitempty"`
}

// Secrets lista os secrets do app.
func (m Manager) Secrets(name string) ([]Secret, error) {
	a, err := m.Get(name)
	if err != nil {
		return nil, err
	}
	names, err := m.secrets(name).List()
	if err != nil {
		return nil, err
	}
	out := make([]Secret, 0, len(names))
	for _, n := range names {
		s := Secret{Name: n}
		s.Infra, _ = infraOfSecret(a, n)
		out = append(out, s)
	}
	return out, nil
}

// SetSecret grava um secret do app. Vale a partir do proximo deploy (ou
// Reiniciar recriando: o arquivo montado so muda num container novo).
func (m Manager) SetSecret(name, secret string, value []byte) error {
	a, err := m.Get(name)
	if err != nil {
		return err
	}
	if id, ok := infraOfSecret(a, secret); ok {
		return fmt.Errorf("%s e a credencial de %s, gravada pelo cais a cada deploy; pra trocar, desligue esse e ligue outro", secret, id)
	}
	if len(value) == 0 {
		return errors.New("valor vazio")
	}
	return m.secrets(name).Put(secret, value)
}

// DeleteSecret apaga um secret do app.
func (m Manager) DeleteSecret(name, secret string) error {
	a, err := m.Get(name)
	if err != nil {
		return err
	}
	if id, ok := infraOfSecret(a, secret); ok {
		return fmt.Errorf("%s e a credencial de %s: desligue ele do app em vez de apagar o secret", secret, id)
	}
	if err := secrets.CheckName(secret); err != nil {
		return err
	}
	return m.secrets(name).Delete(secret)
}

// Env le as variaveis do app.
func (m Manager) Env(name string) (map[string]string, error) {
	if _, err := m.Get(name); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(m.dir(name), EnvFile))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	return ParseEnv(data)
}

// SetEnv troca as variaveis do app (valem no proximo deploy).
func (m Manager) SetEnv(name string, env map[string]string) error {
	if _, err := m.Get(name); err != nil {
		return err
	}
	data, err := FormatEnv(env)
	if err != nil {
		return err
	}
	return fsx.WriteAtomic(filepath.Join(m.dir(name), EnvFile), data, 0o600)
}

// reservedEnv sao variaveis que o cais escreve no .env de todo app.
var reservedEnv = []string{"CAIS_APP", "CAIS_COMMIT", "CAIS_SECRETS"}

// dotEnvPath e o .env do projeto: o compose le o da pasta do arquivo de
// compose (que pode nao ser a raiz do repositorio).
func (m Manager) dotEnvPath(a App, src string) string {
	return filepath.Join(m.dir(a.Name), src, filepath.FromSlash(path.Dir(a.ComposeFile())), ".env")
}

// writeDotEnv grava o .env do projeto: as variaveis do painel mais as do
// cais (CAIS_SECRETS e a pasta dos secrets, pro compose referenciar).
func (m Manager) writeDotEnv(a App, src, commit string) error {
	env, err := m.Env(a.Name)
	if err != nil {
		return err
	}
	env["CAIS_APP"] = a.Name
	env["CAIS_COMMIT"] = commit
	env["CAIS_SECRETS"] = m.SecretsPath(a.Name)
	data, err := FormatEnv(env)
	if err != nil {
		return err
	}
	return fsx.WriteAtomic(m.dotEnvPath(a, src), data, 0o600)
}

// IsReservedEnv diz se a variavel e escrita pelo cais.
func IsReservedEnv(k string) bool { return slices.Contains(reservedEnv, k) }
