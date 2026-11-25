package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"time"
)

// Item e uma checagem de seguranca da VPS. Fix diz como corrigir (a TUI
// mostra e a IA le pelo MCP).
type Item struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
	Fix    string `json:"fix,omitempty"`
	// Fixable: da pra corrigir so este item (Fix na TUI, botao Corrigir).
	// Os do SSH vao pelo harden inteiro (admin com chave antes).
	Fixable bool `json:"fixable,omitempty"`
}

// HardenFix e o conserto da maioria dos itens.
const HardenFix = "sudo cais host harden -user NOME (cria o admin, so chave no SSH, firewall, fail2ban, atualizacoes)"

// Portas que a VPS do cais abre pra fora: SSH e o proxy (HTTP, HTTPS e
// HTTP/3). 68 e 546 sao o cliente DHCP da propria VPS; 41641/udp e o
// WireGuard do Tailscale (conexao direta entre as maquinas da tailnet,
// cifrada e so com as chaves dela).
var publicPorts = map[string]bool{
	"22/tcp": true, "80/tcp": true, "443/tcp": true, "443/udp": true,
	"68/udp": true, "546/udp": true, "41641/udp": true,
}

// firewallPorts sao as regras que o harden libera no ufw.
var firewallPorts = []string{"22/tcp", "80/tcp", "443/tcp", "443/udp"}

// Check confere a seguranca da maquina; so le. Fora do Linux nao ha o que
// conferir (nil). Precisa de root (sshd -T, ufw).
func Check(ctx context.Context, sys System) []Item {
	if runtime.GOOS != "linux" {
		return nil
	}
	if sys.Geteuid() != 0 {
		return []Item{{ID: "root", Title: "Checagem de seguranca", Detail: "precisa de root", Fix: "sudo cais host check"}}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	items := []Item{checkOS(sys)}
	items = append(items, checkSSH(ctx, sys)...)
	items = append(items, checkTailscale(ctx, sys)...)
	if it, ok := checkCaisKeys(ctx, sys); ok {
		items = append(items, it)
	}
	items = append(items,
		checkFirewall(ctx, sys),
		checkFail2ban(ctx, sys),
		checkUpdates(ctx, sys),
		checkPorts(ctx, sys),
		checkDockerLogs(ctx, sys),
		checkReboot(sys),
	)
	for i := range items {
		_, fixable := fixers[items[i].ID]
		items[i].Fixable = !items[i].OK && fixable
	}
	return items
}

func checkOS(sys System) Item {
	it := Item{ID: "os", Title: "Sistema"}
	r, err := readOSRelease(sys)
	switch {
	case err != nil:
		it.Detail = "sem " + osReleasePath
	case r.Supported():
		it.OK, it.Detail = true, r.String()
	default:
		it.Detail = r.String() + ": o cais host harden so prepara Ubuntu e Debian"
		it.Fix = "use Ubuntu LTS na VPS, ou faca o equivalente a mao (docs/seguranca.md)"
	}
	return it
}

func checkSSH(ctx context.Context, sys System) []Item {
	root := Item{ID: "ssh-root", Title: "Login do root pelo SSH", Fix: HardenFix}
	pass := Item{ID: "ssh-password", Title: "SSH so com chave (sem senha)", Fix: HardenFix}
	cfg, err := sshdConfig(ctx, sys, "")
	if err != nil {
		root.Detail = "nao deu pra ler a config do sshd: " + firstLine(err)
		pass.Detail = root.Detail
		return []Item{root, pass}
	}
	if v := cfg["permitrootlogin"]; v == "no" {
		root.OK, root.Detail = true, "bloqueado"
	} else {
		root.Detail = "permitido (PermitRootLogin " + v + ")"
	}
	pw, kbd := cfg["passwordauthentication"], cfg["kbdinteractiveauthentication"]
	if kbd == "" {
		kbd = cfg["challengeresponseauthentication"] // OpenSSH antigo
	}
	switch {
	case pw == "no" && kbd != "yes":
		pass.OK, pass.Detail = true, "senha desligada"
	case pw != "no":
		pass.Detail = "aceita senha (PasswordAuthentication " + pw + "): alvo de forca bruta"
	default:
		pass.Detail = "aceita senha pelo teclado (KbdInteractiveAuthentication yes)"
	}
	return []Item{root, pass}
}

// checkCaisKeys confere se o ssh cais@vps tem alguma chave pra entrar (so
// com o usuario cais do install.sh --ssh).
func checkCaisKeys(ctx context.Context, sys System) (Item, bool) {
	home, ok := hasCaisUser(ctx, sys)
	if !ok {
		return Item{}, false
	}
	it := Item{ID: "ssh-cais", Title: "Chave do ssh cais@vps (a TUI)",
		Fix: "copie as chaves do admin: sudo cp ~/.ssh/authorized_keys " + home + "/.ssh/ && sudo chown -R " + CaisUser + ": " + home + "/.ssh (ou o Corrigir da TUI, por sudo cais)"}
	b, _ := sys.ReadFile(home + "/.ssh/authorized_keys")
	if n := len(parseKeys(string(b))); n > 0 {
		it.OK, it.Detail = true, fmt.Sprintf("%d chave(s)", n)
	} else {
		it.Detail = "sem nenhuma chave: ssh cais@vps da Permission denied"
	}
	return it, true
}

// ufwStatus e o que importa do ufw status verbose.
type ufwStatus struct {
	Active      bool
	DenyIn      bool
	Allowed     []string // regras ALLOW (22/tcp, 80, OpenSSH...)
	Unavailable string   // por que nao deu pra ler
}

func readUFW(ctx context.Context, sys System) ufwStatus {
	out, err := sys.Run(ctx, "ufw", "status", "verbose")
	if errors.Is(err, exec.ErrNotFound) {
		return ufwStatus{Unavailable: "ufw nao instalado"}
	}
	if err != nil {
		return ufwStatus{Unavailable: firstLine(err)}
	}
	var s ufwStatus
	rules := false
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, "Status:"):
			s.Active = strings.Contains(l, "active") && !strings.Contains(l, "inactive")
		case strings.HasPrefix(l, "Default:"):
			s.DenyIn = strings.Contains(l, "deny (incoming)") || strings.Contains(l, "reject (incoming)")
		case strings.HasPrefix(l, "--"):
			rules = true
		case rules && l != "":
			i := strings.Index(l, " ALLOW")
			if i < 0 {
				continue
			}
			to := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(l[:i]), "(v6)"))
			if !slices.Contains(s.Allowed, to) {
				s.Allowed = append(s.Allowed, to)
			}
		}
	}
	return s
}

