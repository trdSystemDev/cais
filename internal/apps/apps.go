package apps

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/trdSystemDev/cais/internal/compose"
	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/docker"
	"github.com/trdSystemDev/cais/internal/fsx"
	"github.com/trdSystemDev/cais/internal/stacks"
)

// Arquivos na pasta de cada app. app.yaml e o override sao do cais; o
// compose vem do repositorio do app (pelo deploy) e passa pela validacao.
const (
	MetaFile     = "app.yaml"
	ComposeFile  = "docker-compose.yml"
	OverrideFile = "cais.override.yml"
	prevSuffix   = ".prev"
)

// Networks sao as redes das stacks em que um app pode entrar. As redes dos
// proxies do docker.sock (Traefik e Woodpecker) nunca entram aqui.
var Networks = []string{"proxy_network", "postgres_network", "mysql_network", "garage_network"}

// ErrNotFound indica que o app nao existe.
var ErrNotFound = errors.New("app nao encontrado")

// Route publica um service do app num dominio pelo Traefik.
type Route struct {
	Host string `yaml:"host" json:"host"`
	// Path e um prefixo opcional (ex: /api). Vazio responde o dominio todo.
	Path    string `yaml:"path,omitempty" json:"path,omitempty"`
	Service string `yaml:"service" json:"service"`
	Port    int    `yaml:"port" json:"port"`
}

func (r Route) String() string {
	return fmt.Sprintf("%s%s -> %s:%d", r.Host, r.Path, r.Service, r.Port)
}

// Deploy e o registro do ultimo deploy.
type Deploy struct {
	At     time.Time `yaml:"at" json:"at"`
	Images []string  `yaml:"images,omitempty" json:"images,omitempty"`
	// Error e o motivo da falha; vazio quando o deploy subiu.
	Error string `yaml:"error,omitempty" json:"error,omitempty"`
	// RolledBack indica que a falha voltou a versao anterior no ar.
	RolledBack bool `yaml:"rolled_back,omitempty" json:"rolled_back,omitempty"`
}

// App e o que o cais guarda de um app (app.yaml).
type App struct {
	Name   string  `yaml:"-" json:"name"`
	Routes []Route `yaml:"routes,omitempty" json:"routes,omitempty"`
	Last   *Deploy `yaml:"last_deploy,omitempty" json:"last_deploy,omitempty"`
}

// Docker sao as operacoes de projeto compose que os apps usam.
type Docker interface {
	Up(ctx context.Context, p docker.Project) error
	PS(ctx context.Context, p docker.Project) ([]docker.Container, error)
	Restart(ctx context.Context, p docker.Project, service string) error
	Stop(ctx context.Context, p docker.Project) error
}

// Manager cuida dos apps em Root (/var/lib/cais/apps). Nao tem trava:
// quem chama (o core) garante um deploy por app e uma troca de rotas por vez.
type Manager struct {
	Root   string
	Docker Docker
	Now    func() time.Time
}

var appName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// CheckName recusa nomes invalidos e os das stacks: o nome do app vira o
// projeto compose, e "-p registry" assumiria a stack do registry.
func CheckName(name string) error {
	if !appName.MatchString(name) {
		return fmt.Errorf("nome de app invalido %q (minusculas, numeros e -, ate 40; igual ao nome do repositorio no GitHub)", name)
	}
	if _, ok := stacks.Get(name); ok || name == "cais" {
		return fmt.Errorf("%q e nome reservado do cais", name)
	}
	return nil
}

func (m Manager) dir(name string) string { return filepath.Join(m.Root, name) }

// Project e o projeto compose do app, com o override quando ha rotas.
func (m Manager) Project(name string) docker.Project {
	p := docker.Project{Name: name, Files: []string{filepath.Join(m.dir(name), ComposeFile)}}
	if _, err := os.Stat(filepath.Join(m.dir(name), OverrideFile)); err == nil {
		p.Files = append(p.Files, filepath.Join(m.dir(name), OverrideFile))
	}
	return p
}

