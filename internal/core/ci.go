package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/trdSystemDev/cais/internal/ci"
	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/secrets"
)

// CIInfo e o que o administrador precisa pra ligar o Woodpecker ao cais.
type CIInfo struct {
	Pipeline string `json:"pipeline"`
	// AuthorizedKeys e a linha do ~deploy/.ssh/authorized_keys.
	AuthorizedKeys string `json:"authorized_keys"`
	DeployUser     string `json:"deploy_user"`
	RegistryHost   string `json:"registry_host"`
	// Secrets sao os secrets a cadastrar no Woodpecker (na organizacao,
	// valendo pra todos os repositorios). Tem valor: so pelo socket do admin.
	Secrets []CISecret `json:"secrets"`
}

// CISecret e um secret do Woodpecker com o valor.
type CISecret struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Desc  string `json:"desc"`
}

// CISetup gera (uma vez) a chave de deploy do CI e devolve o pipeline, a
// linha do authorized_keys e os secrets do Woodpecker.
func (c *Core) CISetup(context.Context) (CIInfo, error) {
	cfg, err := config.Load(c.deps.ConfigPath)
	if err != nil {
		return CIInfo{}, err
	}
	if !cfg.HasStack("registry") {
		return CIInfo{}, errors.New("o registry esta desligado: o CI precisa dele pra publicar as imagens")
	}
	store := secrets.FileStore{Dir: c.deps.SecretsDir}
	regPw, err := store.Get(secrets.RegistryPasswordSecret(secrets.RegistryCI))
	if err != nil {
		return CIInfo{}, fmt.Errorf("senha do usuario ci do registry (rode o Setup antes): %w", err)
	}
	pub, err := ci.EnsureDeployKey(store)
	if err != nil {
		return CIInfo{}, err
	}
	priv, err := store.Get(ci.KeySecret)
	if err != nil {
		return CIInfo{}, err
	}
	bin, err := os.Executable()
	if err != nil {
		bin = "/usr/local/bin/cais"
	}
	if resolved, err := filepath.EvalSymlinks(bin); err == nil {
		bin = resolved
	}
	return CIInfo{
		Pipeline:       string(ci.Pipeline()),
		AuthorizedKeys: ci.AuthorizedKeysLine(bin, pub),
		DeployUser:     ci.DeployUser,
		RegistryHost:   cfg.RegistryHost(),
		Secrets: []CISecret{
			{Name: "registry_password", Value: string(regPw), Desc: "senha do usuario ci do registry (push das imagens)"},
			{Name: "deploy_ssh_key", Value: string(priv), Desc: "chave SSH que so pede o deploy (forced command)"},
		},
	}, nil
}