// extraRules sao as regras ALLOW fora das portas do cais.
func (s ufwStatus) extraRules() []string {
	var extra []string
	for _, to := range s.Allowed {
		// OpenSSH e o 22; as regras so da interface do Tailscale valem so
		// pra tailnet
		if to == "OpenSSH" || strings.HasSuffix(to, " on "+tailscaleIface) {
			continue
		}
		ports, proto, hasProto := strings.Cut(to, "/")
		ok := true
		for _, p := range strings.Split(ports, ",") {
			if hasProto {
				ok = ok && publicPorts[p+"/"+proto]
			} else {
				ok = ok && (p == "22" || p == "80" || p == "443")
			}
		}
		if !ok {
			extra = append(extra, to)
		}
	}
	return extra
}

func checkFirewall(ctx context.Context, sys System) Item {
	it := Item{ID: "firewall", Title: "Firewall (ufw)", Fix: HardenFix}
	s := readUFW(ctx, sys)
	switch {
	case s.Unavailable != "":
		it.Detail = "nao deu pra ler o ufw: " + s.Unavailable
	case !s.Active:
		it.Detail = "desligado: toda porta aberta na VPS fica exposta"
	case !s.DenyIn:
		it.Detail = "ligado, mas a entrada nao e bloqueada por padrao"
	case len(s.extraRules()) > 0:
		it.Detail = "libera alem do SSH e do proxy: " + strings.Join(s.extraRules(), ", ")
		it.Fix = "se nao precisa: sudo ufw delete allow <regra> (ex: sudo ufw delete allow " + s.extraRules()[0] + ")"
	case tailnetSSH(s) && len(publicSSHRules(s)) == 0:
		it.OK, it.Detail = true, "ligado; da internet so 80 e 443 (o SSH so pela tailnet)"
	default:
		it.OK, it.Detail = true, "ligado; so 22, 80 e 443 entram"
	}
	return it
}

