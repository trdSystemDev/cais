package apps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trdSystemDev/cais/internal/docker"
)

// fakeSource e o GitHub: cada commit e um conjunto de arquivos.
type fakeSource struct {
	head    string                       // commit da branch
	commits map[string]map[string]string // commit -> caminho -> conteudo
	refs    []string                     // refs pedidos no Resolve
	// historico pra promocao: pais de cada commit, outras branches e
	// arvore (commits com a mesma arvore tem o mesmo codigo)
	parents  map[string][]string
	branches map[string]string
	trees    map[string]string
}

func (f *fakeSource) Resolve(_ context.Context, repo, ref string) (string, error) {
	f.refs = append(f.refs, ref)
	if c, ok := f.branches[ref]; ok {
		return c, nil
	}
	if _, ok := f.commits[ref]; ok {
		return ref, nil
	}
	if ref == "" || ref == "main" {
		return f.head, nil
	}
	return "", fmt.Errorf("%s nao existe em %s", ref, repo)
}

func (f *fakeSource) Download(_ context.Context, _, commit, dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	for p, c := range f.commits[commit] {
		full := filepath.Join(dir, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// history e o commit e todos os seus ancestrais.
func (f *fakeSource) history(c string) map[string]bool {
	seen := map[string]bool{}
	for todo := []string{c}; len(todo) > 0; todo = todo[1:] {
		if !seen[todo[0]] {
			seen[todo[0]] = true
			todo = append(todo, f.parents[todo[0]]...)
		}
	}
	return seen
}

func (f *fakeSource) Compare(ctx context.Context, repo, base, head string) (Comparison, error) {
	b, err := f.Resolve(ctx, repo, base)
	if err != nil {
		return Comparison{}, err
	}
	h, err := f.Resolve(ctx, repo, head)
	if err != nil {
		return Comparison{}, err
	}
	hb, hh := f.history(b), f.history(h)
	var c Comparison
	for x := range hh {
		if !hb[x] {
			c.AheadBy++
		}
	}
	for x := range hb {
		if !hh[x] {
			c.BehindBy++
		}
	}
	switch {
	case c.AheadBy == 0 && c.BehindBy == 0:
		c.Status = "identical"
	case c.BehindBy == 0:
		c.Status = "ahead"
	case c.AheadBy == 0:
		c.Status = "behind"
	default:
		c.Status = "diverged"
	}
	return c, nil
}

func (f *fakeSource) Tree(_ context.Context, _, commit string) (string, error) {
	if t, ok := f.trees[commit]; ok {
		return t, nil
	}
	return "tree-" + commit, nil
}

// fakeDocker registra o que o deploy pediu, com o compose de cada chamada
// (pra saber de qual pasta subiu).
type fakeDocker struct {
	calls  []string
	config string          // json do compose resolvido
	failUp map[string]bool // compose (conteudo) cujo up falha
	// compose (conteudo) cujo rollout falha
	failRollout map[string]bool
	failBuild   map[string]error // compose (conteudo) cujo build falha
	// hang trava a chamada ("build v2", "up v2") ate o ctx acabar, como o
	// docker morto pelo ctx; canceled sao as chamadas que ja chegaram com o
	// ctx cancelado
	hang     map[string]bool
	canceled []string
	images   map[string]string             // tag -> id da imagem
	envSeen  map[string]string             // .env lido no Config
	ps       map[string][]docker.Container // compose -> containers depois do up
}

func (f *fakeDocker) compose(p docker.Project) string {
	data, _ := os.ReadFile(p.Files[0])
	return strings.TrimSpace(string(data))
}

func (f *fakeDocker) Config(_ context.Context, p docker.Project) ([]byte, error) {
	f.calls = append(f.calls, "config "+f.compose(p))
	env, _ := os.ReadFile(filepath.Join(filepath.Dir(p.Files[0]), ".env"))
	f.envSeen, _ = ParseEnv(env)
	if f.config == "" {
		return []byte(`{"services": {}}`), nil
	}
	// {{src}} e a pasta do codigo desta chamada (contexto do build)
	return []byte(strings.ReplaceAll(f.config, "{{src}}", filepath.Dir(p.Files[0]))), nil
}

// wait trava a chamada se ela esta no hang e registra se o ctx dela ja
// chegou cancelado.
func (f *fakeDocker) wait(ctx context.Context, call string) error {
	if ctx.Err() != nil {
		f.canceled = append(f.canceled, call)
	}
	if f.hang[call] {
		<-ctx.Done()
		return errors.New("signal: killed")
	}
	return nil
}

func (f *fakeDocker) Build(ctx context.Context, p docker.Project, out io.Writer) error {
	c := f.compose(p)
	f.calls = append(f.calls, "build "+c)
	if out != nil {
		io.WriteString(out, "#1 [web] build "+c+"\r\n#2 DONE")
	}
	if err := f.wait(ctx, "build "+c); err != nil {
		return err
	}
	if err := f.failBuild[c]; err != nil {
		return err
	}
	if f.images != nil {
		// o build troca a imagem do service web pela do codigo
		f.images[p.Name+"-web"] = "img-" + c
	}
	return nil
}

func (f *fakeDocker) Up(ctx context.Context, p docker.Project) error {
	c := f.compose(p)
	f.calls = append(f.calls, "up "+c)
	if err := f.wait(ctx, "up "+c); err != nil {
		return err
	}
	if f.failUp[c] {
		return errors.New("container web is unhealthy")
	}
	return nil
}

func (f *fakeDocker) PS(_ context.Context, p docker.Project) ([]docker.Container, error) {
	if cs, ok := f.ps[f.compose(p)]; ok {
		return cs, nil
	}
	return []docker.Container{{Name: "x-web-1", State: "running"}}, nil
}

func (f *fakeDocker) Restart(_ context.Context, p docker.Project, service string) error {
	f.calls = append(f.calls, "restart "+service)
	return nil
}

func (f *fakeDocker) Down(_ context.Context, name string, volumes bool) error {
	f.calls = append(f.calls, fmt.Sprintf("down %s volumes=%v", name, volumes))
	return nil
}

func (f *fakeDocker) Rollout(_ context.Context, p docker.Project, service, image string, drain bool) error {
	c := f.compose(p)
	call := "rollout " + service + " " + image
	if drain {
		call += " drain"
	}
	f.calls = append(f.calls, call+" "+c)
	if f.failRollout[c] {
		return errors.New("container web-2 is unhealthy")
	}
	return nil
}

// imagens: tag -> id; o build do compose v<n> gera a imagem "img-v<n>"
func (f *fakeDocker) TagImage(_ context.Context, src, dst string) error {
	id, ok := f.images[src]
	if !ok {
		return errors.New("no such image " + src)
	}
	f.images[dst] = id
	f.calls = append(f.calls, "tag "+src+" "+dst)
	return nil
}

func (f *fakeDocker) HasImage(_ context.Context, ref string) bool {
	_, ok := f.images[ref]
	return ok
}

func (f *fakeDocker) ImageTags(_ context.Context, project string) ([]string, error) {
	var out []string
	for t := range f.images {
		if strings.HasPrefix(t, project+"-") {
			out = append(out, t)
		}
	}
	slices.Sort(out)
	return out, nil
}

func (f *fakeDocker) RemoveImages(_ context.Context, refs []string) error {
	for _, r := range refs {
		delete(f.images, r)
	}
	if len(refs) > 0 {
		f.calls = append(f.calls, "rmi "+strings.Join(refs, " "))
	}
	return nil
}

func (f *fakeDocker) PruneImages(_ context.Context, project string) error {
	f.calls = append(f.calls, "prune "+project)
	return nil
}

func (f *fakeDocker) Stop(context.Context, docker.Project) error {
	f.calls = append(f.calls, "stop")
	return nil
}

func newManager(t *testing.T) (Manager, *fakeSource, *fakeDocker) {
	src := &fakeSource{head: "c1", commits: map[string]map[string]string{
		"c1": {"docker-compose.yml": "v1"},
		"c2": {"docker-compose.yml": "v2"},
	}}
	d := &fakeDocker{failUp: map[string]bool{}}
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	return Manager{Root: filepath.Join(t.TempDir(), "apps"), Source: src, Docker: d, Now: func() time.Time { return now },
		Networks: []string{ProxyNetwork}, Bin: "/usr/local/bin/cais"}, src, d
}

func TestCheckRecusa(t *testing.T) {
	ok := App{Name: "meu-app", Repo: "trdSystemDev/meu-app"}
	if err := Check(ok); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*App){
		"nome de stack":       func(a *App) { a.Name = "proxy" },
		"nome maiusculo":      func(a *App) { a.Name = "MeuApp" },
		"repo sem dono":       func(a *App) { a.Repo = "meu-app" },
		"repo url":            func(a *App) { a.Repo = "https://github.com/a/b" },
		"branch com ..":       func(a *App) { a.Branch = "a/../b" },
		"compose fora":        func(a *App) { a.Compose = "../x.yml" },
		"compose absoluto":    func(a *App) { a.Compose = "/etc/x.yml" },
		"compose sem yml":     func(a *App) { a.Compose = "Dockerfile" },
		"compose com espaco":  func(a *App) { a.Compose = "a b.yml" },
		"compose com ./ sujo": func(a *App) { a.Compose = "./x.yml" },
	}
	for name, mutate := range cases {
		a := ok
		mutate(&a)
		if Check(a) == nil {
			t.Errorf("%s: aceitou %+v", name, a)
		}
	}
	a := ok
	a.Compose = "deploy/docker-compose.producao.yaml"
	if err := Check(a); err != nil {
		t.Errorf("recusou compose numa subpasta: %v", err)
	}
}

func TestCreateGetList(t *testing.T) {
	m, _, _ := newManager(t)
	if _, err := m.Create(App{Name: "b", Repo: "o/b", Commit: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(App{Name: "a", Repo: "o/a", Branch: "prod", Compose: "prod.yml"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(App{Name: "a", Repo: "o/a"}); err == nil {
		t.Error("criou app repetido")
	}
	list, err := m.List()
	if err != nil || len(list) != 2 || list[0].Name != "a" || list[1].Commit != "" {
		t.Fatalf("List = %+v, %v", list, err)
	}
	if list[0].ComposeFile() != "prod.yml" || list[1].ComposeFile() != DefaultCompose {
		t.Errorf("compose: %s %s", list[0].ComposeFile(), list[1].ComposeFile())
	}
	info, _ := os.Stat(filepath.Join(m.Root, "a"))
	if info == nil || info.Mode().Perm() != 0o700 {
		t.Errorf("pasta do app %v", info)
	}
	if _, err := m.Get("c"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get de app que nao existe: %v", err)
	}
}

// runEnv le o run.env que o deploy gravou (o que o cais run injeta).
func runEnv(t *testing.T, m Manager, app string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(m.RunEnvPath(app))
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}
	return env
}

// o run.env leva todas as variaveis, com qualquer valor; o .env do projeto
// so as do cais (e as citadas no compose, abaixo)
func TestDeployGravaEnvESobe(t *testing.T) {
	m, src, d := newManager(t)
	m.Create(App{Name: "web", Repo: "o/web", Branch: "main"})
	vars := map[string]string{"NODE_ENV": "production", "TOKEN": "a$b'c \"d\"", "CHAVE": "-----BEGIN-----\nlinha\n-----END-----"}
	if err := m.SetEnv("web", vars); err != nil {
		t.Fatal(err)
	}

	res, err := m.Deploy(context.Background(), "web", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Commit != "c1" || src.refs[0] != "main" {
		t.Errorf("commit %s, refs %v", res.Commit, src.refs)
	}
	// build antes da troca, up do codigo novo e a imagem velha sai
	if !slices.Equal(d.calls, []string{"config v1", "build v1", "up v1", "prune web"}) {
		t.Errorf("chamadas %v", d.calls)
	}
	if got := runEnv(t, m, "web"); !maps.Equal(got, vars) {
		t.Errorf("run.env %v", got)
	}
	if info, err := os.Stat(m.RunEnvPath("web")); err != nil || info.Mode().Perm() != 0o444 {
		t.Errorf("run.env %v %v", info, err)
	}
	want := map[string]string{"CAIS_APP": "web", "CAIS_COMMIT": "c1", "CAIS_BIN": "/usr/local/bin/cais", "CAIS_ENV": m.RunEnvPath("web")}
	if !maps.Equal(d.envSeen, want) {
		t.Errorf(".env do projeto %v, queria so as do cais %v", d.envSeen, want)
	}
	info, err := os.Stat(filepath.Join(m.Root, "web", SrcDir, ".env"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf(".env %v %v", info, err)
	}
	a, _ := m.Get("web")
	if a.Commit != "c1" || a.Last == nil || a.Last.Error != "" {
		t.Errorf("app depois do deploy %+v", a)
	}
	if _, err := os.Stat(filepath.Join(m.Root, "web", "src.next")); !os.IsNotExist(err) {
		t.Error("sobrou src.next")
	}
}

// o .env do projeto (ao lado do compose, mesmo numa subpasta) leva as
// variaveis que o compose cita pelo nome, e so elas
func TestDeployComposeNumaSubpasta(t *testing.T) {
	m, src, d := newManager(t)
	src.commits["c1"] = map[string]string{"deploy/prod.yml": "Host(`${DOMINIO}`) environment: NPM_TOKEN"}
	m.Create(App{Name: "web", Repo: "o/web", Compose: "deploy/prod.yml"})
	m.SetEnv("web", map[string]string{"DOMINIO": "web.x", "NPM_TOKEN": "t", "DOMINIO_VELHO": "nao", "SENHA": "nao"})
	if _, err := m.Deploy(context.Background(), "web", ""); err != nil {
		t.Fatal(err)
	}
	if d.envSeen["DOMINIO"] != "web.x" || d.envSeen["NPM_TOKEN"] != "t" {
		t.Errorf(".env sem as citadas no compose: %v", d.envSeen)
	}
	if _, ok := d.envSeen["DOMINIO_VELHO"]; ok {
		t.Errorf("nome parecido entrou no .env: %v", d.envSeen)
	}
	if _, ok := d.envSeen["SENHA"]; ok {
		t.Errorf("variavel nao citada entrou no .env: %v", d.envSeen)
	}
}

func TestDeploySemComposeNaoMexeNoNoAr(t *testing.T) {
	m, src, d := newManager(t)
	m.Create(App{Name: "web", Repo: "o/web"})
	m.Deploy(context.Background(), "web", "")
	src.commits["c3"] = map[string]string{"README.md": "sem compose"}
	d.calls = nil

	_, err := m.Deploy(context.Background(), "web", "c3")
	if err == nil || !strings.Contains(err.Error(), "nao tem o compose") {
		t.Fatalf("erro %v", err)
	}
	if len(d.calls) != 0 {
		t.Errorf("chamou o docker: %v", d.calls)
	}
	a, _ := m.Get("web")
	if a.Commit != "c1" || a.Last.Error == "" || a.Last.Commit != "c3" {
		t.Errorf("app %+v / %+v", a, a.Last)
	}
	if data, _ := os.ReadFile(filepath.Join(m.Root, "web", SrcDir, DefaultCompose)); string(data) != "v1" {
		t.Error("o codigo no ar mudou")
	}
}

func TestDeployFalhoVoltaAAnterior(t *testing.T) {
	m, _, d := newManager(t)
	m.Create(App{Name: "web", Repo: "o/web"})
	if _, err := m.Deploy(context.Background(), "web", "c1"); err != nil {
		t.Fatal(err)
	}
	d.calls = nil
	d.failUp["v2"] = true

	_, err := m.Deploy(context.Background(), "web", "c2")
	if err == nil || !strings.Contains(err.Error(), "unhealthy") || !strings.Contains(err.Error(), "restaurada") {
		t.Fatalf("erro %v", err)
	}
	if !slices.Equal(d.calls, []string{"config v2", "build v2", "up v2", "config v1", "build v1", "up v1"}) {
		t.Errorf("chamadas %v", d.calls)
	}
	a, _ := m.Get("web")
	if a.Commit != "c1" || !a.Last.RolledBack || a.Last.Commit != "c2" {
		t.Errorf("app %+v / %+v", a, a.Last)
	}
	if data, _ := os.ReadFile(filepath.Join(m.Root, "web", SrcDir, DefaultCompose)); string(data) != "v1" {
		t.Error("src nao voltou pro commit anterior")
	}

	// o proximo deploy que da certo segue normal
	d.failUp["v2"] = false
	if _, err := m.Deploy(context.Background(), "web", "c2"); err != nil {
		t.Fatal(err)
	}
	if a, _ := m.Get("web"); a.Commit != "c2" {
		t.Errorf("commit %s", a.Commit)
	}
}

// O up falha e a volta pra versao anterior tambem: o erro diz as duas
// coisas e o historico registra que nao voltou.
func TestDeployFalhoEAVoltaTambemFalha(t *testing.T) {
	m, _, d := newManager(t)
	m.Create(App{Name: "web", Repo: "o/web"})
	if _, err := m.Deploy(context.Background(), "web", "c1"); err != nil {
		t.Fatal(err)
	}
	d.failUp["v2"] = true
	d.failUp["v1"] = true

	_, err := m.Deploy(context.Background(), "web", "c2")
	if err == nil || !strings.Contains(err.Error(), "unhealthy") || !strings.Contains(err.Error(), "a volta pra versao anterior tambem falhou") {
		t.Fatalf("erro %v", err)
	}
	a, _ := m.Get("web")
	if a.Commit != "c1" || a.Last.RolledBack || a.Last.Commit != "c2" || a.Last.Error == "" {
		t.Errorf("app %+v / %+v", a, a.Last)
	}
}

func TestPrimeiroDeployFalhoNaoTemPraOndeVoltar(t *testing.T) {
	m, _, d := newManager(t)
	d.failUp["v1"] = true
	m.Create(App{Name: "web", Repo: "o/web"})
	_, err := m.Deploy(context.Background(), "web", "")
	if err == nil || strings.Contains(err.Error(), "restaurada") {
		t.Fatalf("erro %v", err)
	}
	a, _ := m.Get("web")
	if a.Commit != "" || a.Last.RolledBack {
		t.Errorf("app %+v", a)
	}
}

func TestRestartEStopPedemDeploy(t *testing.T) {
	m, _, d := newManager(t)
	m.Create(App{Name: "web", Repo: "o/web"})
	if err := m.Restart(context.Background(), "web", ""); err == nil {
		t.Error("reiniciou app sem deploy")
	}
	if cs, err := m.Status(context.Background(), "web"); err != nil || len(cs) != 0 {
		t.Errorf("status antes do deploy %v %v", cs, err)
	}
	m.Deploy(context.Background(), "web", "")
	if err := m.Restart(context.Background(), "web", "worker"); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(context.Background(), "web"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(d.calls, "restart worker") || !slices.Contains(d.calls, "stop") {
		t.Errorf("chamadas %v", d.calls)
	}
}

func TestSetEnvRecusa(t *testing.T) {
	m, _, _ := newManager(t)
	m.Create(App{Name: "web", Repo: "o/web"})
	for name, env := range map[string]map[string]string{
		"nome invalido":  {"1A": "x"},
		"nome com ponto": {"A.B": "x"},
		"nome do cais":   {"CAIS_ENV": "x"},
		"byte zero":      {"A": "a\x00b"},
	} {
		if err := m.SetEnv("web", env); err == nil {
			t.Errorf("%s: aceitou %v", name, env)
		}
	}
	in := map[string]string{"A": "linha\nquebrada", "B": `aspa ' "dupla" $x \ barra`, "C": ""}
	if err := m.SetEnv("web", in); err != nil {
		t.Fatal(err)
	}
	if out, _ := m.Env("web"); !maps.Equal(out, in) {
		t.Errorf("ida e volta %q", out)
	}
	if info, _ := os.Stat(filepath.Join(m.Root, "web", EnvFile)); info == nil || info.Mode().Perm() != 0o600 {
		t.Errorf("arquivo das variaveis %v", info)
	}
}

func TestEnvIdaEVolta(t *testing.T) {
	in := map[string]string{"A": "simples", "B": "com $ e espaco", "C": `aspa ' "dupla" \ barra`, "D": ""}
	data, err := FormatEnv(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := ParseEnv(data)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range in {
		if out[k] != v {
			t.Errorf("%s: %q virou %q", k, v, out[k])
		}
	}
	if _, err := FormatEnv(map[string]string{"A": "linha\nquebrada"}); err == nil {
		t.Error("aceitou quebra de linha")
	}
	if _, err := FormatEnv(map[string]string{"1A": "x"}); err == nil {
		t.Error("aceitou nome invalido")
	}
}

func TestParseEnvDeArquivoComum(t *testing.T) {
	out, err := ParseEnv([]byte("# app\r\nexport A=1\r\n\rB = \"dois\"\rC='tres'\n"))
	if err != nil || out["A"] != "1" || out["B"] != "dois" || out["C"] != "tres" {
		t.Errorf("%q %v", out, err)
	}
}

func TestCheckComposeBloqueiaComMotivo(t *testing.T) {
	src := "/var/lib/cais/apps/web/src.next"
	r := Rules{App: "web", Src: src, Bin: "/usr/local/bin/cais", RunEnv: "/var/lib/cais/apps/web/run.env", DotEnv: src + "/.env",
		Networks: []string{"postgres_network", ProxyNetwork}, Apps: []string{"web-homolog"}}
	cases := map[string]struct{ svc, top, want string }{
		"porta":             {`"ports": [{"published": "8080", "target": 3000}]`, "", "tire o ports"},
		"porta aleatoria":   {`"ports": [{"target": 3000}]`, "", "porta aleatoria"},
		"docker.sock":       {`"volumes": [{"type": "bind", "source": "/var/run/docker.sock"}]`, "", "docker.sock"},
		"bind fora":         {`"volumes": [{"type": "bind", "source": "/etc", "target": "/x"}]`, "", "volume nomeado"},
		"privileged":        {`"privileged": true`, "", "privileged"},
		"cap_add":           {`"cap_add": ["SYS_ADMIN"]`, "", "cap_add"},
		"devices":           {`"devices": ["/dev/sda"]`, "", "devices"},
		"rede do host":      {`"network_mode": "host"`, "", "network_mode: host"},
		"pid do host":       {`"pid": "host"`, "", "pid: host"},
		"ipc de fora":       {`"ipc": "container:postgres"`, "", "container de fora"},
		"seccomp":           {`"security_opt": ["seccomp=unconfined"]`, "", "seccomp=unconfined"},
		"container_name":    {`"container_name": "web"`, "", "container_name"},
		"external_links":    {`"external_links": ["postgres"]`, "", "external_links"},
		"env_file fora":     {`"env_file": [{"path": "/etc/shadow"}]`, "", "fora do app"},
		"env_file do .env":  {`"env_file": [{"path": "` + src + `/.env"}]`, "", "docker inspect"},
		"bin com escrita":   {`"volumes": [{"type": "bind", "source": "/usr/local/bin/cais", "target": "/cais"}]`, "", "so leitura"},
		"run.env escrita":   {`"volumes": [{"type": "bind", "source": "/var/lib/cais/apps/web/run.env", "target": "/run/cais/env"}]`, "", "so leitura"},
		"outro binario":     {`"volumes": [{"type": "bind", "source": "/usr/local/bin/outro", "target": "/x", "read_only": true}]`, "", "volume nomeado"},
		"run.env de outro":  {`"volumes": [{"type": "bind", "source": "/var/lib/cais/apps/loja/run.env", "target": "/x", "read_only": true}]`, "", "volume nomeado"},
		"build fora":        {`"build": {"context": "/home"}`, "", "fora do repositorio"},
		"build remoto":      {`"build": {"context": "https://github.com/x/y.git"}`, "", "proprio repositorio"},
		"build ssh":         {`"build": {"context": "` + src + `", "ssh": ["default"]}`, "", "ssh"},
		"router de outro":   {`"labels": {"traefik.http.routers.loja.rule": "Host(` + "`x`" + `)"}, "networks": {"proxy_network": null}`, "", "comecam com o nome do app"},
		"traefik sem rede":  {`"labels": {"traefik.enable": "true"}`, "", "nao esta na proxy_network"},
		"router da homolog": {`"labels": {"traefik.http.routers.web-homolog.rule": "Host(` + "`x`" + `)"}, "networks": {"proxy_network": null}`, "", "que e do app web-homolog"},
		"rede de outro app": {"", `"networks": {"outra": {"name": "loja_default", "external": true}}`, "nao e da infra"},
		"rede com name":     {"", `"networks": {"outra": {"name": "loja_default"}}`, "Tire o name"},
		"infra desligada":   {"", `"networks": {"mysql_network": {"name": "mysql_network", "external": true}}`, "Ligue o mysql"},
		"volume external":   {"", `"volumes": {"pg": {"name": "postgres_data", "external": true}}`, "external"},
		"volume device":     {"", `"volumes": {"d": {"name": "web_d", "driver_opts": {"device": "/etc", "o": "bind"}}}`, "driver_opts"},
		"secret fora":       {"", `"secrets": {"chave": {"file": "/root/.ssh/id_rsa"}}`, "environment: CHAVE"},
		"secret do repo":    {"", `"secrets": {"chave": {"file": "` + src + `/chave"}}`, "cais app env web CHAVE"},
		"secret external":   {"", `"secrets": {"chave": {"external": true}}`, "environment:"},
		"config fora":       {"", `"configs": {"c": {"file": "/etc/hosts"}}`, "fora do repositorio"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := `{"services": {"app": {` + tc.svc + `}}`
			if tc.top != "" {
				cfg += ", " + tc.top
			}
			problems, _ := CheckCompose([]byte(cfg+"}"), r)
			if len(problems) == 0 || !strings.Contains(strings.Join(problems, "\n"), tc.want) {
				t.Fatalf("esperava problema com %q, veio %v", tc.want, problems)
			}
		})
	}
}

func TestCheckComposeAceitaOFormatoDoCais(t *testing.T) {
	src, bin, runEnv := "/var/lib/cais/apps/web/src.next", "/usr/local/bin/cais", "/var/lib/cais/apps/web/run.env"
	cfg := `{"services": {
		"web": {"build": {"context": "` + src + `/app", "dockerfile": "Dockerfile"},
			"entrypoint": ["/cais", "run", "--", "node", "server.js"],
			"labels": {"traefik.enable": "true", "traefik.http.routers.web.rule": "Host(` + "`web.x`" + `)",
				"traefik.http.routers.web-api.rule": "PathPrefix(` + "`/api`" + `)", "traefik.http.services.web.loadbalancer.server.port": "3000"},
			"networks": {"default": null, "proxy_network": null, "postgres_network": null},
			"healthcheck": {"test": ["CMD-SHELL", "test ! -f /tmp/drain && wget -qO- http://127.0.0.1:3000/"]},
			"logging": {"driver": "json-file", "options": {"max-size": "20m", "max-file": "5"}},
			"volumes": [{"type": "volume", "source": "dados", "target": "/data"},
				{"type": "bind", "source": "` + src + `/worker", "target": "/worker", "read_only": true},
				{"type": "bind", "source": "` + bin + `", "target": "/cais", "read_only": true},
				{"type": "bind", "source": "` + runEnv + `", "target": "/run/cais/env", "read_only": true}]},
		"init": {"image": "alpine", "logging": {"driver": "json-file", "options": {"max-size": "20m", "max-file": "5"}}, "volumes": [{"type": "bind", "source": "` + src + `/data"}]}},
	"networks": {"default": {"name": "web_default"}, "proxy_network": {"name": "proxy_network", "external": true},
		"postgres_network": {"name": "postgres_network", "external": true}},
	"volumes": {"dados": {"name": "web_dados"}},
	"secrets": {"npm_token": {"environment": "NPM_TOKEN"}}}`
	problems, warnings := CheckCompose([]byte(cfg), Rules{App: "web", Src: src, Bin: bin, RunEnv: runEnv, DotEnv: src + "/.env", Networks: []string{"postgres_network", ProxyNetwork}})
	if len(problems) != 0 {
		t.Fatalf("recusou o formato do cais: %v", problems)
	}
	// bind com escrita no codigo: os dados somem no proximo deploy; o volume
	// dados nao entra no backup
	if len(warnings) != 2 || !strings.Contains(warnings[0], "some no proximo deploy") || !strings.Contains(warnings[1], "dados nao entram no backup") {
		t.Errorf("avisos %v", warnings)
	}
}

func TestCheckComposeAvisaSemHealthcheckOuSemDrain(t *testing.T) {
	svc := func(hc string) []byte {
		return []byte(`{"services": {"web": {"labels": {"traefik.enable": "true",
			"traefik.http.services.web.loadbalancer.server.port": "8080"}, "networks": {"proxy_network": null},
			"logging": {"driver": "json-file", "options": {"max-size": "20m", "max-file": "5"}}` + hc + `}},
			"networks": {"proxy_network": {"name": "proxy_network", "external": true}}}`)
	}
	r := Rules{App: "web", Src: "/x", Networks: []string{ProxyNetwork}}
	cases := map[string]struct{ hc, want string }{
		"sem healthcheck": {``, "sem healthcheck no compose"},
		"desligado":       {`, "healthcheck": {"disable": true}`, "sem healthcheck no compose"},
		"sem drain":       {`, "healthcheck": {"test": ["CMD", "wget", "-qO-", "http://127.0.0.1:8080/"]}`, "nao tem a drenagem"},
		"com drain":       {`, "healthcheck": {"test": ["CMD-SHELL", "test ! -f /tmp/drain && wget -qO- http://127.0.0.1:8080/"]}`, ""},
	}
	for name, tc := range cases {
		problems, w := CheckCompose(svc(tc.hc), r)
		if len(problems) != 0 {
			t.Fatalf("%s: problemas %v", name, problems)
		}
		switch {
		case tc.want == "" && len(w) != 0:
			t.Errorf("%s: avisos %v", name, w)
		case tc.want != "" && (len(w) != 1 || !strings.Contains(w[0], tc.want) || !strings.Contains(w[0], "127.0.0.1:8080")):
			t.Errorf("%s: avisos %v", name, w)
		}
	}
}

func TestCheckComposeAvisaSemLimiteDeLog(t *testing.T) {
	cases := map[string]struct {
		logging string
		warn    bool
	}{
		"sem logging":           {``, true},
		"json-file sem limite":  {`, "logging": {"driver": "json-file"}`, true},
		"so options sem limite": {`, "logging": {"options": {"max-file": "3"}}`, true},
		"json-file com limite":  {`, "logging": {"driver": "json-file", "options": {"max-size": "10m"}}`, false},
		"local":                 {`, "logging": {"driver": "local"}`, false},
		"journald":              {`, "logging": {"driver": "journald"}`, false},
	}
	for name, tc := range cases {
		_, w := CheckCompose([]byte(`{"services": {"worker": {"image": "x"`+tc.logging+`}}}`), Rules{App: "web", Src: "/x"})
		got := len(w) == 1 && strings.Contains(w[0], "sem limite de log")
		if got != tc.warn || (!tc.warn && len(w) != 0) {
			t.Errorf("%s: avisos %v", name, w)
		}
	}
}

func TestCheckComposeAvisaDoEnvNoBuild(t *testing.T) {
	ctx := t.TempDir()
	cfg := []byte(`{"services": {"web": {"build": {"context": "` + ctx + `"}, "logging": {"driver": "json-file", "options": {"max-size": "20m", "max-file": "5"}}}}}`)
	r := Rules{App: "web", Src: ctx, DotEnv: filepath.Join(ctx, ".env")}
	if _, w := CheckCompose(cfg, r); len(w) != 1 || !strings.Contains(w[0], ".dockerignore") {
		t.Errorf("sem .dockerignore: %v", w)
	}
	os.WriteFile(filepath.Join(ctx, ".dockerignore"), []byte("node_modules\n.env\n"), 0o644)
	if _, w := CheckCompose(cfg, r); len(w) != 0 {
		t.Errorf("com .env no .dockerignore: %v", w)
	}
}

func TestDeployRecusaComposeForaDoFormato(t *testing.T) {
	m, _, d := newManager(t)
	m.Create(App{Name: "web", Repo: "o/web"})
	m.Deploy(context.Background(), "web", "c1")
	d.calls = nil
	d.config = `{"services": {"web": {"ports": [{"published": "80", "target": 80}], "privileged": true}}}`

	res, err := m.Deploy(context.Background(), "web", "c2")
	var ce *ComposeError
	if !errors.As(err, &ce) || len(ce.Problems) != 2 || len(res.Problems) != 2 {
		t.Fatalf("erro %v, res %+v", err, res)
	}
	if !strings.Contains(err.Error(), "nada foi buildado") || !strings.Contains(err.Error(), "cais app guide web") {
		t.Errorf("mensagem %v", err)
	}
	// para antes do build: o que esta no ar nem e tocado
	if !slices.Equal(d.calls, []string{"config v2"}) {
		t.Errorf("chamadas %v", d.calls)
	}
	a, _ := m.Get("web")
	if a.Commit != "c1" || len(a.Last.Problems) != 2 {
		t.Errorf("app %+v %+v", a, a.Last)
	}
}

// o modelo da Ajuda, resolvido pelo docker compose de verdade, passa na
// checagem: o jeito certo que o cais ensina e aceito
func TestModeloDaAjudaPassaNaChecagem(t *testing.T) {
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skip("docker compose nao instalado")
	}
	root := t.TempDir()
	src, bin, runEnv := filepath.Join(root, "src"), filepath.Join(root, "cais"), filepath.Join(root, "run.env")
	os.MkdirAll(src, 0o755)
	os.WriteFile(bin, nil, 0o755)
	os.WriteFile(runEnv, []byte("{}"), 0o444)
	os.WriteFile(filepath.Join(src, ".env"), []byte("CAIS_BIN="+bin+"\nCAIS_ENV="+runEnv+"\nDOMINIO=web.exemplo.com.br\n"), 0o600)
	g := Guide(GuideInput{App: "web", Host: "web.exemplo.com.br", Networks: []string{"postgres_network"},
		Env: []string{"DATABASE_URL"}, Databases: []DBLink{{Engine: "postgres", Name: "web"}}})
	os.WriteFile(filepath.Join(src, "docker-compose.yml"), []byte(g), 0o644)
	cmd := exec.Command("docker", "compose", "-p", "web", "-f", filepath.Join(src, "docker-compose.yml"), "config", "--format", "json", "--no-env-resolution")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("o modelo nao e um compose valido: %v\n%s", err, g)
	}
	problems, warnings := CheckCompose(out, Rules{App: "web", Src: src, Bin: bin, RunEnv: runEnv, DotEnv: filepath.Join(src, ".env"),
		Networks: []string{"postgres_network", ProxyNetwork}})
	if len(problems) != 0 {
		t.Fatalf("o modelo da Ajuda foi recusado: %v\n%s", problems, g)
	}
	// o modelo ja vem com a drenagem: troca sem queda sem aviso
	for _, w := range warnings {
		if strings.Contains(w, "healthcheck") {
			t.Errorf("o modelo da Ajuda gera aviso: %s", w)
		}
	}
	if rs := rollingServices(out, "web"); len(rs) != 1 || !rs[0].drain {
		t.Errorf("o modelo nao troca com drenagem: %+v", rs)
	}
}

func TestGuide(t *testing.T) {
	g := Guide(GuideInput{App: "web", Host: "web.exemplo.com.br", Networks: []string{"postgres_network", "garage_network"},
		Env: []string{"DATABASE_URL"}, Databases: []DBLink{{Engine: "postgres", Name: "loja"}}, Buckets: []BucketLink{{Name: "fotos"}}})
	for _, want := range []string{
		"traefik.http.routers.web.rule: Host(`${DOMINIO:?defina DOMINIO nas variaveis do app}`)",
		"ex: web.exemplo.com.br",
		"traefik.http.routers.web.entrypoints: websecure",
		"proxy_network:\n    external: true",
		"postgres_network:\n    external: true",
		`entrypoint: ["/cais", "run", "--", "node", "server.js"]`,
		"${CAIS_BIN}:/cais:ro",
		"${CAIS_ENV}:/run/cais/env:ro",
		"docker compose -p web exec web /cais run -- <comando>",
		"POSTGRES_LOJA_USER, POSTGRES_LOJA_PASS e\n#   POSTGRES_LOJA_URL",
		"S3_FOTOS_ACCESS_KEY, S3_FOTOS_SECRET_KEY e S3_FOTOS_ENDPOINT",
		"Variaveis do app: DATABASE_URL.",
		"secrets: {npm_token: {environment: NPM_TOKEN}}",
		"postgres:5432",
	} {
		if !strings.Contains(g, want) {
			t.Errorf("guia sem %q:\n%s", want, g)
		}
	}
	for _, notWant := range []string{"env_file", "CAIS_SECRETS", "/run/secrets/<nome>"} {
		if strings.Contains(g, notWant) {
			t.Errorf("guia ainda fala de %q:\n%s", notWant, g)
		}
	}
}

func TestDeployObservaOsContainersDepoisDoUp(t *testing.T) {
	m, _, d := newManager(t)
	m.Settle = 30 * time.Millisecond
	m.Create(App{Name: "web", Repo: "o/web"})
	d.ps = map[string][]docker.Container{
		"v1": {{Name: "web-init-1", State: "exited", ExitCode: 0}, {Name: "web-web-1", State: "running"}},
		"v2": {{Name: "web-web-1", State: "restarting", Status: "Restarting (1) 2 seconds ago"}},
	}
	if _, err := m.Deploy(context.Background(), "web", "c1"); err != nil {
		t.Fatalf("init que saiu com 0 nao e falha: %v", err)
	}
	d.calls = nil
	_, err := m.Deploy(context.Background(), "web", "c2")
	if err == nil || !strings.Contains(err.Error(), "reinicia sem parar") || !strings.Contains(err.Error(), "restaurada") {
		t.Fatalf("erro %v", err)
	}
	if !slices.Equal(d.calls, []string{"config v2", "build v2", "up v2", "config v1", "build v1", "up v1"}) {
		t.Errorf("chamadas %v", d.calls)
	}

	d.ps["v2"] = []docker.Container{{Name: "web-web-1", State: "exited", ExitCode: 1}}
	if _, err := m.Deploy(context.Background(), "web", "c2"); err == nil || !strings.Contains(err.Error(), "parou com erro (codigo 1)") {
		t.Fatalf("erro %v", err)
	}
	if a, _ := m.Get("web"); a.Commit != "c1" {
		t.Errorf("commit no ar %s", a.Commit)
	}
}

func TestDeleteApagaOAppEDerrubaOProjeto(t *testing.T) {
	m, _, d := newManager(t)
	m.Create(App{Name: "web", Repo: "o/web"})
	m.SetEnv("web", map[string]string{"JWT": "x"})
	m.Deploy(context.Background(), "web", "")
	if err := m.Delete(context.Background(), "web", true); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(d.calls, "down web volumes=true") {
		t.Errorf("chamadas %v", d.calls)
	}
	if _, err := os.Stat(filepath.Join(m.Root, "web")); !os.IsNotExist(err) {
		t.Error("a pasta do app ficou")
	}
	if _, err := m.Get("web"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get depois de excluir: %v", err)
	}
	if err := m.Delete(context.Background(), "web", false); !errors.Is(err, ErrNotFound) {
		t.Errorf("excluir de novo: %v", err)
	}
}

// banco ligado vira variaveis do app: usuario, senha e a URL pronta, com a
// credencial lida a cada deploy
func TestBancoLigadoEntraNoDeploy(t *testing.T) {
	m, _, _ := newManager(t)
	pws := map[string]string{"postgres/loja": "s3nh4", "mysql/relatorios": "outra"}
	m.DBCredentials = func(engine, name string) (string, string, error) {
		if pw, ok := pws[engine+"/"+name]; ok {
			return name, pw, nil
		}
		return "", "", fmt.Errorf("o database %s/%s nao existe", engine, name)
	}
	m.Create(App{Name: "web", Repo: "o/web"})
	m.SetEnv("web", map[string]string{"POSTGRES_LOJA_PASS": "colada a mao"})

	if err := m.LinkDB("web", DBLink{Engine: "postgres", Name: "nao"}); err == nil {
		t.Error("ligou banco que nao existe")
	}
	if err := m.LinkDB("web", DBLink{Engine: "postgres", Name: "loja"}); err == nil || !strings.Contains(err.Error(), "ja tem a variavel POSTGRES_LOJA_PASS") {
		t.Fatalf("variavel repetida: %v", err)
	}
	m.SetEnv("web", map[string]string{"APP": "1"})
	if err := m.LinkDB("web", DBLink{Engine: "postgres", Name: "loja"}); err != nil {
		t.Fatal(err)
	}
	// cada banco tem as proprias variaveis: o app pode usar mais de um
	if err := m.LinkDB("web", DBLink{Engine: "mysql", Name: "relatorios"}); err != nil {
		t.Fatal(err)
	}
	if err := m.SetEnv("web", map[string]string{"POSTGRES_LOJA_USER": "x"}); err == nil || !strings.Contains(err.Error(), "postgres/loja") {
		t.Errorf("sobrescreveu o usuario do banco a mao: %v", err)
	}
	a, _ := m.Get("web")
	if got := InfraVars(a); len(got) != 6 || got[2] != "POSTGRES_LOJA_URL" || got[5] != "MYSQL_RELATORIOS_URL" {
		t.Errorf("variaveis da infra %v", got)
	}

	// a senha mudou no cais (com caracteres que a URL escapa): o proximo
	// deploy ja leva a nova
	pws["postgres/loja"] = "p@ss/w:rd"
	if _, err := m.Deploy(context.Background(), "web", ""); err != nil {
		t.Fatal(err)
	}
	env := runEnv(t, m, "web")
	want := map[string]string{"APP": "1",
		"POSTGRES_LOJA_USER": "loja", "POSTGRES_LOJA_PASS": "p@ss/w:rd", "POSTGRES_LOJA_URL": "postgres://loja:p%40ss%2Fw%3Ard@postgres:5432/loja",
		"MYSQL_RELATORIOS_USER": "relatorios", "MYSQL_RELATORIOS_PASS": "outra", "MYSQL_RELATORIOS_URL": "mysql://relatorios:outra@mysql:3306/relatorios"}
	if !maps.Equal(env, want) {
		t.Errorf("run.env %v\nqueria %v", env, want)
	}

	if err := m.UnlinkDB("web", "postgres", "loja"); err != nil {
		t.Fatal(err)
	}
	if a, _ := m.Get("web"); len(a.Databases) != 1 {
		t.Errorf("bancos ligados: %+v", a.Databases)
	}
	m.Deploy(context.Background(), "web", "")
	if env := runEnv(t, m, "web"); env["POSTGRES_LOJA_PASS"] != "" || env["MYSQL_RELATORIOS_PASS"] != "outra" {
		t.Errorf("depois de desligar o postgres: %v", env)
	}
}

func TestBucketLigadoEntraNoDeploy(t *testing.T) {
	m, _, _ := newManager(t)
	keys := map[string][2]string{"portal-fotos": {"GK1", "s3cr3t"}}
	m.BucketCredentials = func(name string) (string, string, error) {
		if k, ok := keys[name]; ok {
			return k[0], k[1], nil
		}
		return "", "", fmt.Errorf("o bucket %s nao existe", name)
	}
	m.Create(App{Name: "web", Repo: "o/web"})
	if err := m.LinkBucket("web", BucketLink{Name: "nao"}); err == nil {
		t.Error("ligou bucket que nao existe")
	}
	if err := m.LinkBucket("web", BucketLink{Name: "portal-fotos"}); err != nil {
		t.Fatal(err)
	}
	if err := m.SetEnv("web", map[string]string{"S3_PORTAL_FOTOS_SECRET_KEY": "x"}); err == nil {
		t.Error("gravou a chave do bucket como variavel comum")
	}

	// chave nova no cais: o proximo deploy ja leva
	keys["portal-fotos"] = [2]string{"GK2", "nova"}
	if _, err := m.Deploy(context.Background(), "web", ""); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"S3_PORTAL_FOTOS_ACCESS_KEY": "GK2", "S3_PORTAL_FOTOS_SECRET_KEY": "nova", "S3_PORTAL_FOTOS_ENDPOINT": "http://garage:3900"}
	if env := runEnv(t, m, "web"); !maps.Equal(env, want) {
		t.Errorf("run.env %v", env)
	}

	if err := m.UnlinkBucket("web", "portal-fotos"); err != nil {
		t.Fatal(err)
	}
	m.Deploy(context.Background(), "web", "")
	if env := runEnv(t, m, "web"); len(env) != 0 {
		t.Errorf("as variaveis do bucket ficaram: %v", env)
	}
}

func TestDeployTrocaSemQuedaOsServicesDoTraefikComHealthcheck(t *testing.T) {
	m, _, d := newManager(t)
	m.Create(App{Name: "web", Repo: "o/web"})
	hc := `"networks": {"proxy_network": {}}, "healthcheck": {"test": ["CMD", "true"]}`
	d.config = `{"services": {
		"app":    {"labels": {"traefik.enable": "true"}, ` + hc + `},
		"api":    {"image": "ghcr.io/o/api:1", "labels": {"traefik.enable": "true"}, "networks": {"proxy_network": {}}, "healthcheck": {"test": ["CMD-SHELL", "test ! -f /tmp/drain && wget -qO- http://127.0.0.1:3000/"]}},
		"semhc":  {"labels": {"traefik.enable": "true"}, "networks": {"proxy_network": {}}},
		"desl":   {"labels": {"traefik.enable": "true"}, "networks": {"proxy_network": {}}, "healthcheck": {"disable": true}},
		"worker": {` + hc + `},
		"db":     {}
	}, "networks": {"proxy_network": {"name": "proxy_network", "external": true}}}`
	if _, err := m.Deploy(context.Background(), "web", "c1"); err != nil {
		t.Fatal(err)
	}
	want := []string{"config v1", "build v1", "rollout api ghcr.io/o/api:1 drain v1", "rollout app web-app v1", "up v1", "prune web"}
	if !slices.Equal(d.calls, want) {
		t.Errorf("chamadas %v\nesperava %v", d.calls, want)
	}
}

func TestDeployComTrocaQueFalhaVoltaAAnterior(t *testing.T) {
	m, _, d := newManager(t)
	m.Create(App{Name: "web", Repo: "o/web"})
	d.config = `{"services": {"app": {"labels": {"traefik.enable": "true"}, "networks": {"proxy_network": {}}, "healthcheck": {"test": ["CMD", "true"]}}},
		"networks": {"proxy_network": {"name": "proxy_network", "external": true}}}`
	if _, err := m.Deploy(context.Background(), "web", "c1"); err != nil {
		t.Fatal(err)
	}
	d.calls = nil
	d.failRollout = map[string]bool{"v2": true}

	_, err := m.Deploy(context.Background(), "web", "c2")
	if err == nil || !strings.Contains(err.Error(), "unhealthy") || !strings.Contains(err.Error(), "restaurada") {
		t.Fatalf("erro %v", err)
	}
	// o up da v2 nem roda; a anterior volta (rebuild pra imagem voltar a ser a dela)
	if !slices.Equal(d.calls, []string{"config v2", "build v2", "rollout app web-app v2", "config v1", "build v1", "up v1"}) {
		t.Errorf("chamadas %v", d.calls)
	}
	if a, _ := m.Get("web"); a.Commit != "c1" || !a.Last.RolledBack {
		t.Errorf("app %+v / %+v", a, a.Last)
	}
}

func TestHistoricoERollbackComImagensGuardadas(t *testing.T) {
	m, src, d := newManager(t)
	d.images = map[string]string{}
	d.config = `{"services": {"web": {"build": {"context": "{{src}}"}, "logging": {"options": {"max-size": "1m"}}}}}`
	src.commits["c3"] = map[string]string{"docker-compose.yml": "v3"}
	m.Create(App{Name: "web", Repo: "o/web", AutoDeploy: true})
	ctx := context.Background()
	for _, c := range []string{"c1", "c2", "c3"} {
		if _, err := m.Deploy(ctx, "web", c); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := m.Get("web")
	if len(a.History) != 3 || a.History[0].Commit != "c3" || a.History[2].Commit != "c1" {
		t.Fatalf("historico %+v", a.History)
	}
	// cada deploy guardou a imagem do seu commit
	for _, c := range []string{"c1", "c2", "c3"} {
		if d.images["web-web:cais-"+c] != "img-v"+c[1:] {
			t.Errorf("imagem guardada de %s: %v", c, d.images)
		}
	}

	// sem commit: volta pro anterior ao que esta no ar, sem build
	src.head = "c3"
	d.calls = nil
	res, err := m.Rollback(ctx, "web", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Commit != "c2" || !res.Images {
		t.Errorf("rollback %+v", res)
	}
	for _, c := range d.calls {
		if strings.HasPrefix(c, "build") {
			t.Errorf("fez build no rollback: %v", d.calls)
		}
	}
	if d.images["web-web"] != "img-v2" {
		t.Errorf("a imagem no ar nao e a de c2: %v", d.images)
	}
	a, _ = m.Get("web")
	if a.Commit != "c2" || !a.History[0].Rollback || !a.History[0].Images || a.AutoHold != "c3" {
		t.Errorf("app depois do rollback %+v / %+v", a, a.History[0])
	}

	// um deploy normal libera o automatico de novo
	if _, err := m.Deploy(ctx, "web", "c3"); err != nil {
		t.Fatal(err)
	}
	if a, _ := m.Get("web"); a.AutoHold != "" {
		t.Errorf("hold ficou: %q", a.AutoHold)
	}

	// erros: commit fora do historico, commit que esta no ar
	if _, err := m.Rollback(ctx, "web", "deadbeef"); err == nil || !strings.Contains(err.Error(), "-ref deadbeef") {
		t.Errorf("commit fora do historico: %v", err)
	}
	if _, err := m.Rollback(ctx, "web", "c3"); err == nil || !strings.Contains(err.Error(), "ja esta no ar") {
		t.Errorf("rollback pro commit no ar: %v", err)
	}
}

func TestRollbackSemImagemGuardadaFazBuild(t *testing.T) {
	m, _, d := newManager(t)
	d.images = map[string]string{}
	d.config = `{"services": {"web": {"build": {"context": "{{src}}"}, "logging": {"options": {"max-size": "1m"}}}}}`
	m.Create(App{Name: "web", Repo: "o/web"})
	ctx := context.Background()
	m.Deploy(ctx, "web", "c1")
	m.Deploy(ctx, "web", "c2")
	delete(d.images, "web-web:cais-c1") // limpa por fora, por exemplo
	d.calls = nil
	res, err := m.Rollback(ctx, "web", "c1")
	if err != nil || res.Images || !slices.Contains(d.calls, "build v1") {
		t.Fatalf("rollback %+v %v %v", res, err, d.calls)
	}
}

func TestImagensGuardadasSoDosUltimosDeploys(t *testing.T) {
	m, src, d := newManager(t)
	d.images = map[string]string{}
	d.config = `{"services": {"web": {"build": {"context": "{{src}}"}, "logging": {"options": {"max-size": "1m"}}}}}`
	m.Create(App{Name: "web", Repo: "o/web"})
	ctx := context.Background()
	for i := 1; i <= KeepImages+2; i++ {
		c := fmt.Sprintf("c%d", i)
		src.commits[c] = map[string]string{"docker-compose.yml": fmt.Sprintf("v%d", i)}
		if _, err := m.Deploy(ctx, "web", c); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= KeepImages+2; i++ {
		_, ok := d.images[fmt.Sprintf("web-web:cais-c%d", i)]
		if want := i > 2; ok != want {
			t.Errorf("imagem de c%d guardada: %v", i, ok)
		}
	}
	a, _ := m.Get("web")
	if len(a.History) != KeepImages+2 {
		t.Errorf("historico com %d", len(a.History))
	}
}

func TestCommitTag(t *testing.T) {
	cases := map[string]string{
		"web-web":                        "web-web:cais-0123456789ab",
		"ghcr.io/o/app:1.2":              "ghcr.io/o/app:cais-0123456789ab",
		"registry.local:5000/app":        "registry.local:5000/app:cais-0123456789ab",
		"registry.local:5000/app:latest": "registry.local:5000/app:cais-0123456789ab",
	}
	for img, want := range cases {
		if got := commitTag(img, "0123456789abcdef"); got != want {
			t.Errorf("%s: %s", img, got)
		}
	}
}

// interrupted deixa o app como um deploy de c2 cortado no meio da troca
// (o daemon parou depois de trocar as pastas e antes do registro): c2 em
// src, c1 em src.prev, a marca da troca e o app.yaml ainda em c1.
func interrupted(t *testing.T, m Manager, d *fakeDocker) {
	t.Helper()
	m.Create(App{Name: "web", Repo: "o/web"})
	for _, c := range []string{"c1", "c2"} {
		if _, err := m.Deploy(context.Background(), "web", c); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := m.Get("web")
	a.Commit, a.Last, a.History = "c1", &a.History[1], a.History[1:]
	m.save(a)
	if err := m.markSwap(a, Deploy{Commit: "c2", Auto: true}); err != nil {
		t.Fatal(err)
	}
	d.calls = nil
}

func TestRecoverVoltaAVersaoNoArDeUmDeployInterrompido(t *testing.T) {
	m, _, d := newManager(t)
	interrupted(t, m, d)
	if !m.Interrupted("web") {
		t.Fatal("a marca da troca nao foi vista")
	}

	ok, err := m.Recover(context.Background(), "web")
	if !ok || err != nil {
		t.Fatalf("recover %v %v", ok, err)
	}
	if !slices.Equal(d.calls, []string{"config v1", "build v1", "up v1"}) {
		t.Errorf("chamadas %v", d.calls)
	}
	if data, _ := os.ReadFile(filepath.Join(m.Root, "web", SrcDir, DefaultCompose)); string(data) != "v1" {
		t.Errorf("src ficou com %q", data)
	}
	a, _ := m.Get("web")
	if a.Commit != "c1" || !a.Last.Interrupted || !a.Last.RolledBack || a.Last.Commit != "c2" || !a.Last.Auto {
		t.Errorf("registro %+v", a.Last)
	}
	if m.Interrupted("web") {
		t.Error("a marca ficou")
	}
	// o automatico tenta o commit interrompido mais uma vez
	if a.Tried("c2", m.Now()) {
		t.Error("interrompido uma vez nao deveria contar como tentado")
	}
	// nada mais pra recuperar
	if ok, _ := m.Recover(context.Background(), "web"); ok {
		t.Error("recuperou duas vezes")
	}
}

func TestRecoverComODaemonParadoEntreAsPastas(t *testing.T) {
	m, _, d := newManager(t)
	interrupted(t, m, d)
	// src ja tinha virado src.prev, e o novo ainda nao tinha virado src
	os.RemoveAll(filepath.Join(m.Root, "web", SrcDir))

	if _, err := m.Recover(context.Background(), "web"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(m.Root, "web", SrcDir, DefaultCompose)); string(data) != "v1" {
		t.Errorf("src ficou com %q", data)
	}
}

// o deploy seguinte (sem passar pelo Recover da subida) parte da versao
// boa: se ele falhar, a volta e pra c1, nunca pro c2 que nao confirmou
func TestDeployDepoisDeInterrompidoVoltaParaAVersaoBoa(t *testing.T) {
	m, src, d := newManager(t)
	src.commits["c3"] = map[string]string{"docker-compose.yml": "v3"}
	interrupted(t, m, d)
	d.failUp["v3"] = true

	_, err := m.Deploy(context.Background(), "web", "c3")
	if err == nil || !strings.Contains(err.Error(), "(c1) foi restaurada") {
		t.Fatalf("erro %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(m.Root, "web", SrcDir, DefaultCompose)); string(data) != "v1" {
		t.Errorf("voltou pra %q", data)
	}
	a, _ := m.Get("web")
	if len(a.History) < 2 || !a.History[1].Interrupted || a.History[1].Commit != "c2" {
		t.Errorf("historico %+v", a.History)
	}
}

func TestTriedTentaOInterrompidoUmaVezSo(t *testing.T) {
	failed := Deploy{Commit: "c2", Error: "x"}
	cut := Deploy{Commit: "c2", Error: "x", Interrupted: true}
	for _, tc := range []struct {
		hist []Deploy
		want bool
	}{
		{nil, false},
		{[]Deploy{{Commit: "c1"}}, false},
		{[]Deploy{failed}, true},
		{[]Deploy{cut, {Commit: "c1"}}, false},
		{[]Deploy{cut, cut}, true},
	} {
		a := App{History: tc.hist}
		if len(tc.hist) > 0 {
			a.Last = &tc.hist[0]
		}
		if got := a.Tried("c2", time.Now()); got != tc.want {
			t.Errorf("%+v: %v", tc.hist, got)
		}
	}
}

func TestFalhaPassageiraOuCulpaDoCommit(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{errors.New("docker compose build (web): failed to resolve source metadata for docker.io/library/node:24: 429 Too Many Requests - toomanyrequests: You have reached your pull rate limit"), true},
		{errors.New("npm error code ETIMEDOUT\nnpm error network request to https://registry.npmjs.org/x failed"), true},
		{errors.New("Get \"https://registry-1.docker.io/v2/\": net/http: TLS handshake timeout"), true},
		{errors.New("dial tcp: lookup registry-1.docker.io: Temporary failure in name resolution"), true},
		{errors.New("docker compose build (web): process \"/bin/sh -c npm run build\" did not complete successfully: exit code: 1"), false},
		{errors.New("container web is unhealthy"), false},
		{&ComposeError{App: "web", Problems: []string{"publica a porta 80 (connection refused)"}}, false},
		{nil, false},
	} {
		if got := transient(tc.err); got != tc.want {
			t.Errorf("%v: %v", tc.err, got)
		}
	}
}

// o deploy registra se a falha e passageira: o build que nao baixou a
// imagem base sim; o app que nao fica de pe nao
func TestDeployRegistraFalhaPassageira(t *testing.T) {
	m, _, d := newManager(t)
	m.Create(App{Name: "web", Repo: "o/web"})
	d.failBuild = map[string]error{"v1": errors.New("failed to resolve source metadata: 503 Service Unavailable")}
	if _, err := m.Deploy(context.Background(), "web", "c1"); err == nil {
		t.Fatal("deploy passou")
	}
	if a, _ := m.Get("web"); !a.Last.Transient {
		t.Errorf("build sem registry nao ficou passageiro: %+v", a.Last)
	}

	d.failBuild = nil
	d.failUp["v1"] = true
	m.Deploy(context.Background(), "web", "c1")
	if a, _ := m.Get("web"); a.Last.Transient || a.Last.Error == "" {
		t.Errorf("app unhealthy ficou passageiro: %+v", a.Last)
	}
}

func TestAutoRetryAtEsperaCadaVezMais(t *testing.T) {
	t0 := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	pass := func(min int) Deploy {
		return Deploy{Commit: "c2", At: t0.Add(time.Duration(min) * time.Minute), Error: "x", Transient: true}
	}
	app := func(hist ...Deploy) App { return App{Last: &hist[0], History: hist} }
	ok := Deploy{Commit: "c1", At: t0.Add(-time.Hour)}

	for _, tc := range []struct {
		name  string
		a     App
		at    time.Time
		retry bool
	}{
		{"primeira falha: 2 min", app(pass(0), ok), t0.Add(2 * time.Minute), true},
		{"segunda: 10 min", app(pass(2), pass(0), ok), t0.Add(12 * time.Minute), true},
		{"terceira: 30 min", app(pass(12), pass(2), pass(0), ok), t0.Add(42 * time.Minute), true},
		{"quarta: desiste", app(pass(42), pass(12), pass(2), pass(0), ok), time.Time{}, false},
		{"culpa do commit: nao tenta", app(Deploy{Commit: "c2", At: t0, Error: "x"}, ok), time.Time{}, false},
		{"passageira depois de uma de culpa do commit conta as duas", app(pass(5), Deploy{Commit: "c2", At: t0, Error: "x"}, ok), t0.Add(15 * time.Minute), true},
		{"deu certo: nada", app(ok), time.Time{}, false},
	} {
		at, retry := tc.a.AutoRetryAt()
		if retry != tc.retry || !at.Equal(tc.at) {
			t.Errorf("%s: %v %v", tc.name, at, retry)
		}
	}
	a := app(pass(0), ok)
	if !a.Tried("c2", t0.Add(time.Minute)) || a.Tried("c2", t0.Add(2*time.Minute)) {
		t.Error("Tried nao respeita a espera")
	}
}

// build travado: o limite cancela, o motivo e claro, a versao no ar nem foi
// tocada (o build roda antes da troca) e a falha conta como passageira
func TestDeployComBuildTravadoPassaDoLimite(t *testing.T) {
	m, _, d := newManager(t)
	m.Create(App{Name: "web", Repo: "o/web"})
	if _, err := m.Deploy(context.Background(), "web", "c1"); err != nil {
		t.Fatal(err)
	}
	m.Timeout = 50 * time.Millisecond
	d.hang = map[string]bool{"build v2": true}
	d.calls = nil

	_, err := m.Deploy(context.Background(), "web", "c2")
	var te *TimeoutError
	if !errors.As(err, &te) || !strings.Contains(err.Error(), "passou do limite de 50ms") {
		t.Fatalf("erro %v", err)
	}
	if !slices.Equal(d.calls, []string{"config v2", "build v2"}) {
		t.Errorf("chamadas %v", d.calls)
	}
	a, _ := m.Get("web")
	if a.Commit != "c1" || !a.Last.Transient || a.Last.RolledBack {
		t.Errorf("registro %+v", a.Last)
	}
	if data, _ := os.ReadFile(filepath.Join(m.Root, "web", SrcDir, DefaultCompose)); string(data) != "v1" {
		t.Errorf("src mudou pra %q", data)
	}
	if _, err := os.Stat(filepath.Join(m.Root, "web", NextDir)); !os.IsNotExist(err) {
		t.Error("src.next ficou")
	}
}

// up travado depois da troca: o limite cancela e a volta da versao anterior
// roda com um ctx proprio (nao o que ja acabou)
func TestDeployComUpTravadoVoltaAAnteriorDepoisDoLimite(t *testing.T) {
	m, _, d := newManager(t)
	m.Create(App{Name: "web", Repo: "o/web"})
	if _, err := m.Deploy(context.Background(), "web", "c1"); err != nil {
		t.Fatal(err)
	}
	m.Timeout = 50 * time.Millisecond
	d.hang = map[string]bool{"up v2": true}
	d.calls = nil

	_, err := m.Deploy(context.Background(), "web", "c2")
	if err == nil || !strings.Contains(err.Error(), "passou do limite") || !strings.Contains(err.Error(), "(c1) foi restaurada") {
		t.Fatalf("erro %v", err)
	}
	if !slices.Equal(d.calls, []string{"config v2", "build v2", "up v2", "config v1", "build v1", "up v1"}) {
		t.Errorf("chamadas %v", d.calls)
	}
	if len(d.canceled) > 0 {
		t.Errorf("a volta rodou com o ctx cancelado: %v", d.canceled)
	}
	a, _ := m.Get("web")
	if a.Commit != "c1" || !a.Last.RolledBack || !a.Last.Transient {
		t.Errorf("registro %+v", a.Last)
	}
}

// volume do app nao entra no backup: o deploy avisa (sem bloquear), com os
// nomes; compose sem volume nao ganha o aviso
func TestCheckComposeAvisaVolumeForaDoBackup(t *testing.T) {
	r := Rules{App: "web", Src: "/x", Networks: []string{ProxyNetwork}}
	logging := `"logging": {"driver": "json-file", "options": {"max-size": "20m"}}`
	with := []byte(`{"services": {"web": {` + logging + `, "volumes": [{"type": "volume", "source": "uploads", "target": "/data"}]}},
		"volumes": {"uploads": {"name": "web_uploads"}, "cache": {"name": "web_cache"}}}`)
	problems, w := CheckCompose(with, r)
	if len(problems) != 0 {
		t.Fatalf("problemas %v", problems)
	}
	if len(w) != 1 || !strings.Contains(w[0], "cache, uploads nao entram no backup") {
		t.Errorf("avisos %v", w)
	}
	without := []byte(`{"services": {"web": {` + logging + `}}}`)
	if _, w := CheckCompose(without, r); len(w) != 0 {
		t.Errorf("aviso sem volume: %v", w)
	}
}

// com o proxy desligado, app com dominio e recusado com o aviso de ligar o
// proxy; app sem dominio (worker) passa
func TestCheckComposeSemProxyLigado(t *testing.T) {
	web := `{"services": {"web": {"image": "x", "labels": {"traefik.enable": "true"},
		"networks": {"default": null, "proxy_network": null}}},
		"networks": {"default": {"name": "web_default"}, "proxy_network": {"name": "proxy_network", "external": true}}}`
	problems, _ := CheckCompose([]byte(web), Rules{App: "web", Src: "/x"})
	if len(problems) == 0 || !strings.Contains(strings.Join(problems, "\n"), "Ligue o proxy na aba Infra") {
		t.Fatalf("app com dominio sem proxy: %v", problems)
	}
	if problems, _ := CheckCompose([]byte(web), Rules{App: "web", Src: "/x", Networks: []string{ProxyNetwork}}); len(problems) != 0 {
		t.Fatalf("app com dominio com proxy: %v", problems)
	}
	worker := `{"services": {"worker": {"image": "x"}}}`
	if problems, _ := CheckCompose([]byte(worker), Rules{App: "web", Src: "/x"}); len(problems) != 0 {
		t.Fatalf("worker sem proxy: %v", problems)
	}
}

// segredo do build: build secret vindo de variavel do app passa sem aviso;
// de arquivo e recusado; build arg com nome de segredo avisa (fica na imagem)
func TestCheckComposeBuildSecret(t *testing.T) {
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, ".dockerignore"), []byte(".env\n"), 0o644)
	r := Rules{App: "web", Src: src, DotEnv: filepath.Join(src, ".env")}
	cfg := func(build, secret string) []byte {
		return []byte(`{"services": {"web": {"build": {"context": "` + src + `"` + build + `},
			"logging": {"driver": "json-file", "options": {"max-size": "20m", "max-file": "5"}}}},
			"secrets": {"npm_token": {"name": "web_npm_token", ` + secret + `}}}`)
	}

	p, w := CheckCompose(cfg(`, "secrets": [{"source": "npm_token"}]`, `"environment": "NPM_TOKEN"`), r)
	if len(p) != 0 || len(w) != 0 {
		t.Fatalf("build secret de variavel: problemas %v avisos %v", p, w)
	}
	if p, _ := CheckCompose(cfg(`, "secrets": [{"source": "npm_token"}]`, `"file": "/root/.npmrc"`), r); len(p) != 1 || !strings.Contains(p[0], "environment: NPM_TOKEN") {
		t.Errorf("build secret de arquivo: %v", p)
	}

	_, w = CheckCompose(cfg(`, "args": {"NPM_TOKEN": "x", "NODE_ENV": "production", "STRIPE_API_KEY": null, "PASSENGER": "1", "NEXT_PUBLIC_MAPBOX_ACCESS_TOKEN": "pk", "VITE_API_KEY": "x"}`, `"environment": "NPM_TOKEN"`), r)
	if len(w) != 2 || !strings.Contains(w[0], "NPM_TOKEN parece segredo") || !strings.Contains(w[0], "id=npm_token") ||
		!strings.Contains(w[1], "STRIPE_API_KEY parece segredo") {
		t.Errorf("avisos dos build args %v", w)
	}
}

// Um compose solto da VPS com o mesmo nome de projeto (de antes da
// migracao) para o deploy antes do build: o up trocaria os containers dele e
// o --remove-orphans apagaria o banco antigo. Os do proprio cais passam.
func TestDeployRecusaProjetoForaDoCais(t *testing.T) {
	m, _, d := newManager(t)
	m.Create(App{Name: "web", Repo: "o/web"})
	d.ps = map[string][]docker.Container{"v1": {
		{Name: "web-backend-1", Service: "backend", State: "running", ConfigFiles: "/root/web/docker-compose.yml"},
		{Name: "web-db-1", Service: "db", State: "running", ConfigFiles: "/root/web/docker-compose.yml"},
	}}

	_, err := m.Deploy(context.Background(), "web", "c1")
	var fe *ForeignProjectError
	if !errors.As(err, &fe) || fe.Files != "/root/web/docker-compose.yml" || len(fe.Containers) != 2 {
		t.Fatalf("erro %v", err)
	}
	if !strings.Contains(err.Error(), "docker compose -p web down") || !strings.Contains(err.Error(), "nada foi feito") {
		t.Errorf("mensagem %v", err)
	}
	if slices.ContainsFunc(d.calls, func(c string) bool { return strings.HasPrefix(c, "build") || strings.HasPrefix(c, "up") }) {
		t.Errorf("buildou ou subiu: %v", d.calls)
	}

	// os containers do proprio cais (a pasta do app) nao contam
	own := filepath.Join(m.dir("web"), SrcDir, "docker-compose.yml")
	d.ps = map[string][]docker.Container{"v1": {{Name: "web-web-1", State: "running", ConfigFiles: own}}}
	if _, err := m.Deploy(context.Background(), "web", "c1"); err != nil {
		t.Fatalf("deploy com os containers do cais: %v", err)
	}
}
