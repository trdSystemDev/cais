package compose

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// MaxSize e o maior compose aceito. Um compose de app tem poucos KB; o
// limite so existe pra ninguem mandar um arquivo enorme pelo deploy.
const MaxSize = 1 << 20

// Policy e o que um app pode usar alem dele mesmo.
type Policy struct {
	// App e o nome do app; o projeto compose sempre tem esse nome.
	App string
	// Networks sao as redes externas (das stacks) em que o app pode entrar,
	// ex: postgres_network. Redes que chegam no docker.sock nunca entram aqui.
	Networks []string
}

// Validate confere o compose de um app e devolve todos os problemas de uma
// vez (errors.Join), cada um com o caminho no YAML.
//
// A regra e lista de permitidos: qualquer chave que nao esteja aqui e
// recusada, entao uma opcao nova do compose nasce bloqueada ate ser
// avaliada. O que se protege e o host e as outras stacks: nada de modo
// privilegiado, namespace do host, caminho do host fora da pasta do app,
// volume/rede/projeto de outra stack, leitura do ambiente do daemon por
// interpolacao, ou label do Traefik (o roteamento e do cais).
func Validate(data []byte, p Policy) error {
	if len(data) > MaxSize {
		return fmt.Errorf("compose com %d bytes, o maximo e %d", len(data), MaxSize)
	}
	doc, err := parse(data)
	if err != nil {
		return err
	}
	v := &validator{policy: p}
	v.checkInterpolation("", doc)
	v.top(doc)
	return v.err()
}

func parse(data []byte) (map[string]any, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("compose vazio")
		}
		return nil, fmt.Errorf("compose invalido: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("compose com mais de um documento YAML (---)")
	}
	return doc, nil
}

type validator struct {
	policy   Policy
	problems []string
}

func (v *validator) bad(where, format string, args ...any) {
	v.problems = append(v.problems, where+": "+fmt.Sprintf(format, args...))
}

func (v *validator) err() error {
	if len(v.problems) == 0 {
		return nil
	}
	sort.Strings(v.problems)
	errs := make([]error, len(v.problems))
	for i, p := range v.problems {
		errs[i] = errors.New(p)
	}
	return errors.Join(errs...)
}

// checkInterpolation recusa ${VAR} e $VAR em qualquer lugar: o compose
// resolveria com o ambiente do daemon (root) na VPS. $$ e o escape de um $
// literal e continua valendo.
func (v *validator) checkInterpolation(where string, node any) {
	switch n := node.(type) {
	case string:
		if hasInterpolation(n) {
			v.bad(orRoot(where), "interpolacao com $ nao e permitida (o compose usaria o ambiente da VPS); use $$ pra um $ literal")
		}
	case map[string]any:
		for k, val := range n {
			if hasInterpolation(k) {
				v.bad(join(where, k), "interpolacao com $ nao e permitida")
			}
			v.checkInterpolation(join(where, k), val)
		}
	case []any:
		for i, val := range n {
			v.checkInterpolation(fmt.Sprintf("%s[%d]", orRoot(where), i), val)
		}
	}
}

func hasInterpolation(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '$' {
			continue
		}
		if i+1 < len(s) && s[i+1] == '$' {
			i++
			continue
		}
		return true
	}
	return false
}

var topKeys = []string{"name", "services", "volumes", "networks", "secrets", "configs"}

