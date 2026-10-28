package apps

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/trdSystemDev/cais/internal/detect"
	"github.com/trdSystemDev/cais/internal/secrets"
)

// Spec descreve o app como o cais publica: o compose sai daqui a cada
// deploy, com a tag do commit. O repositorio do app so precisa do
// Dockerfile; o CI faz o build e pede o deploy da tag.
type Spec struct {
	// Image e o repositorio da imagem, sem tag (registry.exemplo.com.br/app).
	Image string `yaml:"image" json:"image"`
	// Port e a porta HTTP do service web (a das rotas).
	Port int `yaml:"port,omitempty" json:"port,omitempty"`
	// Volumes sao pastas do container que sobrevivem ao deploy (/app/uploads).
	Volumes []string `yaml:"volumes,omitempty" json:"volumes,omitempty"`
	// Env sao variaveis comuns. Nada com cara de segredo: segredo e Secrets.
	Env map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	// Secrets sao arquivos de secrets/ do app montados em /run/secrets/<nome>.
	Secrets []string `yaml:"secrets,omitempty" json:"secrets,omitempty"`
	// Workers sao processos extras com a mesma imagem: nome -> comando.
	Workers map[string]string `yaml:"workers,omitempty" json:"workers,omitempty"`
	// Command troca o comando do Dockerfile no service web (vazio mantem).
	Command []string `yaml:"command,omitempty" json:"command,omitempty"`
	// Build e como o CI constroi a imagem (vai pro .woodpecker.yml do app).
	Build Build `yaml:"build,omitempty" json:"build"`
}

// Build e como o CI constroi a imagem, a partir da raiz do repositorio.
type Build struct {
	// Context e a pasta de build; vazio e a raiz.
	Context string `yaml:"context,omitempty" json:"context,omitempty"`
	// Dockerfile e o caminho a partir da raiz; vazio e o Dockerfile da raiz.
	Dockerfile string `yaml:"dockerfile,omitempty" json:"dockerfile,omitempty"`
	// Args sao build args. So valores publicos: ficam gravados na imagem
	// (docker history), entao segredo aqui e recusado.
	Args map[string]string `yaml:"args,omitempty" json:"args,omitempty"`
}

var (
	repoPath = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
	// o plugin do kaniko junta os args por virgula e passa sem aspas pro
	// shell: espaco, virgula, aspas ou glob (* ? [) quebrariam o build sem avisar
	buildArgValue = regexp.MustCompile(`^[A-Za-z0-9._:/@+=%~#-]*$`)
)

func safeRepoPath(p string) bool {
	if p == "" || p == "." {
		return true
	}
	c := path.Clean(p)
	return c == p && repoPath.MatchString(p) && !strings.HasPrefix(p, "/") && c != ".." && !strings.HasPrefix(c, "../")
}

// CheckBuild confere o build: caminhos dentro do repositorio e build args
// publicos e sem caracteres que o plugin do kaniko nao aguenta.
func CheckBuild(b Build) error {
	var errs []error
	if !safeRepoPath(b.Context) {
		errs = append(errs, fmt.Errorf("pasta de build invalida %q (relativa a raiz do repositorio)", b.Context))
	}
	if !safeRepoPath(b.Dockerfile) {
		errs = append(errs, fmt.Errorf("Dockerfile invalido %q (caminho a partir da raiz do repositorio)", b.Dockerfile))
	}
	for k, v := range b.Args {
		switch {
		case !envName.MatchString(k):
			errs = append(errs, fmt.Errorf("build arg invalido %q", k))
		case detect.LooksSecret(k):
			errs = append(errs, fmt.Errorf("build arg %s parece segredo: fica gravado na imagem (docker history); passe em execucao, por arquivo", k))
		case !buildArgValue.MatchString(v):
			errs = append(errs, fmt.Errorf("build arg %s: o valor nao pode ter espaco, virgula, aspas nem * ? [ (o build do CI quebraria)", k))
		}
	}
	return errors.Join(errs...)
}

// WebService e o nome do service principal no compose gerado.
const WebService = "web"

var (
	imageRepo = regexp.MustCompile(`^[a-z0-9]+([._-][a-z0-9]+)*(:[0-9]+)?(/[a-z0-9]+([._-][a-z0-9]+)*)*$`)
	imageTag  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
)

