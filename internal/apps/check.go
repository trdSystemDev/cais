package apps

import (
	"encoding/json"
	"fmt"
	"github.com/trdSystemDev/cais/internal/docker"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// InfraNetworks sao as redes das stacks da infra que um app pode usar, com a
// stack de cada uma (a mensagem diz qual ligar).
var InfraNetworks = map[string]string{
	"proxy_network":    "proxy",
	"postgres_network": "postgres",
	"mysql_network":    "mysql",
	"garage_network":   "garage",
}

// ProxyNetwork e a rede do Traefik: todo app publicado entra nela.
const ProxyNetwork = "proxy_network"

// Rules e o contexto da checagem do compose de um app.
type Rules struct {
	App    string
	Src    string // pasta do codigo do commit
	Bin    string // ${CAIS_BIN}: o binario do cais (montado so leitura)
	RunEnv string // ${CAIS_ENV}: as variaveis que o cais run injeta
	DotEnv string // o .env que o cais grava (so interpolacao)
	// Networks sao as redes externas que o app pode usar agora: a
	// as das stacks ligadas (proxy_network so com o proxy ligado).
	Networks []string
	// Apps sao os outros apps: um nome do Traefik pertence ao app de nome
	// mais longo que o prefixa (portal-homolog-web e de portal-homolog, nao de
	// portal).
	Apps []string
}

// traefikOwner diz de qual app e um nome de router/service/middleware: o
// de nome mais longo entre os que o prefixam (nome igual ou nome-...).
func traefikOwner(name string, apps []string) string {
	owner := ""
	for _, a := range apps {
		if (name == a || strings.HasPrefix(name, a+"-")) && len(a) > len(owner) {
			owner = a
		}
	}
	return owner
}

// ComposeError e um compose fora do formato do cais: cada problema diz o
// motivo e como fazer do jeito certo. O deploy para antes do build.
type ComposeError struct {
	App      string
	Problems []string
}

func (e *ComposeError) Error() string {
	return "o compose de producao nao segue o formato do cais (nada foi buildado; a versao no ar continua):\n- " +
		strings.Join(e.Problems, "\n- ") + "\n\nModelo pronto pra colar: cais app guide " + e.App
}

// composeConfig e o pedaco do `docker compose config --format json
// --no-env-resolution` (o compose ja resolvido, com caminhos absolutos; so
// o que algum service usa aparece) que a checagem olha.
// composeService e um service do compose resolvido.
type composeService struct {
	Privileged    bool            `json:"privileged"`
	CapAdd        []string        `json:"cap_add"`
	Devices       []any           `json:"devices"`
	NetworkMode   string          `json:"network_mode"`
	Pid           string          `json:"pid"`
	Ipc           string          `json:"ipc"`
	Uts           string          `json:"uts"`
	Cgroup        string          `json:"cgroup"`
	UsernsMode    string          `json:"userns_mode"`
	SecurityOpt   []string        `json:"security_opt"`
	ContainerName string          `json:"container_name"`
	ExternalLinks []string        `json:"external_links"`
	VolumesFrom   []string        `json:"volumes_from"`
	EnvFile       json.RawMessage `json:"env_file"`
	Ports         []struct {
		HostIP    string `json:"host_ip"`
		Published string `json:"published"`
		Target    int    `json:"target"`
	} `json:"ports"`
	Volumes []struct {
		Type     string `json:"type"`
		Source   string `json:"source"`
		Target   string `json:"target"`
		ReadOnly bool   `json:"read_only"`
	} `json:"volumes"`
	Build *struct {
		Context            string            `json:"context"`
		Dockerfile         string            `json:"dockerfile"`
		AdditionalContexts map[string]string `json:"additional_contexts"`
		SSH                any               `json:"ssh"`
		Args               map[string]any    `json:"args"`
	} `json:"build"`
	Labels      map[string]string `json:"labels"`
	Networks    map[string]any    `json:"networks"`
	Image       string            `json:"image"`
	Healthcheck *healthcheck      `json:"healthcheck"`
	Logging     *struct {
		Driver  string            `json:"driver"`
		Options map[string]string `json:"options"`
	} `json:"logging"`
}

type composeConfig struct {
	Services map[string]composeService `json:"services"`
	Networks map[string]struct {
		Name     string `json:"name"`
		External bool   `json:"external"`
	} `json:"networks"`
	Volumes map[string]struct {
		Name       string            `json:"name"`
		External   bool              `json:"external"`
		DriverOpts map[string]string `json:"driver_opts"`
	} `json:"volumes"`
	Secrets map[string]fileRef `json:"secrets"`
	Configs map[string]fileRef `json:"configs"`
}

type healthcheck struct {
	Test    []string `json:"test"`
	Disable bool     `json:"disable"`
}

// active diz se o service tem healthcheck no compose (o HEALTHCHECK do
// Dockerfile nao aparece no compose resolvido).
func (h *healthcheck) active() bool {
	return h != nil && !h.Disable && len(h.Test) > 0 && h.Test[0] != "NONE"
}

// drains diz se o healthcheck falha com o arquivo de drenagem: e o que
// deixa o deploy tirar o container antigo do Traefik antes de parar.
func (h *healthcheck) drains() bool {
	return h.active() && strings.Contains(strings.Join(h.Test[1:], " "), docker.DrainFile)
}

// drainExample e o healthcheck com drenagem pra colar no compose.
func drainExample(port string) string {
	return fmt.Sprintf(`healthcheck: {test: ["CMD-SHELL", "test ! -f %s && wget -qO- http://127.0.0.1:%s/ || exit 1"], interval: 5s, retries: 2, start_period: 10s, start_interval: 1s}`, docker.DrainFile, port)
}

type fileRef struct {
	File     string `json:"file"`
	External bool   `json:"external"`
}

// secretArg e nome de build arg que parece segredo (TOKEN, NPM_TOKEN,
// DB_PASSWORD, STRIPE_API_KEY...). Os publicos por convencao (publicArg)
// ficam de fora.
var secretArg = regexp.MustCompile(`(?i)(^|_)(token|secret|password|passwd|pass|pwd|apikey|api_key|private_key|credentials?)(_|$)`)

// publicArg e build arg que o framework poe no JavaScript do navegador
// (NEXT_PUBLIC_MAPBOX_ACCESS_TOKEN, VITE_..., REACT_APP_...): e publico por
// definicao, e build secret nem chegaria no bundle.
var publicArg = regexp.MustCompile(`^(NEXT_PUBLIC|VITE|REACT_APP|NUXT_PUBLIC|PUBLIC|EXPO_PUBLIC|GATSBY)_`)

// nomes de router/service/middleware nos labels do Traefik
var traefikName = regexp.MustCompile(`^traefik\.(?:http|tcp|udp)\.(routers|services|middlewares)\.([^.]+)\.`)

// CheckCompose confere o compose resolvido contra o formato do cais. Os
// problemas bloqueiam o deploy (acesso ao host, a outro app ou fora do
// roteamento pelo Traefik); os avisos nao.
func CheckCompose(configJSON []byte, r Rules) (problems, warnings []string) {
	var c composeConfig
	if err := json.Unmarshal(configJSON, &c); err != nil {
		return []string{"nao consegui ler o compose resolvido pra conferir: " + err.Error()}, nil
	}
	k := &composeCheck{r: r, guide: "cais app guide " + r.App}
	// a ordem importa: os avisos saem na ordem em que aparecem
	for _, n := range sortedKeys(c.Services) {
		s := c.Services[n]
		k.hostAccess(n, &s)
		k.ports(n, &s)
		k.mounts(n, &s)
		k.build(n, &s)
		k.logging(n, &s)
		k.traefik(n, &s)
	}
	k.networks(&c)
	k.volumes(&c)
	k.files(&c)
	sort.Strings(k.problems)
	return k.problems, k.warnings
}

// composeCheck junta os problemas e avisos de um CheckCompose.
type composeCheck struct {
	r                  Rules
	guide              string // o comando que mostra o modelo do compose
	problems, warnings []string
}

func (k *composeCheck) bad(svc, format string, args ...any) {
	p := fmt.Sprintf(format, args...)
	if svc != "" {
		p = svc + ": " + p
	}
	k.problems = append(k.problems, p)
}

func (k *composeCheck) warn(svc, format string, args ...any) {
	k.warnings = append(k.warnings, svc+": "+fmt.Sprintf(format, args...))
}

// inApp diz se o caminho e do app: o codigo dele.
func (k *composeCheck) inApp(p string) bool {
	p = filepath.Clean(p)
	return within(p, k.r.Src)
}

// caisMount diz se o bind e um dos dois do cais run: o binario do cais e o
// run.env, so esses caminhos exatos.
func (k *composeCheck) caisMount(src string) bool {
	return k.r.Bin != "" && src == filepath.Clean(k.r.Bin) || k.r.RunEnv != "" && src == filepath.Clean(k.r.RunEnv)
}

// runSnippet e o jeito certo de entregar as variaveis ao app.
const runSnippet = `entrypoint: ["/cais", "run", "--", <comando do app>] e volumes ${CAIS_BIN}:/cais:ro e ${CAIS_ENV}:` + RunEnvTarget + `:ro`

// hostAccess confere o que da acesso ao host ou a containers de fora do app.
func (k *composeCheck) hostAccess(n string, s *composeService) {
	if s.Privileged {
		k.bad(n, "privileged da acesso total ao host. Tire o privileged: o app roda como container comum")
	}
	if len(s.CapAdd) > 0 {
		k.bad(n, "cap_add %v da poderes sobre o host. Tire o cap_add", s.CapAdd)
	}
	if len(s.Devices) > 0 {
		k.bad(n, "devices da acesso a dispositivos do host. Tire o devices")
	}
	for field, v := range map[string]string{"network_mode": s.NetworkMode, "pid": s.Pid, "ipc": s.Ipc, "uts": s.Uts, "cgroup": s.Cgroup, "userns_mode": s.UsernsMode} {
		switch {
		case v == "host":
			k.bad(n, "%s: host usa o do host. Tire o %s: o compose isola o app", field, field)
		case strings.HasPrefix(v, "container:"):
			k.bad(n, "%s: %s entra num container de fora do app. Use so services do proprio compose (service:<nome>)", field, v)
		case field == "network_mode" && v == "bridge":
			k.bad(n, "network_mode: bridge poe o app na rede padrao do docker, junto de outros containers. Tire o network_mode e use as redes do compose (%s)", k.guide)
		}
	}
	for _, o := range s.SecurityOpt {
		if strings.Contains(o, "unconfined") || o == "label=disable" || o == "label:disable" {
			k.bad(n, "security_opt %s tira a protecao do kernel. Tire esse security_opt", o)
		}
	}
	if s.ContainerName != "" {
		k.bad(n, "container_name %q pode colidir com outro app ou com a infra. Tire o container_name: o nome sai como %s-%s-1", s.ContainerName, k.r.App, n)
	}
	if len(s.ExternalLinks) > 0 {
		k.bad(n, "external_links liga em containers de fora do app. Use os hosts da infra pelas redes (postgres, mysql, garage) ou o dominio do outro app")
	}
	for _, v := range s.VolumesFrom {
		if strings.HasPrefix(v, "container:") {
			k.bad(n, "volumes_from %s monta volumes de um container de fora do app. Use um volume nomeado do proprio compose", v)
		}
	}
}

// ports confere portas publicadas no host (o acesso e pelo Traefik).
func (k *composeCheck) ports(n string, s *composeService) {
	for _, p := range s.Ports {
		host := p.Published
		if host == "" {
			host = "aleatoria"
		}
		k.bad(n, "publica a porta %d no host (porta %s). No cais o acesso e pelo dominio: tire o ports (use expose se outro service precisar) e ponha os labels do Traefik com a proxy_network (%s)", p.Target, host, k.guide)
	}
}

// mounts confere volumes montados e env_file: so de dentro do app.
func (k *composeCheck) mounts(n string, s *composeService) {
	for _, v := range s.Volumes {
		switch v.Type {
		case "bind":
			src := filepath.Clean(v.Source)
			switch {
			case strings.HasSuffix(src, "docker.sock"):
				k.bad(n, "monta o docker.sock: controla o Docker e o host inteiro. Tire esse volume; o cais e quem fala com o Docker")
			case k.caisMount(src) && !v.ReadOnly:
				k.bad(n, "monta %s com escrita: o binario e as variaveis do cais vao so leitura. Use %s:%s:ro", v.Source, v.Source, v.Target)
			case k.caisMount(src):
			case !k.inApp(src):
				k.bad(n, "monta %s do host, fora da pasta do app. Dados: use um volume nomeado (volumes: {dados: {}} e dados:%s). Arquivo de config: deixe no repositorio; valor secreto: variavel do app (cais app env)", v.Source, v.Target)
			case within(src, k.r.Src) && !v.ReadOnly:
				k.warn(n, "monta %s do codigo com escrita: o que o app gravar ai some no proximo deploy (o codigo e trocado). Pra dados use um volume nomeado; pra codigo, monte com :ro", strings.TrimPrefix(src, filepath.Clean(k.r.Src)+"/"))
			}
		case "npipe":
			k.bad(n, "volume npipe nao existe no cais. Tire esse volume")
		}
	}
	for _, f := range envFiles(s.EnvFile) {
		switch {
		case k.r.DotEnv != "" && filepath.Clean(f) == filepath.Clean(k.r.DotEnv):
			k.bad(n, "env_file: .env poe as variaveis na config do container (aparecem no docker inspect). O cais injeta no processo: %s", runSnippet)
		case !k.inApp(f):
			k.bad(n, "env_file %s le um arquivo de fora do app. As variaveis do app chegam pelo cais run: %s", f, runSnippet)
		}
	}
}

// build confere o build: so do proprio repositorio, sem as chaves do servidor.
func (k *composeCheck) build(n string, s *composeService) {
	if b := s.Build; b != nil {
		switch {
		case strings.Contains(b.Context, "://") || strings.HasPrefix(b.Context, "git@"):
			k.bad(n, "build de %s: o build vem do proprio repositorio. Use build: . (ou a pasta do Dockerfile)", b.Context)
		case !within(filepath.Clean(b.Context), k.r.Src):
			k.bad(n, "build com contexto %s, fora do repositorio. Use uma pasta do repositorio (build: ./app)", b.Context)
		case filepath.IsAbs(b.Dockerfile) && !within(filepath.Clean(b.Dockerfile), k.r.Src):
			k.bad(n, "dockerfile %s fora do repositorio. Use um Dockerfile do repositorio", b.Dockerfile)
		}
		for name, ctx := range b.AdditionalContexts {
			if !strings.Contains(ctx, ":") && !within(filepath.Clean(ctx), k.r.Src) {
				k.bad(n, "additional_contexts %s=%s fora do repositorio. Use pastas do repositorio", name, ctx)
			}
		}
		if b.SSH != nil {
			k.bad(n, "build com ssh repassa as chaves do servidor pro build. Tire o ssh do build")
		}
		for _, arg := range sortedKeys(b.Args) {
			if secretArg.MatchString(arg) && !publicArg.MatchString(arg) {
				id := strings.ToLower(arg)
				k.warn(n, "o build arg %s parece segredo: build arg fica na imagem (docker history). Use build secret: secrets: [%s] no build, "+
					"secrets: {%s: {environment: %s}} no compose e RUN --mount=type=secret,id=%s no Dockerfile (veja %s)", arg, id, id, arg, id, k.guide)
			}
		}
		if dotEnv := filepath.Clean(k.r.DotEnv); k.r.DotEnv != "" && within(dotEnv, filepath.Clean(b.Context)) && !ignoresEnv(b.Context) {
			k.warn(n, "o .env (com as variaveis do app) fica no contexto do build e nao esta no .dockerignore: um COPY . grava as variaveis na imagem. Ponha .env no .dockerignore")
		}
	}
}

// logging confere o limite do log do container.
func (k *composeCheck) logging(n string, s *composeService) {
	// json-file (o padrao do docker) nao tem limite nenhum; local ja gira
	// sozinho, none e syslog/journald nao gravam no disco do docker
	if l := s.Logging; l == nil || ((l.Driver == "" || l.Driver == "json-file") && l.Options["max-size"] == "") {
		k.warn(n, "sem limite de log: o log do container cresce ate encher o disco da VPS (derruba tudo, bancos inclusive). Ponha logging: {driver: json-file, options: {max-size: \"20m\", max-file: \"5\"}}")
	}
}

// traefik confere os labels do Traefik: nomes do app, proxy_network e troca sem queda.
func (k *composeCheck) traefik(n string, s *composeService) {
	traefik := s.Labels["traefik.enable"] == "true"
	for label := range s.Labels {
		m := traefikName.FindStringSubmatch(label)
		if m == nil {
			continue
		}
		traefik = true
		switch owner := traefikOwner(m[2], append([]string{k.r.App}, k.r.Apps...)); {
		case owner == "":
			k.bad(n, "o label %s usa o nome %q no Traefik: nomes de router, service e middleware comecam com o nome do app (%s ou %s-<algo>), senao um app pisa na rota de outro", label, m[2], k.r.App, k.r.App)
		case owner != k.r.App:
			k.bad(n, "o label %s usa o nome %q no Traefik, que e do app %s: use %s ou %s-<algo> sem cair no prefixo de %s", label, m[2], owner, k.r.App, k.r.App, owner)
		}
	}
	if traefik {
		if _, ok := s.Networks[ProxyNetwork]; !ok {
			k.bad(n, "tem labels do Traefik mas nao esta na proxy_network: o Traefik nao alcanca o service. Ponha networks: [default, proxy_network] e proxy_network: {external: true}")
		}
		port := "<porta>"
		for label, v := range s.Labels {
			if strings.HasSuffix(label, ".loadbalancer.server.port") {
				port = v
			}
		}
		switch {
		case s.Labels["traefik.enable"] != "true":
		case !s.Healthcheck.active():
			k.warn(n, "sem healthcheck no compose: cada deploy recria o container e o site cai por alguns segundos. Pra trocar sem queda: %s", drainExample(port))
		case !s.Healthcheck.drains():
			k.warn(n, "o healthcheck nao tem a drenagem (test ! -f %s): o deploy troca sem queda, mas o Traefik so tira o container antigo depois que ele para e algumas requisicoes falham no corte. Ponha \"test ! -f %s &&\" no comeco do teste, ex: %s", docker.DrainFile, docker.DrainFile, drainExample(port))
		}
	}
}

// networks confere as redes: so as da infra (externas) e as do proprio projeto.
func (k *composeCheck) networks(c *composeConfig) {
	for _, key := range sortedKeys(c.Networks) {
		nw := c.Networks[key]
		name := nw.Name
		if name == "" {
			name = key
		}
		allowed := contains(k.r.Networks, name)
		switch {
		case nw.External && allowed:
		case nw.External && InfraNetworks[name] != "":
			k.bad("", "a rede %s e do %s, que esta desligado. Ligue o %s na aba Infra (ou cais service %s on)", name, InfraNetworks[name], InfraNetworks[name], InfraNetworks[name])
		case nw.External:
			k.bad("", "a rede externa %s nao e da infra do cais (outro app ou container de fora). Use so proxy_network e as redes da infra (postgres_network, mysql_network, garage_network)", name)
		case name == ProxyNetwork || InfraNetworks[name] != "":
			k.bad("", "a rede %s tem que ser external: true (ela e da infra, o app nao cria)", name)
		case !strings.HasPrefix(name, k.r.App+"_"):
			k.bad("", "a rede %q (name: %s) e de fora do projeto: entraria na rede de outro app. Tire o name: da rede", key, name)
		}
	}
}

// volumes confere os volumes do projeto e avisa que eles nao entram no backup.
func (k *composeCheck) volumes(c *composeConfig) {
	// so os volumes do proprio app (os externos sao recusados abaixo)
	var own []string
	for _, key := range sortedKeys(c.Volumes) {
		if !c.Volumes[key].External {
			own = append(own, key)
		}
	}
	if len(own) > 0 {
		k.warnings = append(k.warnings, fmt.Sprintf("volumes: %s nao entram no backup do cais (so os databases do Postgres/MySQL da infra e o estado do cais entram). Dado que nao pode se perder vai no banco, ou precisa de backup proprio do app", strings.Join(own, ", ")))
	}
	for _, key := range sortedKeys(c.Volumes) {
		v := c.Volumes[key]
		switch {
		case v.External:
			k.bad("", "o volume %s e external: pode ser o volume de outro app ou da infra (dados de banco). Use um volume do proprio compose (volumes: {%s: {}})", v.Name, key)
		case v.DriverOpts["device"] != "" && !within(filepath.Clean(v.DriverOpts["device"]), k.r.Src):
			k.bad("", "o volume %s monta %s do host (driver_opts device). Tire o driver_opts: um volume nomeado comum guarda os dados", key, v.DriverOpts["device"])
		case v.Name != "" && !strings.HasPrefix(v.Name, k.r.App+"_"):
			k.bad("", "o volume %q (name: %s) e de fora do projeto: poderia ser o volume de outro app. Tire o name: do volume", key, v.Name)
		}
	}
}

// files confere secrets e configs: secret so vindo de variavel do app
// (environment:, o build secret); config so de arquivo do repositorio.
func (k *composeCheck) files(c *composeConfig) {
	for _, key := range sortedKeys(c.Secrets) {
		f := c.Secrets[key]
		switch {
		case f.External:
			k.bad("", "o secret %s e external (do swarm), nao existe no cais. Use environment: <VARIAVEL> (uma variavel do app)", key)
		case f.File != "":
			k.bad("", "o secret %s le o arquivo %s. Segredo e variavel do app (cais app env %s %s): use environment: %s no secret, ou receba no processo pelo cais run",
				key, f.File, k.r.App, strings.ToUpper(key), strings.ToUpper(key))
		}
	}
	for _, key := range sortedKeys(c.Configs) {
		f := c.Configs[key]
		switch {
		case f.External:
			k.bad("", "o config %s e external (do swarm), nao existe no cais. Use um arquivo do repositorio", key)
		case f.File != "" && !k.inApp(f.File):
			k.bad("", "o config %s le %s, fora do repositorio. Use um arquivo do repositorio", key, f.File)
		}
	}
}

// envFiles le o env_file resolvido: lista de {path} ou de texto.
func envFiles(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var objs []struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(raw, &objs) == nil {
		var out []string
		for _, o := range objs {
			out = append(out, o.Path)
		}
		return out
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var one string
	json.Unmarshal(raw, &one)
	return []string{one}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ignoresEnv diz se o .dockerignore do contexto tira o .env do build.
func ignoresEnv(context string) bool {
	data, err := os.ReadFile(filepath.Join(context, ".dockerignore"))
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(data), "\n") {
		l = strings.TrimPrefix(strings.TrimSpace(l), "/")
		l = strings.TrimPrefix(l, "**/")
		if l == ".env" || l == ".env*" || l == "*.env" || l == "*" || l == ".*" {
			return true
		}
	}
	return false
}

// within diz se p esta dentro de dir (ou e o proprio dir).
func within(p, dir string) bool {
	if dir == "" {
		return false
	}
	dir = filepath.Clean(dir)
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}