// Create cria a pasta do app (com secrets/) e o app.yaml vazio.
func (m Manager) Create(name string) (App, error) {
	if err := CheckName(name); err != nil {
		return App{}, err
	}
	if _, err := os.Stat(m.dir(name)); err == nil {
		return App{}, fmt.Errorf("app %q ja existe", name)
	}
	if err := os.MkdirAll(filepath.Join(m.dir(name), "secrets"), 0o700); err != nil {
		return App{}, err
	}
	a := App{Name: name}
	return a, m.save(a)
}

// Get le o app.yaml.
func (m Manager) Get(name string) (App, error) {
	if err := CheckName(name); err != nil {
		return App{}, err
	}
	data, err := os.ReadFile(filepath.Join(m.dir(name), MetaFile))
	if errors.Is(err, os.ErrNotExist) {
		return App{}, fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	if err != nil {
		return App{}, err
	}
	var a App
	if err := yaml.Unmarshal(data, &a); err != nil {
		return App{}, fmt.Errorf("%s/%s: %w", name, MetaFile, err)
	}
	a.Name = name
	return a, nil
}

// List devolve todos os apps em ordem alfabetica.
func (m Manager) List() ([]App, error) {
	entries, err := os.ReadDir(m.Root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []App
	for _, e := range entries {
		if !e.IsDir() || CheckName(e.Name()) != nil {
			continue
		}
		a, err := m.Get(e.Name())
		if errors.Is(err, ErrNotFound) {
			continue // pasta sem app.yaml nao e app
		}
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func (m Manager) save(a App) error {
	data, err := yaml.Marshal(a)
	if err != nil {
		return err
	}
	data = append([]byte("# gerado pelo cais; mude pelo cais (rotas, deploy)\n"), data...)
	return fsx.WriteAtomic(filepath.Join(m.dir(a.Name), MetaFile), data, 0o644)
}

var (
	routePath   = regexp.MustCompile(`^/[A-Za-z0-9._~/-]*$`)
	serviceName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)
)

// CheckRoutes confere as rotas de um app. reserved sao os dominios das
// stacks; others sao os outros apps. Um dominio pertence a um app so:
// senao um app poderia tomar /admin do dominio de outro (o Traefik da
// prioridade ao prefixo mais longo).
func CheckRoutes(app string, routes []Route, reserved []string, others []App) error {
	var errs []error
	owner := map[string]string{}
	for _, o := range others {
		if o.Name == app {
			continue
		}
		for _, r := range o.Routes {
			owner[r.Host] = o.Name
		}
	}
	seen := map[string]bool{}
	for i, r := range routes {
		where := fmt.Sprintf("rota %d (%s)", i+1, r)
		if err := config.ValidHost(r.Host); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", where, err))
		}
		if slices.Contains(reserved, r.Host) {
			errs = append(errs, fmt.Errorf("%s: %s e de uma stack do cais", where, r.Host))
		}
		if o, ok := owner[r.Host]; ok {
			errs = append(errs, fmt.Errorf("%s: %s ja e do app %s", where, r.Host, o))
		}
		if r.Path != "" && !routePath.MatchString(r.Path) {
			errs = append(errs, fmt.Errorf("%s: caminho precisa comecar com / (ex: /api)", where))
		}
		if !serviceName.MatchString(r.Service) {
			errs = append(errs, fmt.Errorf("%s: service invalido", where))
		}
		if r.Port < 1 || r.Port > 65535 {
			errs = append(errs, fmt.Errorf("%s: porta invalida", where))
		}
		key := r.Host + r.Path
		if seen[key] {
			errs = append(errs, fmt.Errorf("%s: %s%s repetido", where, r.Host, r.Path))
		}
		seen[key] = true
	}
	return errors.Join(errs...)
}

// SetRoutes troca as rotas do app. Se o app ja esta no ar, regrava o
// override e sobe de novo (so os labels mudam; o Traefik reage sozinho).
func (m Manager) SetRoutes(ctx context.Context, name string, routes []Route, reserved []string) error {
	a, err := m.Get(name)
	if err != nil {
		return err
	}
	others, err := m.List()
	if err != nil {
		return err
	}
	if err := CheckRoutes(name, routes, reserved, others); err != nil {
		return err
	}

	composeData, err := os.ReadFile(filepath.Join(m.dir(name), ComposeFile))
	deployed := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if deployed {
		if err := checkRouteServices(routes, composeData); err != nil {
			return err
		}
	}

	a.Routes = routes
	if err := m.save(a); err != nil {
		return err
	}
	if !deployed {
		return nil // o override sai no primeiro deploy
	}
	if err := m.writeOverride(a, composeData); err != nil {
		return err
	}
	return m.Docker.Up(ctx, m.Project(name))
}

// checkRouteServices garante que toda rota aponta pra um service que existe.
func checkRouteServices(routes []Route, composeData []byte) error {
	services, err := compose.Services(composeData)
	if err != nil {
		return err
	}
	var errs []error
	for _, r := range routes {
		if !slices.ContainsFunc(services, func(s compose.Service) bool { return s.Name == r.Service }) {
			errs = append(errs, fmt.Errorf("rota %s: o compose nao tem o service %q", r, r.Service))
		}
	}
	return errors.Join(errs...)
}

// writeOverride grava (ou apaga, sem rotas) o cais.override.yml.
func (m Manager) writeOverride(a App, composeData []byte) error {
	file := filepath.Join(m.dir(a.Name), OverrideFile)
	if len(a.Routes) == 0 {
		if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	services, err := compose.Services(composeData)
	if err != nil {
		return err
	}
	data, err := Override(a.Name, a.Routes, services)
	if err != nil {
		return err
	}
	return fsx.WriteAtomic(file, data, 0o644)
}

// Status lista os containers do app. Antes do primeiro deploy, nenhum.
func (m Manager) Status(ctx context.Context, name string) ([]docker.Container, error) {
	if _, err := m.Get(name); err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(m.dir(name), ComposeFile)); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return m.Docker.PS(ctx, m.Project(name))
}

// Restart reinicia um service do app (ou todos, com service vazio).
func (m Manager) Restart(ctx context.Context, name, service string) error {
	if err := m.requireDeployed(name); err != nil {
		return err
	}
	return m.Docker.Restart(ctx, m.Project(name), service)
}

// Stop para o app sem apagar nada; o proximo deploy (ou restart) volta.
func (m Manager) Stop(ctx context.Context, name string) error {
	if err := m.requireDeployed(name); err != nil {
		return err
	}
	return m.Docker.Stop(ctx, m.Project(name))
}

func (m Manager) requireDeployed(name string) error {
	if _, err := m.Get(name); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(m.dir(name), ComposeFile)); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("app %s ainda nao teve deploy", name)
	}
	return nil
}