func (v *validator) top(doc map[string]any) {
	for k, val := range doc {
		switch {
		case isExtension(k):
		case !slices.Contains(topKeys, k):
			v.bad(k, "nao permitido em compose de app")
		case k == "name":
			if s, _ := val.(string); s != v.policy.App {
				v.bad(k, "o projeto do app se chama %q; tire o name ou use esse nome", v.policy.App)
			}
		}
	}

	services, ok := doc["services"].(map[string]any)
	if !ok || len(services) == 0 {
		v.bad("services", "o compose precisa de ao menos um service")
	}
	for name, svc := range services {
		m, ok := svc.(map[string]any)
		if !ok {
			v.bad(join("services", name), "precisa ser um mapa")
			continue
		}
		v.service(join("services", name), m)
	}

	for name, vol := range mapOf(doc["volumes"]) {
		v.topVolume(join("volumes", name), vol)
	}
	for name, net := range mapOf(doc["networks"]) {
		v.topNetwork(join("networks", name), name, net)
	}
	for name, s := range mapOf(doc["secrets"]) {
		v.fileRef(join("secrets", name), s, "./secrets/", false)
	}
	for name, c := range mapOf(doc["configs"]) {
		v.fileRef(join("configs", name), c, "./configs/", true)
	}
}

// volumes no topo: so volume local gerenciado pelo compose. name, external
// e driver_opts poderiam apontar pro volume de outra stack (postgres_data)
// ou fazer bind de qualquer pasta do host.
func (v *validator) topVolume(where string, vol any) {
	m, ok := vol.(map[string]any)
	if vol == nil || (ok && len(m) == 0) {
		return
	}
	if !ok {
		v.bad(where, "precisa ser um mapa")
		return
	}
	for k := range m {
		if k != "labels" && !isExtension(k) {
			v.bad(join(where, k), "nao permitido (o volume do app e sempre local e com o nome do projeto)")
		}
	}
}

// networks no topo: redes externas so da lista da Policy; redes proprias
// sem name/driver (o nome fica prefixado pelo projeto).
func (v *validator) topNetwork(where, key string, net any) {
	m, ok := net.(map[string]any)
	if net == nil || (ok && len(m) == 0) {
		return
	}
	if !ok {
		v.bad(where, "precisa ser um mapa")
		return
	}
	if ext, _ := m["external"].(bool); ext {
		name := key
		if n, ok := m["name"].(string); ok {
			name = n
		}
		if !slices.Contains(v.policy.Networks, name) {
			v.bad(where, "rede externa %q nao liberada pra apps (permitidas: %s)", name, strings.Join(v.policy.Networks, ", "))
		}
		for k := range m {
			if k != "external" && k != "name" && !isExtension(k) {
				v.bad(join(where, k), "nao permitido em rede externa")
			}
		}
		return
	}
	for k := range m {
		if k != "internal" && k != "labels" && !isExtension(k) {
			v.bad(join(where, k), "nao permitido (rede do app e bridge, com o nome do projeto)")
		}
	}
}

// fileRef: secret/config so de arquivo dentro da pasta do app (nunca o
// ambiente do daemon nem caminho do host). Config pode ser inline.
func (v *validator) fileRef(where string, val any, dir string, allowContent bool) {
	m, ok := val.(map[string]any)
	if !ok {
		v.bad(where, "precisa ser um mapa com file: %s<nome>", dir)
		return
	}
	for k, x := range m {
		switch {
		case isExtension(k):
		case k == "file":
			f, _ := x.(string)
			if !safeChild(f, dir) {
				v.bad(join(where, k), "precisa ser %s<nome>, sem '..' (encontrado %q)", dir, f)
			}
		case k == "content" && allowContent:
		default:
			v.bad(join(where, k), "nao permitido (so file: %s<nome>)", dir)
		}
	}
	if _, ok := m["file"]; !ok && !(allowContent && m["content"] != nil) {
		v.bad(where, "falta file: %s<nome>", dir)
	}
}

var nameChars = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

// safeChild diz se p e dir+<nome>, sem subpasta nem '..'.
func safeChild(p, dir string) bool {
	rest, ok := strings.CutPrefix(p, dir)
	return ok && nameChars.MatchString(rest) && rest != "." && rest != ".."
}

// safeRelative diz se p e um caminho relativo que fica dentro da pasta do app.
func safeRelative(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") || strings.Contains(p, `\`) {
		return false
	}
	c := path.Clean(p)
	return c != ".." && !strings.HasPrefix(c, "../")
}