// checkTailscale: a VPS na tailnet e, com ela, o SSH so por la (da
// internet, so os sites). O segundo item so aparece com o Tailscale
// conectado.
func checkTailscale(ctx context.Context, sys System) []Item {
	t := TailscaleStatus(ctx, sys)
	ts := Item{ID: "tailscale", Title: "Tailscale (VPN pra administrar a VPS)", Fix: TailscaleFix}
	switch {
	case t.Connected():
		ts.OK, ts.Detail = true, "conectado: "+cmpOr(t.Name, t.IP4())+" ("+t.IP4()+")"
	case !t.Installed:
		ts.Detail = "nao instalado: o SSH so pode ficar aberto pra internet"
	case t.Error != "":
		ts.Detail = "instalado, mas sem resposta: " + t.Error
	default:
		ts.Detail = "instalado, fora da tailnet (" + cmpOr(t.State, "sem login") + ")"
	}
	if !t.Connected() {
		return []Item{ts}
	}
	ssh := Item{ID: "ssh-tailnet", Title: "SSH so pela tailnet", Fix: CloseSSHFix}
	if SSHTailnetOnly(ctx, sys) {
		ssh.OK, ssh.Detail = true, "da internet so 80 e 443; o SSH entra por "+cmpOr(t.Name, t.IP4())
	} else {
		ssh.Detail = "o 22 esta aberto pra internet (com chave e fail2ban, mas visivel pra quem varre portas)"
	}
	return []Item{ts, ssh}
}

// Consertos do Tailscale: pela TUI (a autorizacao e no navegador).
const (
	TailscaleFix = "Setup > Seguranca > Tailscale: Conectar (instala e mostra o link pra autorizar no navegador)"
	CloseSSHFix  = "Setup > Seguranca > Tailscale: Fechar o SSH publico (volta sozinho em 5 min sem o Confirmar pela tailnet)"
)

func checkFail2ban(ctx context.Context, sys System) Item {
	it := Item{ID: "fail2ban", Title: "fail2ban (bloqueia forca bruta no SSH)", Fix: HardenFix}
	if out, _ := sys.Run(ctx, "systemctl", "is-active", "fail2ban"); out != "active" {
		it.Detail = "nao esta rodando"
		return it
	}
	if _, err := sys.Run(ctx, "fail2ban-client", "status", "sshd"); err != nil {
		it.Detail = "rodando, mas sem o jail do sshd"
		return it
	}
	it.OK, it.Detail = true, "jail sshd ativo"
	return it
}

const autoUpgradesPath = "/etc/apt/apt.conf.d/20auto-upgrades"

func checkUpdates(ctx context.Context, sys System) Item {
	it := Item{ID: "updates", Title: "Atualizacoes de seguranca automaticas", Fix: HardenFix}
	out, err := sys.Run(ctx, "dpkg-query", "-W", "-f=${Status}", "unattended-upgrades")
	if err != nil || !strings.Contains(out, "install ok installed") {
		it.Detail = "unattended-upgrades nao instalado"
		return it
	}
	b, _ := sys.ReadFile(autoUpgradesPath)
	if !strings.Contains(string(b), `APT::Periodic::Unattended-Upgrade "1"`) {
		it.Detail = "unattended-upgrades instalado, mas desligado"
		return it
	}
	it.OK, it.Detail = true, "ligadas (unattended-upgrades)"
	return it
}

// listener e uma porta aberta pra fora.
type listener struct{ port, proto, who string }

func (l listener) key() string { return l.port + "/" + l.proto }

// public diz se o endereco aceita conexao de fora (nao e loopback nem
// link-local).
func public(addr string) bool {
	addr = strings.Trim(addr, "[]")
	if i := strings.IndexByte(addr, '%'); i >= 0 {
		if addr[i+1:] == "lo" {
			return false
		}
		addr = addr[:i]
	}
	if strings.HasPrefix(addr, "127.") || addr == "::1" || strings.HasPrefix(addr, "fe80:") || addr == "localhost" {
		return false
	}
	// enderecos do Tailscale (100.64.0.0/10 e fd7a:115c:a1e0::/48): so as
	// maquinas da tailnet chegam neles
	if ip, err := netip.ParseAddr(addr); err == nil && (tailnet4.Contains(ip) || tailnet6.Contains(ip)) {
		return false
	}
	return true
}

var (
	tailnet4 = netip.MustParsePrefix("100.64.0.0/10")
	tailnet6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
)

