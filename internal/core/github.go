package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/github"
	"github.com/trdSystemDev/cais/internal/secrets"
)

// GitHubKeySecret e o secret com a chave privada do GitHub App.
const GitHubKeySecret = "github_app_key"

// githubApp monta o GitHub App a partir da config e da chave.
func (c *Core) githubApp() (github.App, error) {
	cfg, err := config.Load(c.deps.ConfigPath)
	if err != nil {
		return github.App{}, err
	}
	if cfg.GitHub.AppID == 0 {
		return github.App{}, errors.New("GitHub App nao configurado: informe o App ID na aba Setup (ou cais setup config --github-app-id <id>)")
	}
	pem, err := secrets.FileStore{Dir: c.deps.SecretsDir}.Get(GitHubKeySecret)
	if errors.Is(err, secrets.ErrNotFound) {
		return github.App{}, errors.New("falta a chave do GitHub App: cole o .pem na aba Setup (ou cais setup secret github_app_key < arquivo.pem)")
	}
	if err != nil {
		return github.App{}, err
	}
	key, err := github.ParseKey(pem)
	if err != nil {
		return github.App{}, err
	}
	return github.App{ID: cfg.GitHub.AppID, Key: key, BaseURL: c.deps.GitHubURL, Now: c.deps.Now}, nil
}

// TestGitHub confere o GitHub App sem abrir repositorio: o GitHub aceita o
// App ID com a chave, e em que contas ele esta instalado. appID 0 e key
// vazia usam o que esta salvo (config e secret), pra testar o formulario
// antes de salvar.
func (c *Core) TestGitHub(ctx context.Context, appID int64, key string) (github.Check, error) {
	if appID < 0 {
		return github.Check{}, errors.New("App ID invalido: e o numero na pagina do app no GitHub")
	}
	// o salvo e so o padrao do que o formulario nao trouxe: config ou chave
	// faltando nao e erro aqui (o teste roda antes de salvar)
	saved, _ := c.githubApp()
	var err error
	app := github.App{ID: appID, BaseURL: c.deps.GitHubURL, Now: c.deps.Now}
	switch {
	case key != "":
		pem, err := github.NormalizeKey(key)
		if err != nil {
			return github.Check{}, err
		}
		if app.Key, err = github.ParseKey(pem); err != nil {
			return github.Check{}, err
		}
	case saved.Key != nil:
		app.Key = saved.Key
	default:
		if pem, gerr := (secrets.FileStore{Dir: c.deps.SecretsDir}).Get(GitHubKeySecret); gerr == nil {
			if app.Key, err = github.ParseKey(pem); err != nil {
				return github.Check{}, err
			}
		} else if errors.Is(gerr, secrets.ErrNotFound) {
			return github.Check{}, errors.New("falta a chave do GitHub App: cole o .pem (ou o caminho dele)")
		} else {
			return github.Check{}, gerr
		}
	}
	if app.ID == 0 {
		if saved.ID == 0 {
			return github.Check{}, errors.New("falta o App ID do GitHub App")
		}
		app.ID = saved.ID
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return app.Check(ctx)
}

// githubSource e a origem do codigo dos apps pelo GitHub App. As sessoes
// (token de uma hora restrito ao repositorio) ficam guardadas no Core e sao
// reaproveitadas enquanto o token vale.
type githubSource struct{ c *Core }

// with roda fn com a sessao do repositorio. Token recusado (401: app
// reinstalado, chave trocada) descarta a sessao e tenta uma vez com outra.
func (g githubSource) with(ctx context.Context, repo string, fn func(*github.Session) error) error {
	r, err := github.ParseRepo(repo)
	if err != nil {
		return err
	}
	gh, err := g.c.githubApp()
	if err != nil {
		return err
	}
	for try := 0; ; try++ {
		s, err := g.c.gh.Open(ctx, gh, r)
		if err != nil {
			return err
		}
		err = fn(s)
		if try == 0 && github.IsUnauthorized(err) {
			g.c.gh.Forget(gh, r)
			continue
		}
		return err
	}
}

func (g githubSource) Resolve(ctx context.Context, repo, ref string) (sha string, err error) {
	err = g.with(ctx, repo, func(s *github.Session) (err error) {
		sha, err = s.Resolve(ctx, ref)
		return err
	})
	return sha, err
}

func (g githubSource) Download(ctx context.Context, repo, commit, dir string) error {
	return g.with(ctx, repo, func(s *github.Session) error {
		return s.Download(ctx, commit, dir)
	})
}

// GitHubConnected e o GitHub App criado e gravado pelo Conectar.
type GitHubConnected struct {
	AppID int64  `json:"app_id"`
	Name  string `json:"name"`
	Owner string `json:"owner"`
	// InstallURL e onde escolher os repositorios que o app le.
	InstallURL string `json:"install_url"`
}

// GitHubConnectLink e o link que cria o GitHub App do cais pelo manifest,
// na conta de quem abre ou na organizacao org. O nome leva o hostname da
// VPS e um sufixo aleatorio: nome de GitHub App e unico no GitHub inteiro.
func (c *Core) GitHubConnectLink(_ context.Context, org string) (string, error) {
	return github.ConnectLink(githubAppName(), org)
}

func githubAppName() string {
	host, _ := os.Hostname()
	host = strings.Trim(nonName.ReplaceAllString(strings.ToLower(host), "-"), "-")
	const maxHost = 34 - len("cais--") - 4
	if len(host) > maxHost {
		host = strings.Trim(host[:maxHost], "-")
	}
	b := make([]byte, 4)
	rand.Read(b)
	suffix := hex.EncodeToString(b)[:4]
	if host == "" {
		return "cais-" + suffix
	}
	return "cais-" + host + "-" + suffix
}

var nonName = regexp.MustCompile(`[^a-z0-9]+`)

// ConnectGitHub troca o codigo da volta do GitHub (o app criado pelo link
// do GitHubConnectLink) pelo App ID e a chave privada e grava os dois: a
// chave no secret e o App ID na config. Substitui o GitHub App que houver.
func (c *Core) ConnectGitHub(ctx context.Context, code string) (GitHubConnected, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	app, err := github.App{BaseURL: c.deps.GitHubURL, Now: c.deps.Now}.ConvertManifest(ctx, code)
	if err != nil {
		return GitHubConnected{}, err
	}
	r := GitHubConnected{AppID: app.ID, Name: app.Name, Owner: app.Owner.Login, InstallURL: github.InstallLink(app.Slug)}
	if r.Owner == "" {
		r.Owner = "a sua conta"
	}
	// a chave so vem agora: grava antes da config, e o erro da config diz o
	// App ID pra nao perder o app criado
	if err := c.SetSecret(ctx, GitHubKeySecret, app.PEM); err != nil {
		return r, fmt.Errorf("gravando a chave do GitHub App %d: %w", app.ID, err)
	}
	cfg, err := config.Load(c.deps.ConfigPath)
	if errors.Is(err, config.ErrNotFound) {
		cfg, err = config.Default(), nil
	}
	if err == nil {
		cfg.GitHub.AppID = app.ID
		err = c.saveConfig(ctx, cfg)
	}
	if err != nil {
		return r, fmt.Errorf("a chave foi gravada, mas nao o App ID %d (grave com cais setup config --github-app-id %d): %w", app.ID, app.ID, err)
	}
	c.deps.Logger.Info("github app conectado", "app", app.Slug, "id", app.ID, "dono", r.Owner)
	return r, nil
}