// CheckSpec confere a spec. Variavel com cara de segredo e recusada: o
// valor apareceria no docker inspect. A excecao e X_FILE apontando pra um
// arquivo em /run/secrets, que e a forma certa de passar segredo.
func CheckSpec(s Spec) error {
	var errs []error
	if !imageRepo.MatchString(s.Image) {
		errs = append(errs, fmt.Errorf("imagem invalida %q (sem tag: registry.exemplo.com.br/app)", s.Image))
	}
	if s.Port < 0 || s.Port > 65535 {
		errs = append(errs, fmt.Errorf("porta invalida %d", s.Port))
	}
	for _, v := range s.Volumes {
		if !strings.HasPrefix(v, "/") || strings.ContainsAny(v, ": \t\n") || path.Clean(v) != v || v == "/" {
			errs = append(errs, fmt.Errorf("volume %q: precisa ser uma pasta absoluta do container (ex: /app/uploads)", v))
		}
	}
	for k, v := range s.Env {
		switch {
		case !envName.MatchString(k):
			errs = append(errs, fmt.Errorf("variavel invalida %q", k))
		case strings.HasSuffix(k, "_FILE") && strings.HasPrefix(v, "/run/secrets/"):
		case detect.LooksSecret(k):
			errs = append(errs, fmt.Errorf("%s parece segredo: nao vai em variavel (aparece no docker inspect); use um secret do app e %s_FILE=/run/secrets/<nome>", k, k))
		case strings.ContainsAny(v, "\n\r"):
			errs = append(errs, fmt.Errorf("%s: valor com quebra de linha", k))
		}
	}
	for _, n := range s.Secrets {
		if err := secrets.CheckName(n); err != nil {
			errs = append(errs, err)
		}
		if strings.HasPrefix(n, "cais_") {
			errs = append(errs, fmt.Errorf("secret %q: o prefixo cais_ e reservado", n))
		}
	}
	if err := CheckBuild(s.Build); err != nil {
		errs = append(errs, err)
	}
	for _, c := range s.Command {
		if c == "" || strings.ContainsAny(c, "\x00\n") {
			errs = append(errs, fmt.Errorf("comando invalido %q", strings.Join(s.Command, " ")))
			break
		}
	}
	for name, cmd := range s.Workers {
		if name == WebService || !serviceName.MatchString(name) {
			errs = append(errs, fmt.Errorf("worker com nome invalido %q", name))
		}
		if strings.TrimSpace(cmd) == "" {
			errs = append(errs, fmt.Errorf("worker %s sem comando", name))
		}
	}
	return errors.Join(errs...)
}

// volumeName da um nome de volume a partir da pasta do container.
func volumeName(dir string) string {
	return "data" + sanitize(strings.ReplaceAll(dir, "/", "-"))
}

// Render gera o compose do app a partir da spec, com a imagem na tag dada.
// O resultado ainda passa pela validacao no Deploy, como qualquer compose.
// O healthcheck vem da propria imagem (HEALTHCHECK do Dockerfile).
func Render(s Spec, tag string) ([]byte, error) {
	if err := CheckSpec(s); err != nil {
		return nil, err
	}
	if !imageTag.MatchString(tag) {
		return nil, fmt.Errorf("tag invalida %q", tag)
	}

	type logging struct {
		Driver  string            `yaml:"driver"`
		Options map[string]string `yaml:"options"`
	}
	type service struct {
		Image       string            `yaml:"image"`
		Command     any               `yaml:"command,omitempty"`
		Restart     string            `yaml:"restart"`
		Environment map[string]string `yaml:"environment,omitempty"`
		Secrets     []string          `yaml:"secrets,omitempty"`
		Volumes     []string          `yaml:"volumes,omitempty"`
		Logging     logging           `yaml:"logging"`
	}
	// $ e interpolacao pro compose: o valor literal vai escapado ($$)
	esc := func(v string) string { return strings.ReplaceAll(v, "$", "$$") }
	env := map[string]string{}
	for k, v := range s.Env {
		env[k] = esc(v)
	}
	if len(env) == 0 {
		env = nil
	}
	base := service{Image: s.Image + ":" + tag, Restart: "unless-stopped", Environment: env, Secrets: s.Secrets,
		// sem limite, o log de um app enche o disco da VPS
		Logging: logging{Driver: "json-file", Options: map[string]string{"max-size": "20m", "max-file": "5"}}}
	vols := map[string]any{}
	for _, v := range s.Volumes {
		base.Volumes = append(base.Volumes, volumeName(v)+":"+v)
		vols[volumeName(v)] = map[string]any{}
	}
	web := base
	if len(s.Command) > 0 {
		cmd := make([]string, len(s.Command))
		for i, c := range s.Command {
			cmd[i] = esc(c)
		}
		web.Command = cmd
	}
	services := map[string]service{WebService: web}
	for name, cmd := range s.Workers {
		w := base
		w.Command = esc(cmd)
		services[name] = w
	}

	doc := map[string]any{"services": services}
	if len(vols) > 0 {
		doc["volumes"] = vols
	}
	if len(s.Secrets) > 0 {
		sec := map[string]any{}
		for _, n := range s.Secrets {
			sec[n] = map[string]any{"file": "./secrets/" + n}
		}
		doc["secrets"] = sec
	}
	data, err := yaml.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return append([]byte("# gerado pelo cais a partir da spec do app -- nao edite\n"), data...), nil
}