// sanitize deixa um nome de service pronto pra nome de router do Traefik.
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// Override gera o cais.override.yml: labels do Traefik e a proxy_network
// nos services com rota. Os nomes de router e service do Traefik levam o
// nome do app na frente, entao nunca colidem com os de outro app ou stack.
func Override(app string, routes []Route, services []compose.Service) ([]byte, error) {
	type svcOverride struct {
		Labels   map[string]string `yaml:"labels"`
		Networks []string          `yaml:"networks"`
	}
	out := map[string]*svcOverride{}

	for i, r := range routes {
		idx := slices.IndexFunc(services, func(s compose.Service) bool { return s.Name == r.Service })
		if idx < 0 {
			return nil, fmt.Errorf("rota %s: o compose nao tem o service %q", r, r.Service)
		}
		so := out[r.Service]
		if so == nil {
			so = &svcOverride{Labels: map[string]string{
				"traefik.enable":         "true",
				"traefik.docker.network": "proxy_network",
			}}
			// sem networks no compose o service so esta na default
			// implicita; o override somaria so a proxy_network e tiraria
			// ele da default (e dos outros services do app)
			if len(services[idx].Networks) == 0 {
				so.Networks = append(so.Networks, "default")
			}
			so.Networks = append(so.Networks, "proxy_network")
			out[r.Service] = so
		}

		router := fmt.Sprintf("%s-%s-%d", app, sanitize(r.Service), i+1)
		backend := fmt.Sprintf("%s-%s-%d", app, sanitize(r.Service), r.Port)
		rule := fmt.Sprintf("Host(`%s`)", r.Host)
		if r.Path != "" && r.Path != "/" {
			rule += fmt.Sprintf(" && PathPrefix(`%s`)", r.Path)
		}
		so.Labels["traefik.http.routers."+router+".rule"] = rule
		so.Labels["traefik.http.routers."+router+".entrypoints"] = "websecure"
		so.Labels["traefik.http.routers."+router+".service"] = backend
		so.Labels["traefik.http.services."+backend+".loadbalancer.server.port"] = fmt.Sprint(r.Port)
	}

	doc := map[string]any{
		"services": out,
		"networks": map[string]any{"proxy_network": map[string]any{"external": true}},
	}
	data, err := yaml.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return append([]byte("# gerado pelo cais a partir das rotas do app -- nao edite\n"), data...), nil
}

