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
	// Stacks sao os servicos ligados na aba Infra (proxy, postgres, mysql,
	// garage). Nenhum sobe sem ser ligado: o Setup so configura.
	Stacks []string `yaml:"stacks,omitempty" json:"stacks,omitempty"`
	Proxy  Proxy    `yaml:"proxy" json:"proxy"`
	Hosts  Hosts    `yaml:"hosts,omitempty" json:"hosts"`
	GitHub GitHub   `yaml:"github,omitempty" json:"github"`
	Backup Backup   `yaml:"backup,omitempty" json:"backup"`
	Notify Notify   `yaml:"notify,omitempty" json:"notify"`
	// Versions e a versao (tag da imagem) escolhida dos bancos e do Garage
	// (postgres, mysql, garage); sem ela, a padrao do cais. O formato de
	// cada uma e conferido no pacote stacks.
	Versions map[string]string `yaml:"versions,omitempty" json:"versions,omitempty"`

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
	// BehindProxy e a migracao com outro proxy na frente (um nginx da VPS
	// nas 80/443, repassando os sites ja migrados): as portas escutam so no
	// 127.0.0.1 (nada aberto na internet) e o Traefik confia no
	// X-Forwarded-For dele, pros apps verem o IP de quem acessa.
	BehindProxy bool `yaml:"behind_proxy,omitempty" json:"behind_proxy,omitempty"`
	// DNS troca a prova do Let's Encrypt: vazio e pela porta 80 (o Let's
	// Encrypt acessa o site); DNSCloudflare e por um registro TXT criado
	// com o token da API da Cloudflare (secret cloudflare_dns_token). Pelo
	// DNS sai certificado pra dominio que a internet nao alcanca (IP do
	// Tailscale) e pra curinga; todo dominio dos apps precisa estar na
	// Cloudflare.
	DNS string `yaml:"dns,omitempty" json:"dns,omitempty"`
	// Wildcards sao os dominios com certificado curinga (dev.exemplo.com.br
	// cobre *.dev.exemplo.com.br): um certificado so pra todos os apps
	// debaixo dele, que ja nascem com HTTPS. Precisa do DNS.
	Wildcards []string `yaml:"wildcards,omitempty" json:"wildcards,omitempty"`
}

// DNSCloudflare e a prova do Let's Encrypt pelo DNS da Cloudflare.
const DNSCloudflare = "cloudflare"

// CloudflareTokenSecret e o secret com o token da API da Cloudflare
// (permissoes DNS: Edit e Zone: Read na zona do dominio: o lego acha o ID
// da zona antes de criar o TXT).
const CloudflareTokenSecret = "cloudflare_dns_token"

// DNSChallenge diz se os certificados saem pela prova no DNS.
func (c Config) DNSChallenge() bool { return c.LetsEncrypt() && c.Proxy.DNS != "" }

// BehindProxyTrustedIPs sao as origens de quem o Traefik aceita o
// X-Forwarded-For com BehindProxy: o loopback e as redes privadas, porque o
// nginx do host chega no container pelo gateway da rede do docker
// (172.16/12 ou 192.168/16, conforme a VPS). Com as portas so no
// 127.0.0.1, de fora ninguem chega no Traefik pra mentir o IP.
const BehindProxyTrustedIPs = "127.0.0.1/32,::1/128,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16"

// Portas padrao do proxy no host.
const (
	DefaultHTTPPort  = 80
	DefaultHTTPSPort = 443
)

// StandardPorts diz se o proxy esta em 80 e 443. Fora delas o Let's Encrypt
// nao emite certificado: ele valida pela porta 80 da internet.
func (p Proxy) StandardPorts() bool {
	return p.HTTPPort == DefaultHTTPPort && p.HTTPSPort == DefaultHTTPSPort
}