// SetRepo grava o repositorio do GitHub de onde o app veio.
func (m Manager) SetRepo(name, repo string) error {
	a, err := m.Get(name)
	if err != nil {
		return err
	}
	a.Repo = repo
	return m.save(a)
}

// SetSpec grava a spec do app. Nao faz deploy: a proxima tag usa a spec nova.
func (m Manager) SetSpec(name string, s Spec) error {
	a, err := m.Get(name)
	if err != nil {
		return err
	}
	if err := CheckSpec(s); err != nil {
		return err
	}
	a.Spec = &s
	return m.save(a)
}

// DeployTag gera o compose da spec com a tag e faz o deploy. E o que o CI
// chama depois do build: ele so escolhe a tag, nunca a configuracao.
func (m Manager) DeployTag(ctx context.Context, name, tag string) (DeployResult, error) {
	a, err := m.Get(name)
	if err != nil {
		return DeployResult{}, err
	}
	if a.Spec == nil {
		return DeployResult{}, fmt.Errorf("app %s sem spec: configure pelo import (cais app init) antes do deploy por tag", name)
	}
	if missing := m.missingSecrets(name, a.Spec.Secrets); len(missing) > 0 {
		return DeployResult{}, fmt.Errorf("faltam secrets do app: %s (informe com: cais app secret %s <nome>)", strings.Join(missing, ", "), name)
	}
	data, err := Render(*a.Spec, tag)
	if err != nil {
		return DeployResult{}, err
	}
	res, err := m.Deploy(ctx, name, data)
	if err != nil {
		return res, err
	}
	// so um deploy que subiu vira a tag no ar (o Deploy ja gravou o Last)
	a, err = m.Get(name)
	if err != nil {
		return res, err
	}
	a.Tag = tag
	return res, m.save(a)
}

// redeploy refaz o deploy da tag no ar com a spec atual: e como uma
// mudanca de variavel ou secret chega nos containers, com a mesma
// validacao, espera dos healthchecks e volta se falhar.
func (m Manager) redeploy(ctx context.Context, name string) error {
	a, err := m.Get(name)
	if err != nil {
		return err
	}
	if a.Tag == "" {
		return nil // ainda sem deploy por tag: vale no primeiro
	}
	_, err = m.DeployTag(ctx, name, a.Tag)
	return err
}

// SetEnv troca as variaveis comuns do app. Com apply e o app no ar, refaz o
// deploy da tag atual pra elas valerem agora; senao valem no proximo deploy.
func (m Manager) SetEnv(ctx context.Context, name string, env map[string]string, apply bool) error {
	a, err := m.Get(name)
	if err != nil {
		return err
	}
	if a.Spec == nil {
		return fmt.Errorf("app %s sem spec (modo compose): as variaveis ficam no compose do app", name)
	}
	spec := *a.Spec
	spec.Env = env
	if len(env) == 0 {
		spec.Env = nil
	}
	if err := CheckSpec(spec); err != nil {
		return err
	}
	a.Spec = &spec
	if err := m.save(a); err != nil {
		return err
	}
	if !apply {
		return nil
	}
	return m.redeploy(ctx, name)
}

func (m Manager) missingSecrets(name string, names []string) []string {
	var out []string
	for _, n := range names {
		if ok, _ := secrets.Exists(m.secrets(name), n); !ok {
			out = append(out, n)
		}
	}
	return out
}

// Secret e o estado de um secret do app: so o nome, nunca o valor.
type Secret struct {
	Name string `json:"name"`
	// Set diz se ja tem valor gravado.
	Set bool `json:"set"`
	// Managed indica secret do cais (senha do banco): nao se edita a mao.
	Managed bool `json:"managed,omitempty"`
}