// imagesOf lista as imagens do compose (uma por service, sem repetir).
func imagesOf(services []compose.Service) []string {
	var out []string
	for _, s := range services {
		if s.Image != "" && !slices.Contains(out, s.Image) {
			out = append(out, s.Image)
		}
	}
	sort.Strings(out)
	return out
}

// DeployResult e o que um deploy fez.
type DeployResult struct {
	App    string   `json:"app"`
	Images []string `json:"images"`
}

// Deploy valida o compose novo, grava junto com o override das rotas e
// sobe o app esperando os healthchecks. Se o up falhar e havia uma versao
// no ar, ela volta (arquivos e containers) e o erro diz isso. O registro
// do deploy (ok ou falha) fica no app.yaml.
func (m Manager) Deploy(ctx context.Context, name string, composeData []byte) (DeployResult, error) {
	a, err := m.Get(name)
	if err != nil {
		return DeployResult{}, err
	}
	if err := compose.Validate(composeData, compose.Policy{App: name, Networks: Networks}); err != nil {
		return DeployResult{}, fmt.Errorf("compose recusado:\n%w", err)
	}
	if err := checkRouteServices(a.Routes, composeData); err != nil {
		return DeployResult{}, err
	}
	services, err := compose.Services(composeData)
	if err != nil {
		return DeployResult{}, err
	}
	res := DeployResult{App: name, Images: imagesOf(services)}

	dir := m.dir(name)
	composeFile := filepath.Join(dir, ComposeFile)
	overrideFile := filepath.Join(dir, OverrideFile)

	// guarda a versao atual pra voltar se o up falhar
	hadPrev, err := backup(composeFile, overrideFile)
	if err != nil {
		return res, err
	}
	if err := fsx.WriteAtomic(composeFile, composeData, 0o644); err != nil {
		return res, err
	}
	if err := m.writeOverride(a, composeData); err != nil {
		return res, err
	}

	upErr := m.Docker.Up(ctx, m.Project(name))
	last := &Deploy{At: m.Now(), Images: res.Images}
	if upErr != nil {
		last.Error = upErr.Error()
		if hadPrev {
			if rerr := m.restore(ctx, name, composeFile, overrideFile); rerr != nil {
				upErr = fmt.Errorf("%w\n\ne a volta pra versao anterior tambem falhou: %v", upErr, rerr)
			} else {
				last.RolledBack = true
				upErr = fmt.Errorf("%w\n\na versao anterior foi restaurada e continua no ar", upErr)
			}
		}
	}
	a.Last = last
	if err := m.save(a); err != nil && upErr == nil {
		return res, err
	}
	return res, upErr
}

// backup copia compose e override atuais pra .prev. Devolve false quando
// ainda nao havia deploy (nada pra restaurar).
func backup(composeFile, overrideFile string) (bool, error) {
	cur, err := os.ReadFile(composeFile)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := fsx.WriteAtomic(composeFile+prevSuffix, cur, 0o644); err != nil {
		return false, err
	}
	ov, err := os.ReadFile(overrideFile)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.Remove(overrideFile + prevSuffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	case err != nil:
		return false, err
	default:
		if err := fsx.WriteAtomic(overrideFile+prevSuffix, ov, 0o644); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (m Manager) restore(ctx context.Context, name, composeFile, overrideFile string) error {
	prev, err := os.ReadFile(composeFile + prevSuffix)
	if err != nil {
		return err
	}
	if err := fsx.WriteAtomic(composeFile, prev, 0o644); err != nil {
		return err
	}
	ov, err := os.ReadFile(overrideFile + prevSuffix)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.Remove(overrideFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	case err != nil:
		return err
	default:
		if err := fsx.WriteAtomic(overrideFile, ov, 0o644); err != nil {
			return err
		}
	}
	return m.Docker.Up(ctx, m.Project(name))
}