// PortsWarning e o aviso sobre portas fora do padrao com Let's Encrypt;
// vazio quando nao ha o que avisar.
func (c Config) PortsWarning() string {
	// pelo DNS o Let's Encrypt nao acessa a VPS: porta nenhuma importa
	if c.Proxy.StandardPorts() || !c.LetsEncrypt() || c.DNSChallenge() {
		return ""
	}
	if c.Proxy.BehindProxy {
		return fmt.Sprintf("o proxy esta atras de outro (portas %d/%d so no 127.0.0.1): o Let's Encrypt valida pela porta 80, "+
			"entao o outro proxy precisa repassar /.well-known/acme-challenge/ dos sites migrados pra 127.0.0.1:%d, "+
			"senao o certificado nao sai.", c.Proxy.HTTPPort, c.Proxy.HTTPSPort, c.Proxy.HTTPPort)
	}
	return fmt.Sprintf("o proxy esta nas portas %d/%d: o Let's Encrypt valida pela porta 80 da internet e nao vai emitir "+
		"certificado (os sites abrem com aviso de certificado). Fora de 80/443 so pra teste ou migracao.", c.Proxy.HTTPPort, c.Proxy.HTTPSPort)
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
	// Provider e o destino dos backups: google (Google Drive, o padrao) ou
	// onedrive (OneDrive pessoal).
	Provider string `yaml:"provider,omitempty" json:"provider,omitempty"`
	// Time e o horario diario (HH:MM, no fuso da VPS); vazio desliga o
	// backup automatico (o "Fazer backup agora" continua valendo).
	Time string `yaml:"time,omitempty" json:"time,omitempty"`
	// RetentionDays apaga da nuvem os backups mais velhos que isso (o mais
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

// Default devolve a configuracao inicial do Setup, sem dominio e e-mail.
func Default() Config {
	return Config{
		Proxy: Proxy{HTTPPort: DefaultHTTPPort, HTTPSPort: DefaultHTTPSPort},
	}
}

// S3Host e o endpoint publico do Garage (API S3).
func (c Config) S3Host() string { return c.Hosts.S3 }

// HasStack diz se a stack esta ligada.
func (c Config) HasStack(name string) bool {
	for _, s := range c.Stacks {
		if s == name {
			return true
		}
	}
	return false
}

// imageTag e o formato de tag do docker (vai direto pro compose)
var imageTag = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

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
		errs = append(errs, errors.New("hosts.s3: o Garage precisa do endereco publico do S3 (ex: s3.exemplo.com.br); preencha no Garage, na aba Infra"))
	}
	// o e-mail so serve pro Let's Encrypt do proxy: obrigatorio com ele
	// ligado, e conferido sempre que preenchido
	if c.Email != "" || (c.LetsEncrypt() && c.HasStack("proxy")) {
		if err := validEmail(c.Email); err != nil {
			if c.Email == "" {
				err = errors.New("obrigatorio com o proxy ligado (avisos do Let's Encrypt); preencha no proxy, na aba Infra")
			}
			errs = append(errs, fmt.Errorf("email: %w", err))
		}
	}
	if c.Hosts.S3 != "" {
		if err := validHost(c.Hosts.S3); err != nil {
			errs = append(errs, fmt.Errorf("hosts.s3: %w", err))
		}
	}
	for name, v := range c.Versions {
		if v != "" && !imageTag.MatchString(v) {
			errs = append(errs, fmt.Errorf("versions.%s: tag invalida %q", name, v))
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
	if c.Proxy.BehindProxy && (c.Proxy.HTTPPort == DefaultHTTPPort || c.Proxy.HTTPSPort == DefaultHTTPSPort) {
		errs = append(errs, errors.New("proxy: atras de outro proxy, use portas fora de 80/443 (o outro proxy esta nelas); "+
			"terminada a migracao, desligue o behind_proxy e volte pra 80/443"))
	}
	if d := c.Proxy.DNS; d != "" && d != DNSCloudflare {
		errs = append(errs, fmt.Errorf("proxy.dns: %q invalido (vazio, pela porta 80, ou %s)", d, DNSCloudflare))
	}
	if c.Proxy.DNS != "" && !c.LetsEncrypt() {
		errs = append(errs, errors.New("proxy.dns: a prova pelo DNS e do Let's Encrypt; com a CA local (teste) nao vale"))
	}
	for _, w := range c.Proxy.Wildcards {
		if err := validHost(w); err != nil || !strings.Contains(w, ".") {
			errs = append(errs, fmt.Errorf("proxy.wildcards: %q invalido: o dominio sem o *. (ex: dev.exemplo.com.br cobre *.dev.exemplo.com.br)", w))
		}
	}
	if len(c.Proxy.Wildcards) > 0 && !c.DNSChallenge() {
		errs = append(errs, errors.New("proxy.wildcards: certificado curinga so sai pela prova no DNS (proxy.dns: cloudflare)"))
	}
	if b := c.Backup; b.Time != "" {
		if _, _, ok := parseHHMM(b.Time); !ok {
			errs = append(errs, fmt.Errorf("backup.time: %q invalido (use HH:MM, ex: 03:00)", b.Time))
		}
	}
	if p := c.Backup.Provider; p != "" && p != "google" && p != "onedrive" {
		errs = append(errs, fmt.Errorf("backup.provider: %q invalido (google ou onedrive)", p))
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