// Secrets lista os secrets do app: os da spec (com ou sem valor) e os que
// existem em secrets/.
func (m Manager) Secrets(name string) ([]Secret, error) {
	a, err := m.Get(name)
	if err != nil {
		return nil, err
	}
	stored, err := m.secrets(name).List()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []Secret
	add := func(n string, set bool) {
		if !seen[n] {
			seen[n] = true
			out = append(out, Secret{Name: n, Set: set, Managed: strings.HasPrefix(n, "cais_")})
		}
	}
	for _, n := range stored {
		add(n, true)
	}
	if a.Spec != nil {
		for _, n := range a.Spec.Secrets {
			add(n, false)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// SetSecret grava o valor de um secret do app. Com o app no ar, recria os
// containers pra eles enxergarem o arquivo novo.
func (m Manager) SetSecret(ctx context.Context, name, secret string, value []byte) error {
	if _, err := m.Get(name); err != nil {
		return err
	}
	if err := secrets.CheckName(secret); err != nil {
		return err
	}
	if strings.HasPrefix(secret, "cais_") {
		return fmt.Errorf("%s e gerenciado pelo cais", secret)
	}
	if len(value) == 0 {
		return errors.New("valor vazio")
	}
	if err := m.secrets(name).Put(secret, value); err != nil {
		return err
	}
	// secret novo entra na spec pra ser montado; o compose muda e o deploy
	// da tag no ar e refeito (so recriar nao montaria um arquivo novo)
	a, err := m.Get(name)
	if err != nil {
		return err
	}
	if a.Spec != nil && !slices.Contains(a.Spec.Secrets, secret) {
		spec := *a.Spec
		spec.Secrets = append(slices.Clone(spec.Secrets), secret)
		a.Spec = &spec
		if err := m.save(a); err != nil {
			return err
		}
		return m.redeploy(ctx, name)
	}
	if _, err := os.Stat(filepath.Join(m.dir(name), ComposeFile)); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return m.Docker.Recreate(ctx, m.Project(name))
}

// Plan e a proposta de configuracao de um app a partir da analise do
// repositorio. E so sugestao: quem aplica e o usuario, editando o que
// quiser (banco, porta, variaveis) antes.
type Plan struct {
	App string `json:"app"`
	// Repo e o repositorio no GitHub (dono/nome), quando o plano veio de la.
	Repo     string        `json:"repo,omitempty"`
	Spec     Spec          `json:"spec"`
	Routes   []Route       `json:"routes,omitempty"`
	Database *Database     `json:"database,omitempty"`
	Analysis detect.Result `json:"analysis"`
}

// PlanFromAnalysis monta o plano: imagem no registry do cais, rota
// <app>.<dominio>, banco detectado e cada variavel no lugar certo (comum
// no ambiente, segredo em arquivo, banco pelo cais).
func PlanFromAnalysis(name, registryHost, domain string, r detect.Result) Plan {
	p := Plan{App: name, Analysis: r}
	p.Spec = Spec{Image: registryHost + "/" + name, Port: r.Port, Volumes: r.Volumes, Env: map[string]string{},
		Command: r.Command}
	if r.Build.Context != "." || r.Build.Dockerfile != "Dockerfile" || len(r.Build.Args) > 0 {
		p.Spec.Build = Build{Context: r.Build.Context, Dockerfile: r.Build.Dockerfile, Args: r.Build.Args}
		if p.Spec.Build.Context == "." {
			p.Spec.Build.Context = ""
		}
		if p.Spec.Build.Dockerfile == "Dockerfile" {
			p.Spec.Build.Dockerfile = ""
		}
	}
	for _, v := range r.Vars {
		switch v.Kind {
		case detect.VarPlain:
			if v.Name != "PORT" {
				p.Spec.Env[v.Name] = v.Example
			}
		case detect.VarSecret:
			n := strings.ToLower(v.Name)
			p.Spec.Secrets = append(p.Spec.Secrets, n)
			p.Spec.Env[v.Name+"_FILE"] = "/run/secrets/" + n
		}
	}
	if r.Port > 0 {
		p.Spec.Env["PORT"] = fmt.Sprint(r.Port)
		if domain != "" {
			host := name + "." + domain
			// a URL do app no exemplo de producao (AUTH_URL...) ja diz o
			// dominio real: vira a rota se for do dominio do cais
			for _, k := range appURLVars {
				if h := urlHost(p.Spec.Env[k]); h != "" && (h == domain || strings.HasSuffix(h, "."+domain)) {
					host = h
					break
				}
			}
			p.Routes = []Route{{Host: host, Service: WebService, Port: r.Port}}
			for _, k := range appURLVars {
				if _, ok := p.Spec.Env[k]; ok {
					p.Spec.Env[k] = "https://" + host
				}
			}
		}
	}
	if len(p.Spec.Env) == 0 {
		p.Spec.Env = nil
	}
	if r.Database != nil && r.Database.Engine != "" {
		p.Database = &Database{Engine: r.Database.Engine, Env: r.Database.Env, Extensions: r.Database.Extensions}
	}
	return p
}

// appURLVars sao variaveis com a URL publica do app (login, links em e-mail):
// apontam pra rota.
var appURLVars = []string{"AUTH_URL", "NEXTAUTH_URL", "APP_URL", "PUBLIC_URL", "BASE_URL", "SITE_URL", "NEXT_PUBLIC_APP_URL", "NEXT_PUBLIC_SITE_URL"}

func urlHost(v string) string {
	rest, ok := strings.CutPrefix(v, "https://")
	if !ok {
		rest, ok = strings.CutPrefix(v, "http://")
	}
	if !ok {
		return ""
	}
	host, _, _ := strings.Cut(rest, "/")
	host, _, _ = strings.Cut(host, ":")
	return host
}