// splitHostPort separa "0.0.0.0:22", "[::]:22", "*:80", "127.0.0.53%lo:53".
func splitHostPort(s string) (string, string, bool) {
	i := strings.LastIndexByte(s, ':')
	if i < 0 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

// readListeners sao as portas publicas escutando (ss) e as publicadas
// pelos containers (docker ps): o Docker passa por cima do ufw, entao uma
// porta publicada fica exposta mesmo com o firewall ligado.
func readListeners(ctx context.Context, sys System) ([]listener, error) {
	out, err := sys.Run(ctx, "ss", "-H", "-tuln")
	if err != nil {
		return nil, err
	}
	seen := map[string]int{}
	var ls []listener
	add := func(l listener) {
		if i, ok := seen[l.key()]; ok {
			if l.who != "" {
				ls[i].who = l.who
			}
			return
		}
		seen[l.key()] = len(ls)
		ls = append(ls, l)
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		host, port, ok := splitHostPort(f[4])
		if !ok || !public(host) {
			continue
		}
		add(listener{port: port, proto: f[0]})
	}
	// sem docker, so o ss
	if ps, err := sys.Run(ctx, "docker", "ps", "--format", "{{.Names}}\t{{.Ports}}"); err == nil {
		for _, line := range strings.Split(ps, "\n") {
			name, ports, _ := strings.Cut(line, "\t")
			for _, p := range strings.Split(ports, ",") {
				from, to, ok := strings.Cut(strings.TrimSpace(p), "->")
				if !ok {
					continue
				}
				host, port, ok := splitHostPort(from)
				if !ok || !public(host) {
					continue
				}
				_, proto, _ := strings.Cut(to, "/")
				add(listener{port: port, proto: proto, who: "container " + name})
			}
		}
	}
	return ls, nil
}

func checkPorts(ctx context.Context, sys System) Item {
	it := Item{ID: "ports", Title: "Portas abertas pra internet"}
	ls, err := readListeners(ctx, sys)
	if err != nil {
		it.Detail = "nao deu pra listar (ss): " + firstLine(err)
		return it
	}
	var extra []string
	docker := false
	for _, l := range ls {
		if publicPorts[l.key()] {
			continue
		}
		s := l.key()
		if l.who != "" {
			s += " (" + l.who + ")"
			docker = true
		}
		extra = append(extra, s)
	}
	if len(extra) == 0 {
		it.OK, it.Detail = true, "so SSH e o proxy (22, 80, 443)"
		return it
	}
	it.Detail = "fora do SSH e do proxy: " + strings.Join(extra, ", ")
	it.Fix = "pare o servico ou escute so no 127.0.0.1; o acesso de fora e pelo proxy (dominio) ou por tunel SSH"
	if docker {
		it.Fix += `. Container: publique so no 127.0.0.1 ("127.0.0.1:5000:80") ou tire o ports; o Docker passa por cima do firewall`
	}
	return it
}

const dockerDaemonPath = "/etc/docker/daemon.json"

// dockerLogsLimited diz se o daemon.json limita os logs dos containers.
func dockerLogsLimited(cfg map[string]any) bool {
	if cfg["log-driver"] == "local" {
		return true
	}
	opts, _ := cfg["log-opts"].(map[string]any)
	return opts["max-size"] != nil
}

func checkDockerLogs(ctx context.Context, sys System) Item {
	it := Item{ID: "docker-logs", Title: "Logs dos containers com limite", Fix: HardenFix}
	if _, err := sys.Run(ctx, "docker", "version", "--format", "{{.Server.Version}}"); err != nil {
		it.Detail = "Docker nao responde"
		it.Fix = "instale com o install.sh do cais (instala o Docker no Ubuntu e no Debian)"
		return it
	}
	cfg := map[string]any{}
	if b, err := sys.ReadFile(dockerDaemonPath); err == nil {
		if err := json.Unmarshal(b, &cfg); err != nil {
			it.Detail = fmt.Sprintf("%s invalido: %v", dockerDaemonPath, err)
			return it
		}
	}
	if !dockerLogsLimited(cfg) {
		it.Detail = "sem limite: um container falando muito enche o disco"
		return it
	}
	it.OK, it.Detail = true, "com rotacao"
	return it
}

const rebootRequiredPath = "/var/run/reboot-required"

func checkReboot(sys System) Item {
	it := Item{ID: "reboot", Title: "Reinicio pendente"}
	if sys.Exists(rebootRequiredPath) {
		it.Detail = "uma atualizacao (kernel ou biblioteca) so vale depois de reiniciar"
		it.Fix = "sudo reboot (ou o Corrigir da TUI), num horario calmo: os containers voltam sozinhos"
		return it
	}
	it.OK, it.Detail = true, "nenhum"
	return it
}

// Failed sao os itens com problema.
func Failed(items []Item) []Item {
	var bad []Item
	for _, it := range items {
		if !it.OK {
			bad = append(bad, it)
		}
	}
	return bad
}
