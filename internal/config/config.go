package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/trdSystemDev/cais/internal/fsx"
)

// ErrNotFound indica que ainda nao existe config.yaml: a VPS nao passou
// pelo Setup.
var ErrNotFound = errors.New("config.yaml nao encontrado (rode o Setup)")

// Config e o conteudo de /etc/cais/config.yaml. So guarda o que um humano
// decide; secrets nunca entram aqui (ficam em SecretsDir).
type Config struct {
	// Nao ha dominio da VPS: cada app usa o proprio dominio (labels do
	// compose), e o certificado do Let's Encrypt sai pra esse nome.

	// Email recebe os avisos do Let's Encrypt.
	Email string `yaml:"email" json:"email"`
	// TLS e de onde vem o certificado dos sites: TLSLetsEncrypt (padrao,
	// precisa de DNS publico) ou TLSSelfSigned (teste local, ex: dominio
	// .localhost, que nao tem como ter certificado publico).
	TLS string `yaml:"tls,omitempty" json:"tls,omitempty"`
	// TestDomain e o dominio do modo de teste (TLSSelfSigned), obrigatorio
	// nele: a CA local so assina nomes dele (ex: trd.localhost, e os apps
	// em <app>.trd.localhost). Fora do modo de teste nao vale.
	TestDomain string `yaml:"test_domain,omitempty" json:"test_domain,omitempty"`
	// Stacks sao os servicos opcionais ligados (postgres, mysql, garage).
	// As stacks fixas (CoreStacks) sobem sempre e nao precisam estar aqui.
	Stacks []string `yaml:"stacks,omitempty" json:"stacks,omitempty"`
	Proxy  Proxy    `yaml:"proxy" json:"proxy"`
	Hosts  Hosts    `yaml:"hosts,omitempty" json:"hosts"`
	GitHub GitHub   `yaml:"github,omitempty" json:"github"`
	Backup Backup   `yaml:"backup,omitempty" json:"backup"`
	Notify Notify   `yaml:"notify,omitempty" json:"notify"`

	// campos de versoes antigas (CI e registry saíram): aceitos na leitura
	// pra config velha continuar valendo, e descartados ao gravar
	OldWoodpecker any `yaml:"woodpecker,omitempty" json:"-"`
	// OldDomain e o dominio base, que saiu: na leitura vira TestDomain (modo
	// de teste) e Hosts.S3 (s3.<dominio>, com o Garage ligado)
	OldDomain string `yaml:"domain,omitempty" json:"-"`
}

// Proxy sao as portas do host publicadas pelo Traefik. Fora de 80/443 so
// faz sentido numa migracao, com outro proxy ainda segurando essas portas.
type Proxy struct {
	HTTPPort  int `yaml:"http_port" json:"http_port"`
	HTTPSPort int `yaml:"https_port" json:"https_port"`
}

// Hosts troca o nome publico de uma stack. Vazio usa o padrao.
type Hosts struct {
	// S3 e o endpoint S3 do Garage (padrao s3.<domain>).
	S3 string `yaml:"s3,omitempty" json:"s3,omitempty"`

	OldCI       string `yaml:"ci,omitempty" json:"-"`
	OldRegistry string `yaml:"registry,omitempty" json:"-"`
}

// GitHub e o GitHub App do cais, que baixa o codigo dos repositorios
// privados no deploy. A chave privada fica no secret github_app_key.
type GitHub struct {
	AppID int64 `yaml:"app_id,omitempty" json:"app_id,omitempty"`
}

// Backup e quando e pra quem sai o backup diario (bancos e estado do cais,
// pro Google Drive). O login no Drive e o OAuth client ficam nos secrets.
type Backup struct {
	// Time e o horario diario (HH:MM, no fuso da VPS); vazio desliga o
	// backup automatico (o "Fazer backup agora" continua valendo).
	Time string `yaml:"time,omitempty" json:"time,omitempty"`
	// RetentionDays apaga do Drive os backups mais velhos que isso (o mais
	// novo fica sempre). Zero usa DefaultRetentionDays.
	RetentionDays int `yaml:"retention_days,omitempty" json:"retention_days,omitempty"`
	// Recipient e a chave publica age (age1...) que criptografa os
	// backups. A privada fica fora da VPS: sem ela nenhum backup abre.
	Recipient string `yaml:"recipient,omitempty" json:"recipient,omitempty"`
}

// Notify e pra quem vao os e-mails de falha (deploy automatico e backup),
// enviados pela API da Brevo. A chave da API fica no secret brevo_api_key.
type Notify struct {
	// From e o remetente, um e-mail ja validado na conta da Brevo.
	From string `yaml:"from,omitempty" json:"from,omitempty"`
	// To sao os destinatarios; vazio desliga os avisos.
	To []string `yaml:"to,omitempty" json:"to,omitempty"`
}

// On diz se os avisos por e-mail estao ligados.
func (n Notify) On() bool { return len(n.To) > 0 }

