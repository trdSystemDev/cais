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

	"github.com/trdSystemDev/cais/internal/fsx"
)

// ErrNotFound indica que ainda nao existe config.yaml: a VPS nao passou
// pelo Setup.
var ErrNotFound = errors.New("config.yaml nao encontrado (rode o Setup)")

// Config e o conteudo de /etc/cais/config.yaml. So guarda o que um humano
// decide; secrets nunca entram aqui (ficam em SecretsDir).
type Config struct {
	// Domain e o dominio base da VPS. Os subdominios das stacks saem dele
	// (ci.<domain>, registry.<domain>) se Hosts nao disser outra coisa.
	Domain string `yaml:"domain" json:"domain"`
	// Email recebe os avisos do Let's Encrypt.
	Email string `yaml:"email" json:"email"`
	// TLS e de onde vem o certificado dos sites: TLSLetsEncrypt (padrao,
	// precisa de DNS publico) ou TLSSelfSigned (teste local, ex: dominio
	// .localhost, que nao tem como ter certificado publico).
	TLS string `yaml:"tls,omitempty" json:"tls,omitempty"`
	// Stacks sao os servicos opcionais ligados (postgres, mysql, garage).
	// As stacks fixas (CoreStacks) sobem sempre e nao precisam estar aqui.
	Stacks     []string   `yaml:"stacks,omitempty" json:"stacks,omitempty"`
	Proxy      Proxy      `yaml:"proxy" json:"proxy"`
	Hosts      Hosts      `yaml:"hosts,omitempty" json:"hosts"`
	Woodpecker Woodpecker `yaml:"woodpecker,omitempty" json:"woodpecker"`
	GitHub     GitHub     `yaml:"github,omitempty" json:"github"`
}

// Proxy sao as portas do host publicadas pelo Traefik. Fora de 80/443 so
// faz sentido numa migracao, com outro proxy ainda segurando essas portas.
type Proxy struct {
	HTTPPort  int `yaml:"http_port" json:"http_port"`
	HTTPSPort int `yaml:"https_port" json:"https_port"`
}

// Hosts troca o nome publico de uma stack. Vazio usa o padrao.
type Hosts struct {
	CI string `yaml:"ci,omitempty" json:"ci,omitempty"`
	// Registry e o nome do registry de imagens, usado no push do CI e no
	// pull da VPS (e de qualquer outra maquina com o login).
	Registry string `yaml:"registry,omitempty" json:"registry,omitempty"`
	// S3 e o endpoint S3 do Garage (padrao s3.<domain>).
	S3 string `yaml:"s3,omitempty" json:"s3,omitempty"`
}

// GitHub e o GitHub App do cais, que le os repositorios privados no import
// e abre o PR do CI. A chave privada fica no secret github_app_key.
type GitHub struct {
	AppID int64 `yaml:"app_id,omitempty" json:"app_id,omitempty"`
}

// Woodpecker e o que o CI precisa e nao e secret. O client secret do app
// OAuth do GitHub fica em SecretsDir.
type Woodpecker struct {
	// GitHubClient e o Client ID do OAuth app do GitHub.
	GitHubClient string `yaml:"github_client,omitempty" json:"github_client,omitempty"`
	// Admin e o usuario do GitHub que administra o Woodpecker.
	Admin string `yaml:"admin,omitempty" json:"admin,omitempty"`
}

// Modos de TLS.
const (
	TLSLetsEncrypt = "letsencrypt"
	TLSSelfSigned  = "selfsigned"
)

// LetsEncrypt diz se os certificados vem do Let's Encrypt.
func (c Config) LetsEncrypt() bool { return c.TLS == "" || c.TLS == TLSLetsEncrypt }

// CoreStacks sobem sempre: sem proxy, registry e CI nao da pra publicar
// app nenhum. O resto (bancos, storage) e ligado conforme o uso.
var CoreStacks = []string{"proxy", "registry", "woodpecker"}

// IsCore diz se a stack e fixa.
func IsCore(name string) bool {
	for _, s := range CoreStacks {
		if s == name {
			return true
		}
	}
	return false
}

// Default devolve a configuracao inicial do Setup, sem dominio e e-mail.
func Default() Config {
	return Config{
		Proxy: Proxy{HTTPPort: 80, HTTPSPort: 443},
	}
}

// CIHost e o nome publico do Woodpecker.
func (c Config) CIHost() string {
	if c.Hosts.CI != "" {
		return c.Hosts.CI
	}
	return "ci." + c.Domain
}

// RegistryHost e o nome publico do registry de imagens.
func (c Config) RegistryHost() string {
	if c.Hosts.Registry != "" {
		return c.Hosts.Registry
	}
	return "registry." + c.Domain
}

// S3Host e o endpoint publico do Garage (API S3).
func (c Config) S3Host() string {
	if c.Hosts.S3 != "" {
		return c.Hosts.S3
	}
	return "s3." + c.Domain
}

// HasStack diz se a stack esta ativa: as fixas sempre estao.
func (c Config) HasStack(name string) bool {
	if IsCore(name) {
		return true
	}
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
	if c.Hosts.Registry != "" {
		if err := validHost(c.Hosts.Registry); err != nil {
			errs = append(errs, fmt.Errorf("hosts.registry: %w", err))
		}
	}
	if c.Hosts.S3 != "" {
		if err := validHost(c.Hosts.S3); err != nil {
			errs = append(errs, fmt.Errorf("hosts.s3: %w", err))
		}
	}
	if c.TLS != "" && c.TLS != TLSLetsEncrypt && c.TLS != TLSSelfSigned {
		errs = append(errs, fmt.Errorf("tls: %q invalido (use %s ou %s)", c.TLS, TLSLetsEncrypt, TLSSelfSigned))
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
	if c.GitHub.AppID < 0 {
		errs = append(errs, fmt.Errorf("github.app_id: id invalido %d", c.GitHub.AppID))
	}
	if v := c.Woodpecker.GitHubClient; v != "" && !githubClient.MatchString(v) {
		errs = append(errs, fmt.Errorf("woodpecker.github_client: client id invalido %q", v))
	}
	if v := c.Woodpecker.Admin; v != "" && !githubUser.MatchString(v) {
		errs = append(errs, fmt.Errorf("woodpecker.admin: usuario do GitHub invalido %q", v))
	}
	return errors.Join(errs...)
}

// ValidHost confere um nome de dominio (ex: a rota de um app).
func ValidHost(h string) error { return validHost(h) }

// ReservedHosts sao os nomes das stacks publicas: nenhum app pode usar,
// mesmo com a stack desligada (ela pode ser ligada depois).
func (c Config) ReservedHosts() []string {
	return []string{c.CIHost(), c.RegistryHost(), c.S3Host()}
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
	if labels[len(labels)-1] == "local" {
		// .local e do mDNS: o sistema pergunta pro Avahi e nem le o /etc/hosts
		return fmt.Errorf("%q usa .local, que e reservado pro mDNS; pra teste local use .localhost (ex: trd.localhost)", h)
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
	return fsx.WriteAtomic(path, data, 0o644)
}
