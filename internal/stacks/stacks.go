package stacks

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"text/template"

	"github.com/trdSystemDev/cais/internal/config"
)

//go:embed templates/*/docker-compose.yml.tmpl
var templates embed.FS

// Stack e uma stack base da VPS.
type Stack struct {
	Name string `json:"name"`
	Desc string `json:"desc"`
	// Requires sao as stacks que precisam estar de pe antes (redes externas).
	Requires []string `json:"requires,omitempty"`
	// Optional e servico que o usuario liga e desliga (aba Infra). Hoje
	// todas sao: nada sobe sem ser ligado.
	Optional bool `json:"optional,omitempty"`
}

// All lista as stacks na ordem de subida: cada uma so depende das anteriores.
var All = []Stack{
	{Name: "proxy", Desc: "Traefik: publica os apps com HTTPS (Let's Encrypt)", Optional: true},
	{Name: "postgres", Desc: "Postgres compartilhado, so no loopback (tunel SSH)", Optional: true},
	{Name: "mysql", Desc: "MySQL compartilhado, so no loopback (tunel SSH)", Optional: true},
	{Name: "garage", Desc: "Storage S3 (Garage), publico com TLS", Requires: []string{"proxy"}, Optional: true},
}

// Get procura uma stack pelo nome.
func Get(name string) (Stack, bool) {
	i := slices.IndexFunc(All, func(s Stack) bool { return s.Name == name })
	if i < 0 {
		return Stack{}, false
	}
	return All[i], true
}

// Enabled valida as stacks ativas na config e as devolve na ordem de subida.
func Enabled(c config.Config) ([]Stack, error) {
	var errs []error
	seen := map[string]bool{}
	for _, name := range c.Stacks {
		_, ok := Get(name)
		switch {
		case !ok:
			errs = append(errs, fmt.Errorf("stack desconhecida %q", name))
		case seen[name]:
			errs = append(errs, fmt.Errorf("stack %q repetida", name))
		}
		seen[name] = true
	}

	var out []Stack
	for _, s := range All {
		if !seen[s.Name] {
			continue
		}
		for _, req := range s.Requires {
			if !seen[req] {
				errs = append(errs, fmt.Errorf("o %s precisa do %s ligado: ligue o %s antes (aba Infra, ou cais service %s on); pra desligar o %s, desligue o %s primeiro", s.Name, req, req, req, req, s.Name))
			}
		}
		if err := checkStack(s.Name, c); err != nil {
			errs = append(errs, err)
		}
		out = append(out, s)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return out, nil
}

// checkStack confere os campos da config que so uma stack exige: o proxy
// com Let's Encrypt precisa do e-mail.
func checkStack(name string, c config.Config) error {
	if name == "proxy" && c.LetsEncrypt() && c.Email == "" {
		return errors.New("proxy: falta o e-mail do Let's Encrypt (no proxy, na aba Infra)")
	}
	return nil
}

// Rendered e o compose de uma stack pronto pra gravar.
type Rendered struct {
	Compose []byte
	// Secrets sao os nomes dos arquivos em secretsDir que o compose usa,
	// na ordem em que aparecem. Saem do proprio template, entao nunca
	// divergem dele.
	Secrets []string
}

// vars e o que os templates enxergam.
type vars struct {
	Email       string
	LetsEncrypt bool
	HTTPPort    int
	HTTPSPort   int
	// BehindProxy: portas so no 127.0.0.1, confiando no X-Forwarded-For
	// do proxy da frente (TrustedIPs)
	BehindProxy bool
	TrustedIPs  string
	S3Host      string
}

var secretName = regexp.MustCompile(`^[a-z0-9_]+$`)

// Render gera o docker-compose.yml de uma stack. Os secrets viram caminhos
// absolutos em secretsDir, entao o compose fica valido de qualquer pasta.
func Render(name string, c config.Config, secretsDir string) (Rendered, error) {
	if _, ok := Get(name); !ok {
		return Rendered{}, fmt.Errorf("stack desconhecida %q", name)
	}
	if err := c.Validate(); err != nil {
		return Rendered{}, err
	}
	if err := checkStack(name, c); err != nil {
		return Rendered{}, err
	}
	if !filepath.IsAbs(secretsDir) || strings.ContainsAny(secretsDir, "\"\\\n") {
		return Rendered{}, fmt.Errorf("pasta de secrets invalida %q: precisa ser absoluta e sem aspas", secretsDir)
	}

	var used []string
	funcs := template.FuncMap{
		"secret": func(n string) (string, error) {
			if !secretName.MatchString(n) {
				return "", fmt.Errorf("nome de secret invalido %q", n)
			}
			if !slices.Contains(used, n) {
				used = append(used, n)
			}
			return filepath.Join(secretsDir, n), nil
		},
	}
	path := "templates/" + name + "/docker-compose.yml.tmpl"
	tmpl, err := template.New(filepath.Base(path)).Funcs(funcs).ParseFS(templates, path)
	if err != nil {
		return Rendered{}, err
	}

	var buf bytes.Buffer
	err = tmpl.Execute(&buf, vars{
		Email:       c.Email,
		LetsEncrypt: c.LetsEncrypt(),
		HTTPPort:    c.Proxy.HTTPPort,
		HTTPSPort:   c.Proxy.HTTPSPort,
		BehindProxy: c.Proxy.BehindProxy,
		TrustedIPs:  config.BehindProxyTrustedIPs,
		S3Host:      c.S3Host(),
	})
	if err != nil {
		return Rendered{}, fmt.Errorf("stack %s: %w", name, err)
	}
	return Rendered{Compose: buf.Bytes(), Secrets: used}, nil
}
