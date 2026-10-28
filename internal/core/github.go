package core

import (
	"context"
	"errors"

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
	return github.App{ID: cfg.GitHub.AppID, Key: key, BaseURL: c.deps.GitHubURL}, nil
}

// githubSource e a origem do codigo dos apps pelo GitHub App: cada acesso
// pede um token de uma hora restrito ao repositorio.
type githubSource struct{ c *Core }

func (g githubSource) open(ctx context.Context, repo string) (*github.Session, error) {
	r, err := github.ParseRepo(repo)
	if err != nil {
		return nil, err
	}
	gh, err := g.c.githubApp()
	if err != nil {
		return nil, err
	}
	return gh.Open(ctx, r)
}

func (g githubSource) Resolve(ctx context.Context, repo, ref string) (string, error) {
	s, err := g.open(ctx, repo)
	if err != nil {
		return "", err
	}
	return s.Resolve(ctx, ref)
}

func (g githubSource) Download(ctx context.Context, repo, commit, dir string) error {
	s, err := g.open(ctx, repo)
	if err != nil {
		return err
	}
	return s.Download(ctx, commit, dir)
}
