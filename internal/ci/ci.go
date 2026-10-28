// Package ci e o que liga o Woodpecker ao cais: o pipeline (igual pra todo
// app) e a chave SSH com que o CI pede o deploy.
package ci

import (
	"crypto/ed25519"
	"crypto/rand"
	_ "embed"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/trdSystemDev/cais/internal/secrets"
)

//go:embed woodpecker.yml
var pipeline []byte

// Pipeline e o .woodpecker.yml dos apps.
func Pipeline() []byte { return pipeline }

// Nomes dos secrets da chave de deploy na pasta de secrets centrais.
const (
	KeySecret    = "ci_deploy_key"
	PubKeySecret = "ci_deploy_key_pub"
)

// DeployUser e o usuario da VPS que o CI usa no ssh.
const DeployUser = "cais-deploy"

// EnsureDeployKey gera (uma vez) o par de chaves do CI e devolve a chave
// publica no formato do authorized_keys.
func EnsureDeployKey(s secrets.Store) (string, error) {
	if pub, err := s.Get(PubKeySecret); err == nil {
		if _, err := s.Get(KeySecret); err == nil {
			return strings.TrimSpace(string(pub)), nil
		}
	} else if !errors.Is(err, secrets.ErrNotFound) {
		return "", err
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, "cais-ci")
	if err != nil {
		return "", err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", err
	}
	authorized := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " cais-ci"
	if err := s.Put(KeySecret, pem.EncodeToMemory(block)); err != nil {
		return "", err
	}
	if err := s.Put(PubKeySecret, []byte(authorized+"\n")); err != nil {
		return "", err
	}
	return authorized, nil
}

// AuthorizedKeysLine e a linha do ~deploy/.ssh/authorized_keys: a chave so
// roda o cais deploy (forced command) e nao abre shell, pty nem tunel.
func AuthorizedKeysLine(caisBin, publicKey string) string {
	return fmt.Sprintf(`command="%s deploy",restrict %s`, caisBin, publicKey)
}
