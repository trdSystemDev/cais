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
	"slices"
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
	// Databases sao os bancos da infra que o app usa (a credencial fica no
	// cais; o deploy grava <engine>_<nome>_user e <engine>_<nome>_pass).
	Databases []DBLink `yaml:"databases,omitempty" json:"databases,omitempty"`
	// Buckets sao os buckets do Garage que o app usa (o deploy grava
	// s3_<bucket>_access_key e s3_<bucket>_secret_key).
	Buckets []BucketLink `yaml:"buckets,omitempty" json:"buckets,omitempty"`
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
}

// Source e de onde vem o codigo (o GitHub, pelo GitHub App).
type Source interface {
	// Resolve devolve o commit de ref (branch ou sha); ref vazio e a
	// branch principal do repositorio.
	Resolve(ctx context.Context, repo, ref string) (string, error)
	// Download extrai o codigo do commit em dir (que nao existe ainda).
	Download(ctx context.Context, repo, commit, dir string) error
}

// Docker sao as operacoes de projeto compose que os apps usam.
type Docker interface {
	Config(ctx context.Context, p docker.Project) ([]byte, error)
	Build(ctx context.Context, p docker.Project) error
	Up(ctx context.Context, p docker.Project) error
	PS(ctx context.Context, p docker.Project) ([]docker.Container, error)
	Restart(ctx context.Context, p docker.Project, service string) error
	Stop(ctx context.Context, p docker.Project) error
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
}

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
	if err := os.MkdirAll(m.SecretsPath(a.Name), 0o700); err != nil {
		return App{}, err
	}
	a.Commit, a.Last, a.Databases, a.Buckets = "", nil, nil, nil
	return a, m.save(a)
}

// Update troca repositorio, branch e compose do app (vale no proximo deploy).
func (m Manager) Update(a App) error {
	cur, err := m.Get(a.Name)
	if err != nil {
		return err
	}
	if err := Check(a); err != nil {
		return err
	}
	cur.Repo, cur.Branch, cur.Compose = a.Repo, a.Branch, a.Compose
	return m.save(cur)
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
}

// Deploy baixa o commit de ref (vazio = o mais novo da branch do app),
// grava o .env, faz o build com a versao antiga ainda no ar, troca e sobe
// esperando os healthchecks. Se o up falhar e havia versao no ar, ela
// volta. Os avisos sobre o compose nao impedem o deploy.
func (m Manager) Deploy(ctx context.Context, name, ref string) (DeployResult, error) {
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
	commit, err := m.Source.Resolve(ctx, a.Repo, ref)
	if err != nil {
		return DeployResult{}, err
	}
	res := DeployResult{App: name, Commit: commit}

	// o codigo novo vai pra uma pasta propria: o app no ar nao e tocado ate
	// o build terminar
	dir := m.dir(name)
	next := "src.next"
	os.RemoveAll(filepath.Join(dir, next))
	if err := m.Source.Download(ctx, a.Repo, commit, filepath.Join(dir, next)); err != nil {
		return res, err
	}
	fail := func(err error) (DeployResult, error) {
		os.RemoveAll(filepath.Join(dir, next))
		m.record(a, Deploy{At: m.Now(), Commit: commit, Error: err.Error(), Warnings: res.Warnings, Problems: res.Problems})
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
		SecretsDir: m.SecretsPath(name), DotEnv: m.dotEnvPath(a, next), Networks: m.Networks})
	if len(res.Problems) > 0 {
		return fail(&ComposeError{App: name, Problems: res.Problems})
	}
	if err := m.Docker.Build(ctx, p); err != nil {
		return fail(err)
	}

	// troca: src vira src.prev e o novo vira src
	hadPrev := m.deployed(a)
	os.RemoveAll(filepath.Join(dir, PrevDir))
	if hadPrev {
		if err := os.Rename(filepath.Join(dir, SrcDir), filepath.Join(dir, PrevDir)); err != nil {
			return fail(err)
		}
	}
	if err := os.Rename(filepath.Join(dir, next), filepath.Join(dir, SrcDir)); err != nil {
		return fail(err)
	}

	upErr := m.Docker.Up(ctx, m.project(a, SrcDir))
	if upErr == nil {
		upErr = m.settle(ctx, a)
	}
	last := Deploy{At: m.Now(), Commit: commit, Warnings: res.Warnings}
	if upErr != nil {
		last.Error = upErr.Error()
		if hadPrev {
			if rerr := m.rollback(ctx, a); rerr != nil {
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
	return res, nil
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

// rollback volta src.prev pro lugar e sobe de novo. O build da versao
// anterior e refeito (as imagens do compose sao trocadas a cada build), mas
// com o cache das camadas.
func (m Manager) rollback(ctx context.Context, a App) error {
	dir := m.dir(a.Name)
	failed := filepath.Join(dir, "src.failed")
	os.RemoveAll(failed)
	if err := os.Rename(filepath.Join(dir, SrcDir), failed); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(dir, PrevDir), filepath.Join(dir, SrcDir)); err != nil {
		return err
	}
	os.RemoveAll(failed)
	p := m.project(a, SrcDir)
	if err := m.Docker.Build(ctx, p); err != nil {
		return err
	}
	return m.Docker.Up(ctx, p)
}

func (m Manager) record(a App, d Deploy) {
	cur, err := m.Get(a.Name)
	if err != nil {
		return
	}
	if d.Error == "" {
		cur.Commit = d.Commit
	}
	cur.Last = &d
	m.save(cur)
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