// chaves permitidas num service. O que nao esta aqui (privileged, cap_add,
// devices, pid, ipc, build, extends, ports, container_name, volumes_from,
// sysctls, runtime, provider...) e recusado.
var serviceKeys = []string{
	"image", "command", "entrypoint", "environment", "env_file", "healthcheck",
	"restart", "depends_on", "labels", "volumes", "tmpfs", "networks", "secrets",
	"configs", "user", "working_dir", "stop_signal", "stop_grace_period",
	"logging", "deploy", "mem_limit", "mem_reservation", "cpus", "cpu_shares",
	"pids_limit", "ulimits", "shm_size", "read_only", "init", "expose",
	"extra_hosts", "dns", "dns_search", "hostname", "platform", "pull_policy",
	"profiles", "tty", "stdin_open", "cap_drop", "security_opt", "network_mode",
	"post_start", "pre_stop",
}

// por que algumas chaves comuns sao recusadas: a mensagem diz o que fazer
var serviceHints = map[string]string{
	"build":          "o build e feito no CI; use image: com a imagem do registry",
	"ports":          "o app nao publica porta no host; o acesso e pelo Traefik, com os dominios configurados no cais",
	"container_name": "o nome do container sai do projeto do app (evita conflito com as stacks)",
	"privileged":     "da acesso total ao host",
	"cap_add":        "capabilities extras dao poder sobre o host",
	"devices":        "da acesso a dispositivos do host",
	"extends":        "le outros arquivos; deixe o compose completo",
	"pid":            "compartilha processos com o host ou outro container",
	"ipc":            "compartilha memoria com o host ou outro container",
	"volumes_from":   "monta volumes de outro container",
	"sysctls":        "muda parametros do kernel",
	"provider":       "roda um plugin no host",
}

func (v *validator) service(where string, svc map[string]any) {
	for k, val := range svc {
		kw := join(where, k)
		switch {
		case isExtension(k):
		case !slices.Contains(serviceKeys, k):
			if hint, ok := serviceHints[k]; ok {
				v.bad(kw, "nao permitido: %s", hint)
			} else {
				v.bad(kw, "nao permitido em compose de app")
			}
		case k == "image":
			if s, _ := val.(string); strings.TrimSpace(s) == "" {
				v.bad(kw, "precisa ser o nome da imagem")
			}
		case k == "env_file":
			v.envFile(kw, val)
		case k == "labels":
			v.labels(kw, val)
		case k == "volumes":
			for i, vol := range listOf(val) {
				v.serviceVolume(fmt.Sprintf("%s[%d]", kw, i), vol)
			}
		case k == "network_mode":
			s, _ := val.(string)
			if s != "none" && !strings.HasPrefix(s, "service:") {
				v.bad(kw, "so none ou service:<nome> (host ou container: entra na rede de outro)")
			}
		case k == "security_opt":
			for i, o := range listOf(val) {
				s, _ := o.(string)
				if !strings.HasPrefix(s, "no-new-privileges") {
					v.bad(fmt.Sprintf("%s[%d]", kw, i), "so no-new-privileges (%q desliga protecoes do kernel)", s)
				}
			}
		case k == "logging":
			if m, ok := val.(map[string]any); ok {
				if d, ok := m["driver"].(string); ok && d != "json-file" && d != "local" && d != "none" {
					v.bad(join(kw, "driver"), "so json-file, local ou none")
				}
			}
		case k == "deploy":
			v.deploy(kw, val)
		case k == "post_start" || k == "pre_stop":
			for i, h := range listOf(val) {
				if m, ok := h.(map[string]any); ok && m["privileged"] == true {
					v.bad(fmt.Sprintf("%s[%d].privileged", kw, i), "nao permitido: da acesso total ao host")
				}
			}
		}
	}
	if _, ok := svc["image"]; !ok {
		v.bad(where, "falta image: (o build e feito no CI)")
	}
}

