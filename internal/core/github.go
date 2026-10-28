package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/ci"
	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/detect"
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
		return github.App{}, errors.New("GitHub App nao configurado: informe o App ID (cais setup config --github-app-id <id>)")
	}
	pem, err := secrets.FileStore{Dir: c.deps.SecretsDir}.Get(GitHubKeySecret)
	if errors.Is(err, secrets.ErrNotFound) {
		return github.App{}, errors.New("falta a chave do GitHub App: cais setup secret github_app_key < arquivo.pem")
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

// repoApp confere que o repositorio serve de app: o CI usa o nome do
// repositorio como nome do app e da imagem, entao os dois sao o mesmo.
func repoApp(link string) (github.Repo, error) {
	repo, err := github.ParseRepo(link)
	if err != nil {
		return repo, err
	}
	if err := apps.CheckName(repo.Name); err != nil {
		return repo, fmt.Errorf("o repositorio %s nao serve de nome de app (%v); o CI usa o nome do repositorio como nome do app e da imagem: renomeie o repositorio no GitHub", repo, err)
	}
	return repo, nil
}

// PlanAppFromGitHub le do repositorio (privado, pelo GitHub App) so os
// arquivos da analise e devolve a proposta de configuracao. O app tem o
// nome do repositorio. Nao grava nada.
func (c *Core) PlanAppFromGitHub(ctx context.Context, link string) (apps.Plan, error) {
	repo, err := repoApp(link)
	if err != nil {
		return apps.Plan{}, err
	}
	gh, err := c.githubApp()
	if err != nil {
		return apps.Plan{}, err
	}
	s, err := gh.Open(ctx, repo)
	if err != nil {
		return apps.Plan{}, err
	}
	files, err := s.ReadFiles(ctx, detect.Wanted)
	if err != nil {
		return apps.Plan{}, err
	}
	plan, err := c.PlanApp(ctx, repo.Name, files)
	if err != nil {
		return plan, err
	}
	plan.Repo = repo.String()
	return plan, nil
}

// ProposeAppCI abre (ou atualiza) um pull request no repositorio do app com
// o .woodpecker.yml do cais e devolve o link.
func (c *Core) ProposeAppCI(ctx context.Context, name string) (string, error) {
	a, err := c.appManager().Get(name)
	if err != nil {
		return "", err
	}
	if a.Repo == "" {
		return "", fmt.Errorf("app %s nao veio do GitHub: copie o pipeline com cais ci pipeline > .woodpecker.yml", name)
	}
	repo, err := repoApp(a.Repo)
	if err != nil {
		return "", err
	}
	gh, err := c.githubApp()
	if err != nil {
		return "", err
	}
	s, err := gh.Open(ctx, repo)
	if err != nil {
		return "", err
	}
	body := "Pipeline do cais: build do Dockerfile, push no registry e deploy da tag do commit na VPS.\n\n" +
		"A configuracao do app (porta, banco, variaveis, secrets) fica no cais; o CI so escolhe a tag.\n" +
		"Antes do merge, cadastre no Woodpecker os secrets `registry_password` e `deploy_ssh_key` (`cais ci setup`)."
	link, err := s.ProposeFile(ctx, "cais/ci", ".woodpecker.yml", ci.Pipeline(), "CI do cais (build e deploy)", body)
	if err != nil {
		var gerr *github.Error
		if errors.As(err, &gerr) && (gerr.Status == 403 || gerr.Status == 404) {
			return "", fmt.Errorf("%w\no GitHub App precisa de Contents e Pull requests com Read and write pra abrir o PR", err)
		}
		return "", err
	}
	return link, nil
}
