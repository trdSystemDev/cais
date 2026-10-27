package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ErrNotFound indica que ainda nao existe config.yaml: a VPS nao passou
// pelo Setup.
var ErrNotFound = errors.New("config.yaml nao encontrado (rode o Setup)")

// Config e o conteudo de /etc/cais/config.yaml. So guarda o que um humano
// decide; secrets nunca entram aqui (ficam em SecretsDir).
type Config struct {
	// Domain e o dominio base da VPS. Os subdominios das stacks saem dele
	// (ci.<domain>, db.<domain>, registry.<domain>) se Hosts nao disser outra coisa.
	Domain string `yaml:"domain" json:"domain"`
	// Email recebe os avisos do Let's Encrypt.
	Email string `yaml:"email" json:"email"`
	// Stacks sao as stacks base ativas (proxy, infra, registry, woodpecker, dbgate).
	Stacks     []string   `yaml:"stacks" json:"stacks"`
	Proxy      Proxy      `yaml:"proxy" json:"proxy"`
	Hosts      Hosts      `yaml:"hosts,omitempty" json:"hosts"`
	Woodpecker Woodpecker `yaml:"woodpecker,omitempty" json:"woodpecker"`
}

// Proxy sao as portas do host publicadas pelo BunkerWeb. Fora de 80/443 so
// faz sentido numa migracao, com outro proxy ainda segurando essas portas.
type Proxy struct {
	HTTPPort  int `yaml:"http_port" json:"http_port"`
	HTTPSPort int `yaml:"https_port" json:"https_port"`
}

// Hosts troca o nome publico de uma stack. Vazio usa o padrao.
type Hosts struct {
	CI string `yaml:"ci,omitempty" json:"ci,omitempty"`
	DB string `yaml:"db,omitempty" json:"db,omitempty"`
	// Registry e o nome do registry de imagens, usado no push do CI e no
	// pull da VPS (e de qualquer outra maquina com o login).
	Registry string `yaml:"registry,omitempty" json:"registry,omitempty"`
}

// Woodpecker e o que o CI precisa e nao e secret. O client secret do app
// OAuth do GitHub fica em SecretsDir.
type Woodpecker struct {
	// GitHubClient e o Client ID do OAuth app do GitHub.
	GitHubClient string `yaml:"github_client,omitempty" json:"github_client,omitempty"`
	// Admin e o usuario do GitHub que administra o Woodpecker.
	Admin string `yaml:"admin,omitempty" json:"admin,omitempty"`
}

// Default devolve a configuracao inicial do Setup, sem dominio e e-mail.
func Default() Config {
	return Config{
		Stacks: []string{"proxy", "infra", "registry", "woodpecker", "dbgate"},
		Proxy:  Proxy{HTTPPort: 80, HTTPSPort: 443},
	}
}

// CIHost e o nome publico do Woodpecker.
func (c Config) CIHost() string {
	if c.Hosts.CI != "" {
		return c.Hosts.CI
	}
	return "ci." + c.Domain
}

// DBHost e o nome publico do DBGate.
func (c Config) DBHost() string {
	if c.Hosts.DB != "" {
		return c.Hosts.DB
	}
	return "db." + c.Domain
}

// RegistryHost e o nome publico do registry de imagens.
func (c Config) RegistryHost() string {
	if c.Hosts.Registry != "" {
		return c.Hosts.Registry
	}
	return "registry." + c.Domain
}

// HasStack diz se a stack esta ativa.
func (c Config) HasStack(name string) bool {
	for _, s := range c.Stacks {
		if s == name {
			return true
		}
	}
	return false
}

var (
	hostLabel    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	githubUser   = regexp.MustCompile(`^[A-Za-z0-9](-?[A-Za-z0-9]){0,38}$`)
	githubClient = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
)

// Validate confere os campos gerais. As regras de cada stack (dependencias,
// campos obrigatorios do Woodpecker) ficam no pacote stacks. Os valores
// vao direto pros compose, entao tudo e restrito a caracteres seguros.
func (c Config) Validate() error {
	var errs []error
	if err := validHost(c.Domain); err != nil {
		errs = append(errs, fmt.Errorf("domain: %w", err))
	}
	if err := validEmail(c.Email); err != nil {
		errs = append(errs, fmt.Errorf("email: %w", err))
	}
	if c.Hosts.CI != "" {
		if err := validHost(c.Hosts.CI); err != nil {
			errs = append(errs, fmt.Errorf("hosts.ci: %w", err))
		}
	}
	if c.Hosts.DB != "" {
		if err := validHost(c.Hosts.DB); err != nil {
			errs = append(errs, fmt.Errorf("hosts.db: %w", err))
		}
	}
	if c.Hosts.Registry != "" {
		if err := validHost(c.Hosts.Registry); err != nil {
			errs = append(errs, fmt.Errorf("hosts.registry: %w", err))
		}
	}
	if !validPort(c.Proxy.HTTPPort) {
		errs = append(errs, fmt.Errorf("proxy.http_port: porta invalida %d", c.Proxy.HTTPPort))
	}
	if !validPort(c.Proxy.HTTPSPort) {
		errs = append(errs, fmt.Errorf("proxy.https_port: porta invalida %d", c.Proxy.HTTPSPort))
	}
	if c.Proxy.HTTPPort == c.Proxy.HTTPSPort {
		errs = append(errs, errors.New("proxy: http_port e https_port nao podem ser iguais"))
	}
	if v := c.Woodpecker.GitHubClient; v != "" && !githubClient.MatchString(v) {
		errs = append(errs, fmt.Errorf("woodpecker.github_client: client id invalido %q", v))
	}
	if v := c.Woodpecker.Admin; v != "" && !githubUser.MatchString(v) {
		errs = append(errs, fmt.Errorf("woodpecker.admin: usuario do GitHub invalido %q", v))
	}
	return errors.Join(errs...)
}

func validHost(h string) error {
	if h == "" {
		return errors.New("vazio")
	}
	if len(h) > 253 {
		return fmt.Errorf("%q passa de 253 caracteres", h)
	}
	labels := strings.Split(h, ".")
	if len(labels) < 2 {
		return fmt.Errorf("%q nao e um dominio completo (ex: exemplo.com.br)", h)
	}
	for _, l := range labels {
		if !hostLabel.MatchString(l) {
			return fmt.Errorf("%q nao e um dominio valido (so minusculas, numeros, '-' e '.')", h)
		}
	}
	return nil
}

func validEmail(e string) error {
	if e == "" {
		return errors.New("vazio")
	}
	local, domain, ok := strings.Cut(e, "@")
	if !ok || local == "" || strings.ContainsAny(local, " \t\"'@") {
		return fmt.Errorf("%q nao e um e-mail valido", e)
	}
	if validHost(domain) != nil {
		return fmt.Errorf("%q nao e um e-mail valido", e)
	}
	return nil
}

func validPort(p int) bool { return p > 0 && p <= 65535 }

// Load le e valida o config.yaml. Campos ausentes recebem o padrao de
// Default; campos desconhecidos sao erro, pra um erro de digitacao nao
// passar em silencio. Se o arquivo nao existe, devolve ErrNotFound.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, ErrNotFound
	}
	if err != nil {
		return Config{}, err
	}
	c := Default()
	c.Stacks = nil // a lista do arquivo substitui a padrao, nao soma
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Save valida e grava o config.yaml de forma atomica (arquivo temporario +
// rename): quem le nunca ve um arquivo pela metade.
func Save(path string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	data = append([]byte("# gerado pelo cais; pode editar, o cais valida ao ler\n"), data...)

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op depois do rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