// DefaultRetentionDays e a retencao quando a config nao diz.
const DefaultRetentionDays = 30

// Retention e a retencao em dias.
func (b Backup) Retention() int {
	if b.RetentionDays > 0 {
		return b.RetentionDays
	}
	return DefaultRetentionDays
}

// At devolve o horario do backup no dia de t (fuso de t); ok falso sem
// horario.
func (b Backup) At(t time.Time) (time.Time, bool) {
	h, m, ok := parseHHMM(b.Time)
	if !ok {
		return time.Time{}, false
	}
	return time.Date(t.Year(), t.Month(), t.Day(), h, m, 0, 0, t.Location()), true
}

var hhmm = regexp.MustCompile(`^([01][0-9]|2[0-3]):([0-5][0-9])$`)

func parseHHMM(s string) (h, m int, ok bool) {
	g := hhmm.FindStringSubmatch(s)
	if g == nil {
		return 0, 0, false
	}
	h, _ = strconv.Atoi(g[1])
	m, _ = strconv.Atoi(g[2])
	return h, m, true
}

// Modos de TLS.
const (
	TLSLetsEncrypt = "letsencrypt"
	TLSSelfSigned  = "selfsigned"
)

// LetsEncrypt diz se os certificados vem do Let's Encrypt.
func (c Config) LetsEncrypt() bool { return c.TLS == "" || c.TLS == TLSLetsEncrypt }

// CoreStacks sobem sempre: sem proxy nao da pra publicar app nenhum. O
// resto (bancos, storage) e ligado conforme o uso.
var CoreStacks = []string{"proxy"}

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

// S3Host e o endpoint publico do Garage (API S3).
func (c Config) S3Host() string { return c.Hosts.S3 }

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

var hostLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Validate confere os campos gerais. As regras de cada stack (dependencias)
// ficam no pacote stacks. Os valores vao direto pros compose, entao tudo e
// restrito a caracteres seguros.
func (c Config) Validate() error {
	var errs []error
	if c.TestDomain != "" {
		if err := validHost(c.TestDomain); err != nil {
			errs = append(errs, fmt.Errorf("test_domain: %w", err))
		}
	} else if c.TLS == TLSSelfSigned {
		errs = append(errs, errors.New("test_domain: o modo de teste (tls selfsigned) precisa do dominio do teste (ex: trd.localhost): a CA local so assina nomes dele"))
	}
	if c.HasStack("garage") && c.Hosts.S3 == "" {
		errs = append(errs, errors.New("hosts.s3: o Garage precisa do endereco publico do S3 (ex: s3.exemplo.com.br)"))
	}
	if err := validEmail(c.Email); err != nil {
		errs = append(errs, fmt.Errorf("email: %w", err))
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
	if b := c.Backup; b.Time != "" {
		if _, _, ok := parseHHMM(b.Time); !ok {
			errs = append(errs, fmt.Errorf("backup.time: %q invalido (use HH:MM, ex: 03:00)", b.Time))
		}
	}
	if c.Backup.RetentionDays < 0 || c.Backup.RetentionDays > 3650 {
		errs = append(errs, fmt.Errorf("backup.retention_days: %d invalido (1 a 3650)", c.Backup.RetentionDays))
	}
	if r := c.Backup.Recipient; r != "" && !strings.HasPrefix(r, "age1") {
		errs = append(errs, fmt.Errorf("backup.recipient: %q nao e uma chave publica age (age1...)", r))
	}
	if n := c.Notify; n.On() || n.From != "" {
		if err := validEmail(n.From); err != nil {
			errs = append(errs, fmt.Errorf("notify.from: %w", err))
		}
		for _, to := range n.To {
			if err := validEmail(to); err != nil {
				errs = append(errs, fmt.Errorf("notify.to: %w", err))
			}
		}
	}
	if c.GitHub.AppID < 0 {
		errs = append(errs, fmt.Errorf("github.app_id: id invalido %d", c.GitHub.AppID))
	}
	return errors.Join(errs...)
}

// ValidHost confere um nome de dominio.
func ValidHost(h string) error { return validHost(h) }

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
	// stacks de versoes antigas (o CI e o registry sairam)
	c.Stacks = slices.DeleteFunc(c.Stacks, func(s string) bool { return s == "registry" || s == "woodpecker" })
	// o dominio base saiu: o que ele fazia passa pros campos de agora
	if d := c.OldDomain; d != "" {
		if c.TLS == TLSSelfSigned && c.TestDomain == "" {
			c.TestDomain = d
		}
		if c.HasStack("garage") && c.Hosts.S3 == "" {
			c.Hosts.S3 = "s3." + d
		}
		c.OldDomain = ""
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
	// campos de versoes antigas nao voltam pro arquivo
	c.OldWoodpecker, c.Hosts.OldCI, c.Hosts.OldRegistry, c.OldDomain = nil, "", "", ""
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
