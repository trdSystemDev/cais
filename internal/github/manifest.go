package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// ConnectPage e a pagina estatica do cais (GitHub Pages do repositorio,
// docs/github/index.html) que cria o GitHub App pelo manifest: monta o
// formulario e manda pro GitHub; na volta, mostra o codigo pra colar no
// cais. O cais nao abre porta, entao a volta do GitHub nao chega na VPS.
const ConnectPage = "https://trdsystemdev.github.io/cais/github/"

var (
	// o GitHub limita o nome do app a 34 caracteres
	appName      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,33}$`)
	manifestCode = regexp.MustCompile(`^[A-Za-z0-9_-]{8,100}$`)
)

// ConnectLink e o link que cria o GitHub App name na conta de quem abre (org
// vazia) ou na organizacao org. As permissoes ficam na pagina: so ler o
// codigo (Contents: read), sem webhook.
func ConnectLink(name, org string) (string, error) {
	if !appName.MatchString(name) {
		return "", fmt.Errorf("nome do GitHub App invalido %q: letras, numeros, . _ e -, ate 34", name)
	}
	q := url.Values{"name": {name}}
	if org = strings.TrimSpace(org); org != "" {
		if !ownerName.MatchString(org) {
			return "", fmt.Errorf("organizacao invalida %q: o nome dela no GitHub (github.com/<org>)", org)
		}
		q.Set("org", org)
	}
	return ConnectPage + "?" + q.Encode(), nil
}

// InstallLink e onde instalar o app nos repositorios.
func InstallLink(slug string) string { return "https://github.com/apps/" + slug + "/installations/new" }

// Created e o GitHub App criado pelo manifest, com a chave privada (o GitHub
// so entrega a chave aqui, uma vez).
type Created struct {
	ID      int64  `json:"id"`
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	HTMLURL string `json:"html_url"`
	Owner   struct {
		Login string `json:"login"`
	} `json:"owner"`
	PEM string `json:"pem"`
}

// ConvertManifest troca o codigo da volta do GitHub (vale 1 hora e uma vez
// so) pelo App ID e a chave privada. Sem autenticacao: o codigo e a prova.
func (a App) ConvertManifest(ctx context.Context, code string) (Created, error) {
	code = strings.TrimSpace(code)
	if !manifestCode.MatchString(code) {
		return Created{}, errors.New("codigo invalido: cole o codigo que a pagina mostrou depois do Create GitHub App")
	}
	var c Created
	_, err := a.do(ctx, request{method: http.MethodPost, path: "/app-manifests/" + code + "/conversions"}, &c)
	var ge *Error
	if errors.As(err, &ge) && (ge.Status == http.StatusNotFound || ge.Status == http.StatusUnprocessableEntity) {
		return Created{}, errors.New("o GitHub nao aceitou o codigo: ele vale 1 hora e so uma vez; crie o app de novo pelo Conectar")
	}
	if err != nil {
		return Created{}, err
	}
	if c.ID <= 0 || c.Slug == "" || c.PEM == "" {
		return Created{}, errors.New("resposta do GitHub sem o App ID, o nome ou a chave")
	}
	if _, err := ParseKey([]byte(c.PEM)); err != nil {
		return Created{}, err
	}
	return c, nil
}
