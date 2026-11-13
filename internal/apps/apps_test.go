package apps

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	envSeen     map[string]string             // .env lido no Config
	ps          map[string][]docker.Container // compose -> containers depois do up
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
	return []byte(f.config), nil
}

func (f *fakeDocker) Build(_ context.Context, p docker.Project) error {
	f.calls = append(f.calls, "build "+f.compose(p))
	return nil
}

func (f *fakeDocker) Up(_ context.Context, p docker.Project) error {
	c := f.compose(p)
	f.calls = append(f.calls, "up "+c)
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
	return Manager{Root: filepath.Join(t.TempDir(), "apps"), Source: src, Docker: d, Now: func() time.Time { return now }}, src, d
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
	info, _ := os.Stat(m.SecretsPath("a"))
	if info == nil || info.Mode().Perm() != 0o700 {
		t.Errorf("pasta de secrets %v", info)
	}
	if _, err := m.Get("c"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get de app que nao existe: %v", err)
	}
}

func TestDeployGravaEnvESobe(t *testing.T) {
	m, src, d := newManager(t)
	m.Create(App{Name: "web", Repo: "o/web", Branch: "main"})
	if err := m.SetEnv("web", map[string]string{"NODE_ENV": "production", "TOKEN": "a$b'c"}); err == nil {
		t.Error("aceitou valor com $ e aspa simples juntos")
	}
	if err := m.SetEnv("web", map[string]string{"NODE_ENV": "production", "PRECO": "R$10"}); err != nil {
		t.Fatal(err)
	}
	m.SetSecret("web", "jwt", []byte("segredo"))

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
	if d.envSeen["NODE_ENV"] != "production" || d.envSeen["PRECO"] != "R$10" || d.envSeen["CAIS_COMMIT"] != "c1" ||
		d.envSeen["CAIS_APP"] != "web" || d.envSeen["CAIS_SECRETS"] != m.SecretsPath("web") {
		t.Errorf(".env do projeto %v", d.envSeen)
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

func TestDeployComposeNumaSubpasta(t *testing.T) {
	m, src, d := newManager(t)
	src.commits["c1"] = map[string]string{"deploy/prod.yml": "sub"}
	m.Create(App{Name: "web", Repo: "o/web", Compose: "deploy/prod.yml"})
	m.SetEnv("web", map[string]string{"A": "1"})
	if _, err := m.Deploy(context.Background(), "web", ""); err != nil {
		t.Fatal(err)
	}
	// o compose le o .env da pasta do arquivo
	if d.envSeen["A"] != "1" {
		t.Errorf(".env nao ficou ao lado do compose: %v", d.envSeen)
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
	if !slices.Equal(d.calls, []string{"config v2", "build v2", "up v2", "build v1", "up v1"}) {
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

func TestSecrets(t *testing.T) {
	m, _, _ := newManager(t)
	m.Create(App{Name: "web", Repo: "o/web"})
	if err := m.SetSecret("web", "../fora", []byte("x")); err == nil {
		t.Error("aceitou nome de secret com caminho")
	}
	if err := m.SetSecret("web", "jwt", nil); err == nil {
		t.Error("aceitou secret vazio")
	}
	m.SetSecret("web", "jwt", []byte("a"))
	m.SetSecret("web", "db_url", []byte("b"))
	list, _ := m.Secrets("web")
	if len(list) != 2 || list[0].Name != "db_url" {
		t.Errorf("secrets %v", list)
	}
	m.DeleteSecret("web", "jwt")
	if list, _ := m.Secrets("web"); len(list) != 1 {
		t.Errorf("depois de apagar %v", list)
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
	src, secretsDir := "/var/lib/cais/apps/web/src.next", "/var/lib/cais/apps/web/secrets"
	r := Rules{App: "web", Src: src, SecretsDir: secretsDir, Networks: []string{"postgres_network"}, Apps: []string{"web-homolog"}}
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
		"env_file fora":     {`"env_file": [{"path": "/etc/shadow"}]`, "", "env_file: .env"},
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
		"secret fora":       {"", `"secrets": {"chave": {"file": "/root/.ssh/id_rsa"}}`, "cais app secret web chave"},
		"config fora":       {"", `"configs": {"c": {"file": "/etc/hosts"}}`, "fora do app"},
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
	src, secretsDir := "/var/lib/cais/apps/web/src.next", "/var/lib/cais/apps/web/secrets"
	cfg := `{"services": {
		"web": {"build": {"context": "` + src + `/app", "dockerfile": "Dockerfile"},
			"env_file": [{"path": "` + src + `/.env"}],
			"labels": {"traefik.enable": "true", "traefik.http.routers.web.rule": "Host(` + "`web.x`" + `)",
				"traefik.http.routers.web-api.rule": "PathPrefix(` + "`/api`" + `)", "traefik.http.services.web.loadbalancer.server.port": "3000"},
			"networks": {"default": null, "proxy_network": null, "postgres_network": null},
			"healthcheck": {"test": ["CMD-SHELL", "test ! -f /tmp/drain && wget -qO- http://127.0.0.1:3000/"]},
			"logging": {"driver": "json-file", "options": {"max-size": "20m", "max-file": "5"}},
			"volumes": [{"type": "volume", "source": "dados", "target": "/data"},
				{"type": "bind", "source": "` + src + `/worker", "target": "/worker", "read_only": true},
				{"type": "bind", "source": "` + secretsDir + `/jwt", "target": "/jwt", "read_only": true}]},
		"init": {"image": "alpine", "logging": {"driver": "json-file", "options": {"max-size": "20m", "max-file": "5"}}, "volumes": [{"type": "bind", "source": "` + src + `/data"}]}},
	"networks": {"default": {"name": "web_default"}, "proxy_network": {"name": "proxy_network", "external": true},
		"postgres_network": {"name": "postgres_network", "external": true}},
	"volumes": {"dados": {"name": "web_dados"}},
	"secrets": {"jwt": {"file": "` + secretsDir + `/jwt"}, "env": {}}}`
	problems, warnings := CheckCompose([]byte(cfg), Rules{App: "web", Src: src, SecretsDir: secretsDir, Networks: []string{"postgres_network"}})
	if len(problems) != 0 {
		t.Fatalf("recusou o formato do cais: %v", problems)
	}
	// bind com escrita no codigo: os dados somem no proximo deploy
	if len(warnings) != 1 || !strings.Contains(warnings[0], "some no proximo deploy") {
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
	r := Rules{App: "web", Src: "/x"}
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
	src, secretsDir := filepath.Join(root, "src"), filepath.Join(root, "secrets")
	os.MkdirAll(src, 0o755)
	os.MkdirAll(secretsDir, 0o700)
	os.WriteFile(filepath.Join(secretsDir, "jwt"), []byte("x"), 0o444)
	os.WriteFile(filepath.Join(src, ".env"), []byte("CAIS_SECRETS="+secretsDir+"\nDATABASE_URL=x\nDOMINIO=web.exemplo.com.br\n"), 0o600)
	g := Guide(GuideInput{App: "web", Host: "web.exemplo.com.br", Networks: []string{"postgres_network"},
		Env: []string{"DATABASE_URL"}, Secrets: []string{"jwt"}})
	os.WriteFile(filepath.Join(src, "docker-compose.yml"), []byte(g), 0o644)
	cmd := exec.Command("docker", "compose", "-p", "web", "-f", filepath.Join(src, "docker-compose.yml"), "config", "--format", "json", "--no-env-resolution")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("o modelo nao e um compose valido: %v\n%s", err, g)
	}
	problems, warnings := CheckCompose(out, Rules{App: "web", Src: src, SecretsDir: secretsDir, Networks: []string{"postgres_network"}})
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
	g := Guide(GuideInput{App: "web", Host: "web.exemplo.com.br", Networks: []string{"postgres_network"},
		Env: []string{"DATABASE_URL"}, Secrets: []string{"jwt"}})
	for _, want := range []string{
		"traefik.http.routers.web.rule: Host(`${DOMINIO:?defina DOMINIO nas variaveis do app}`)",
		"ex: web.exemplo.com.br",
		"traefik.http.routers.web.entrypoints: websecure",
		"proxy_network:\n    external: true",
		"postgres_network:\n    external: true",
		"file: ${CAIS_SECRETS}/jwt",
		"postgres:5432",
		"env_file: .env",
	} {
		if !strings.Contains(g, want) {
			t.Errorf("guia sem %q:\n%s", want, g)
		}
	}
}

// sem healthcheck o up volta "running" na hora; o deploy observa um pouco e
// container que reinicia ou para com erro e falha (e a anterior volta)
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
	if !slices.Equal(d.calls, []string{"config v2", "build v2", "up v2", "build v1", "up v1"}) {
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
	m.SetSecret("web", "jwt", []byte("x"))
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
	m.SetSecret("web", "postgres_loja_pass", []byte("colada a mao"))
	read := func(s string) string {
		data, _ := os.ReadFile(filepath.Join(m.SecretsPath("web"), s))
		return string(data)
	}

	if err := m.LinkDB("web", DBLink{Engine: "postgres", Name: "nao"}); err == nil {
		t.Error("ligou banco que nao existe")
	}
	if err := m.LinkDB("web", DBLink{Engine: "postgres", Name: "loja"}); err == nil || !strings.Contains(err.Error(), "gravado a mao") {
		t.Fatalf("secret repetido: %v", err)
	}
	m.DeleteSecret("web", "postgres_loja_pass")
	if err := m.LinkDB("web", DBLink{Engine: "postgres", Name: "loja"}); err != nil {
		t.Fatal(err)
	}
	// cada banco tem os proprios secrets: o app pode usar mais de um
	if err := m.LinkDB("web", DBLink{Engine: "mysql", Name: "relatorios"}); err != nil {
		t.Fatal(err)
	}
	// os secrets aparecem na hora, marcados com o banco
	list, _ := m.Secrets("web")
	got := map[string]string{}
	for _, s := range list {
		got[s.Name] = s.Infra
	}
	want := map[string]string{"postgres_loja_user": "postgres/loja", "postgres_loja_pass": "postgres/loja",
		"mysql_relatorios_user": "mysql/relatorios", "mysql_relatorios_pass": "mysql/relatorios"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("secrets %v", got)
	}
	if read("postgres_loja_user") != "loja" || read("postgres_loja_pass") != "s3nh4" {
		t.Errorf("credencial %q %q", read("postgres_loja_user"), read("postgres_loja_pass"))
	}
	if err := m.DeleteSecret("web", "postgres_loja_pass"); err == nil {
		t.Error("apagou a senha do banco como secret comum")
	}
	if err := m.SetSecret("web", "postgres_loja_user", []byte("x")); err == nil {
		t.Error("sobrescreveu o usuario do banco a mao")
	}

	// a senha mudou no cais: o proximo deploy ja leva a nova
	pws["postgres/loja"] = "nova"
	if _, err := m.Deploy(context.Background(), "web", ""); err != nil {
		t.Fatal(err)
	}
	if read("postgres_loja_pass") != "nova" {
		t.Errorf("nao atualizou: %q", read("postgres_loja_pass"))
	}

	if err := m.UnlinkDB("web", "postgres", "loja"); err != nil {
		t.Fatal(err)
	}
	if list, _ := m.Secrets("web"); len(list) != 2 || list[0].Infra != "mysql/relatorios" {
		t.Errorf("depois de desligar o postgres: %+v", list)
	}
	if a, _ := m.Get("web"); len(a.Databases) != 1 {
		t.Errorf("bancos ligados: %+v", a.Databases)
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
	read := func(s string) string {
		data, _ := os.ReadFile(filepath.Join(m.SecretsPath("web"), s))
		return string(data)
	}
	if err := m.LinkBucket("web", BucketLink{Name: "nao"}); err == nil {
		t.Error("ligou bucket que nao existe")
	}
	if err := m.LinkBucket("web", BucketLink{Name: "portal-fotos"}); err != nil {
		t.Fatal(err)
	}
	if read("s3_portal_fotos_access_key") != "GK1" || read("s3_portal_fotos_secret_key") != "s3cr3t" {
		t.Errorf("chave %q %q", read("s3_portal_fotos_access_key"), read("s3_portal_fotos_secret_key"))
	}
	list, _ := m.Secrets("web")
	if len(list) != 2 || list[0].Infra != "s3/portal-fotos" {
		t.Fatalf("secrets %+v", list)
	}
	if err := m.DeleteSecret("web", "s3_portal_fotos_secret_key"); err == nil {
		t.Error("apagou a chave do bucket como secret comum")
	}

	// chave nova no cais: o proximo deploy ja leva
	keys["portal-fotos"] = [2]string{"GK2", "nova"}
	if _, err := m.Deploy(context.Background(), "web", ""); err != nil {
		t.Fatal(err)
	}
	if read("s3_portal_fotos_access_key") != "GK2" {
		t.Errorf("nao atualizou: %q", read("s3_portal_fotos_access_key"))
	}

	if err := m.UnlinkBucket("web", "portal-fotos"); err != nil {
		t.Fatal(err)
	}
	if list, _ := m.Secrets("web"); len(list) != 0 {
		t.Errorf("os secrets do bucket ficaram: %+v", list)
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
	if !slices.Equal(d.calls, []string{"config v2", "build v2", "rollout app web-app v2", "build v1", "up v1"}) {
		t.Errorf("chamadas %v", d.calls)
	}
	if a, _ := m.Get("web"); a.Commit != "c1" || !a.Last.RolledBack {
		t.Errorf("app %+v / %+v", a, a.Last)
	}
}