// env_file e lido pelo compose no host: so arquivos dentro da pasta do app.
func (v *validator) envFile(where string, val any) {
	for i, e := range listOf(val) {
		p, _ := e.(string)
		if m, ok := e.(map[string]any); ok {
			p, _ = m["path"].(string)
		}
		if !safeRelative(p) {
			v.bad(fmt.Sprintf("%s[%d]", where, i), "precisa ser um arquivo dentro da pasta do app (encontrado %q)", p)
		}
	}
}

// labels: traefik.* e do cais (um app nao pode rotear o dominio de outro) e
// com.docker.compose.* e do proprio compose.
func (v *validator) labels(where string, val any) {
	var keys []string
	switch l := val.(type) {
	case map[string]any:
		for k := range l {
			keys = append(keys, k)
		}
	case []any:
		for _, item := range l {
			s, _ := item.(string)
			k, _, _ := strings.Cut(s, "=")
			keys = append(keys, k)
		}
	}
	for _, k := range keys {
		switch {
		case strings.HasPrefix(k, "traefik."):
			v.bad(join(where, k), "o roteamento do Traefik e configurado no cais (dominios do app), nao por label")
		case strings.HasPrefix(k, "com.docker."):
			v.bad(join(where, k), "label reservado do docker")
		}
	}
}

// serviceVolume: volume nomeado, tmpfs ou bind de caminho relativo dentro
// da pasta do app. Nada de caminho absoluto do host.
func (v *validator) serviceVolume(where string, vol any) {
	switch x := vol.(type) {
	case string:
		src, _, hasTarget := strings.Cut(x, ":")
		if !hasTarget {
			return // volume anonimo
		}
		if isHostPath(src) && !safeRelative(src) {
			v.bad(where, "caminho do host fora da pasta do app (%q); use volume nomeado ou ./pasta", src)
		}
	case map[string]any:
		typ, _ := x["type"].(string)
		src, _ := x["source"].(string)
		switch typ {
		case "volume", "tmpfs":
		case "bind":
			if !safeRelative(src) {
				v.bad(where, "bind fora da pasta do app (%q); use volume nomeado ou ./pasta", src)
			}
			if b, ok := x["bind"].(map[string]any); ok && b["propagation"] != nil {
				v.bad(join(where, "bind.propagation"), "nao permitido")
			}
		default:
			v.bad(join(where, "type"), "so volume, bind (dentro da pasta do app) ou tmpfs")
		}
	default:
		v.bad(where, "formato de volume desconhecido")
	}
}

func isHostPath(src string) bool {
	return strings.HasPrefix(src, "/") || strings.HasPrefix(src, ".") || strings.HasPrefix(src, "~") || strings.Contains(src, `\`)
}

// deploy: so limites de recurso, replicas e restart. Reserva de devices
// (GPU) da acesso a dispositivo do host.
func (v *validator) deploy(where string, val any) {
	m, ok := val.(map[string]any)
	if !ok {
		return
	}
	for k := range m {
		if k != "resources" && k != "replicas" && k != "restart_policy" && !isExtension(k) {
			v.bad(join(where, k), "nao permitido (so resources, replicas e restart_policy)")
		}
	}
	res, _ := m["resources"].(map[string]any)
	if r, ok := res["reservations"].(map[string]any); ok && r["devices"] != nil {
		v.bad(join(where, "resources.reservations.devices"), "nao permitido: da acesso a dispositivos do host")
	}
}

func isExtension(k string) bool { return strings.HasPrefix(k, "x-") }

func join(where, k string) string {
	if where == "" {
		return k
	}
	return where + "." + k
}

func orRoot(where string) string {
	if where == "" {
		return "(raiz)"
	}
	return where
}

func mapOf(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// listOf aceita um item so ou uma lista (env_file: a.env e env_file: [a.env]).
func listOf(v any) []any {
	switch x := v.(type) {
	case nil:
		return nil
	case []any:
		return x
	case map[string]any:
		// security_opt/env_file nao sao mapas; labels em mapa tem tratamento proprio
		return []any{x}
	default:
		return []any{x}
	}
}
