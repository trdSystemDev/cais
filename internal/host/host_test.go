package host

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSys e uma VPS simulada: arquivos em memoria e os comandos que o host
// usa, com o estado que eles mudam (usuarios, ufw, servicos, sshd).
type fakeSys struct {
	euid     int
	files    map[string]string
	users    map[string]bool
	groups   map[string][]string
	pw       map[string]bool
	active   map[string]bool
	ufwOn    bool
	ufwDeny  bool
	ufwRules []string
	docker   bool
	// contas expiradas (admin removido)
	expired map[string]bool
	// o Docker rodando com o live-restore; num swarm
	liveRestore, swarm bool
	ss                 string
	ps                 string
	// tsState e o BackendState do Tailscale ("" = nao instalado); tsAuth, o
	// link de login enquanto espera
	tsState  string
	tsAuth   string
	tsExpiry string // KeyExpiry da VPS (vazio: nao vence)
	aptSim   string // a saida do apt-get -s full-upgrade
	// dpkgLines vao pro dpkg.log durante o full-upgrade, um a cada
	// dpkgEvery (o andamento le o log noutra goroutine: mu)
	dpkgLines []string
	mu        sync.Mutex
	// fail devolve erro pro comando com esse prefixo
	fail   map[string]string
	calls  []string
	inputs []string
}

func newFake() *fakeSys {
	return &fakeSys{
		files: map[string]string{
			osReleasePath: "PRETTY_NAME=\"Ubuntu 24.04.1 LTS\"\nID=ubuntu\nVERSION_CODENAME=noble\n",
			rootKeys:      "# chave do painel\nssh-ed25519 AAAAC3Nza patrick@note\n",
		},
		users:  map[string]bool{"root": true},
		groups: map[string][]string{},
		pw:     map[string]bool{},
		active: map[string]bool{},
		docker: true,
		fail:   map[string]string{},
	}
}

func (f *fakeSys) Run(ctx context.Context, name string, args ...string) (string, error) {
	return f.RunInput(ctx, "", name, args...)
}

func (f *fakeSys) RunInput(_ context.Context, input, name string, args ...string) (string, error) {
	cmd := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, cmd)
	if input != "" {
		f.inputs = append(f.inputs, input)
	}
	for prefix, msg := range f.fail {
		if strings.HasPrefix(cmd, prefix) {
			return "", errors.New(msg)
		}
	}
	last := ""
	if len(args) > 0 {
		last = args[len(args)-1]
	}
	switch {
	case strings.HasSuffix(cmd, " full-upgrade") && strings.HasPrefix(cmd, "env ") && len(f.dpkgLines) > 0:
		for _, l := range f.dpkgLines {
			f.mu.Lock()
			f.files[dpkgLog] += l + "\n"
			f.mu.Unlock()
			time.Sleep(3 * dpkgEvery)
		}
	case strings.HasPrefix(cmd, "apt-get -s"):
		return f.aptSim, nil
	case cmd == "tailscale version":
		if f.tsState == "" {
			return "", errors.New("exec: \"tailscale\": executable file not found in $PATH")
		}
		return "1.90.1", nil
	case cmd == "tailscale status --json":
		st := map[string]any{"BackendState": f.tsState, "AuthURL": f.tsAuth}
		if f.tsState == "Running" {
			self := map[string]any{"DNSName": "srv1902144.tail1234.ts.net.", "TailscaleIPs": []string{"100.90.100.127", "fd7a:115c:a1e0::1"}}
			if f.tsExpiry != "" {
				self["KeyExpiry"] = f.tsExpiry
			}
			st["Self"] = self
			st["CurrentTailnet"] = map[string]any{"Name": "patrick@exemplo.com"}
		}
		b, _ := json.Marshal(st)
		if f.tsState != "Running" {
			return string(b), errors.New("exit status 1")
		}
		return string(b), nil
	case name == "curl" && strings.HasSuffix(last, ".tailscale-keyring.list"):
		return "# Tailscale packages for ubuntu noble\ndeb [signed-by=/usr/share/keyrings/tailscale-archive-keyring.gpg] https://pkgs.tailscale.com/stable/ubuntu noble main", nil
	case name == "env" && last == "tailscale" && slices.Contains(args, "install"):
		f.tsState = "NeedsLogin"
	case name == "systemd-run" && slices.Contains(args, "--unit="+TailscaleUpUnit):
		f.tsAuth = "https://login.tailscale.com/a/abc123"
	case strings.HasPrefix(cmd, "ufw allow in on tailscale0 to any port 22"):
		if !slices.Contains(f.ufwRules, "22/tcp on tailscale0") {
			f.ufwRules = append(f.ufwRules, "22/tcp on tailscale0")
		}
	case name == "ufw" && args[0] == "delete":
		f.ufwRules = slices.DeleteFunc(f.ufwRules, func(r string) bool { return r == last })
	case cmd == "getent group sudo":
		var members []string
		for u, gs := range f.groups {
			if slices.Contains(gs, "sudo") {
				members = append(members, u)
			}
		}
		slices.Sort(members)
		return "sudo:x:27:" + strings.Join(members, ","), nil
	case cmd == "getent group docker":
		if !f.docker {
			return "", errors.New("exit status 2")
		}
		var members []string
		for u, gs := range f.groups {
			if slices.Contains(gs, "docker") {
				members = append(members, u)
			}
		}
		slices.Sort(members)
		return "docker:x:999:" + strings.Join(members, ","), nil
	case name == "getent":
		if !f.users[last] {
			return "", errors.New("exit status 2")
		}
		return last + ":x:1000:1000::/home/" + last + ":/bin/bash", nil
	case name == "adduser":
		f.users[last] = true
	case cmd == "id -nG "+last:
		return strings.Join(append([]string{last}, f.groups[last]...), " "), nil
	case name == "gpasswd" && len(args) == 3:
		f.groups[args[1]] = slices.DeleteFunc(f.groups[args[1]], func(g string) bool { return g == last })
	case name == "usermod" && args[0] == "--expiredate":
		if f.expired == nil {
			f.expired = map[string]bool{}
		}
		f.expired[last] = args[1] == "1"
	case name == "usermod" && args[0] == "-aG":
		f.groups[last] = append(f.groups[last], args[1])
	case name == "passwd":
		if f.pw[last] {
			return last + " P 2026-11-24 0 99999 7 -1", nil
		}
		return last + " L 2026-11-24 0 99999 7 -1", nil
	case name == "chpasswd":
		u, _, _ := strings.Cut(input, ":")
		f.pw[u] = true
	case cmd == "ufw status verbose":
		out := "Status: inactive"
		if f.ufwOn {
			out = "Status: active\nLogging: on (low)\n"
			if f.ufwDeny {
				out += "Default: deny (incoming), allow (outgoing), deny (routed)\n"
			} else {
				out += "Default: allow (incoming), allow (outgoing), deny (routed)\n"
			}
			out += "New profiles: skip\n\nTo                         Action      From\n--                         ------      ----\n"
			for _, r := range f.ufwRules {
				out += r + "                     ALLOW IN    Anywhere\n"
				out += r + " (v6)                ALLOW IN    Anywhere (v6)\n"
			}
		}
		return out, nil
	case cmd == "ufw default deny incoming":
		f.ufwDeny = true
	case name == "ufw" && args[0] == "allow":
		if !slices.Contains(f.ufwRules, last) {
			f.ufwRules = append(f.ufwRules, last)
		}
	case cmd == "ufw --force enable":
		f.ufwOn = true
	case cmd == "sshd -T":
		if f.files[SSHDropin] == sshdHarden {
			return "port 22\npermitrootlogin no\npasswordauthentication no\nkbdinteractiveauthentication no\n", nil
		}
		return "port 22\npermitrootlogin yes\npasswordauthentication yes\nkbdinteractiveauthentication no\n", nil
	case strings.HasPrefix(cmd, "sshd -T -C"):
		return "pubkeyauthentication yes", nil
	case name == "systemctl" && args[0] == "is-active":
		if f.active[last] {
			return "active", nil
		}
		return "inactive", errors.New("exit status 3")
	case cmd == "systemctl restart fail2ban":
		f.active["fail2ban"] = true
	case name == "systemd-run" && slices.Contains(args, "--unit="+RebootAtUnit):
		f.active[RebootAtUnit+".timer"] = true
	case cmd == "systemctl stop "+RebootAtUnit+".timer":
		f.active[RebootAtUnit+".timer"] = false
	case name == "systemd-run":
		f.active[RollbackUnit+".timer"] = true
	case cmd == "systemctl stop "+RollbackUnit+".timer":
		f.active[RollbackUnit+".timer"] = false
	case name == "fail2ban-client":
		if !f.active["fail2ban"] {
			return "", errors.New("Failed to access socket path")
		}
	case name == "docker" && args[0] == "version":
		if !f.docker {
			return "", errors.New("Cannot connect to the Docker daemon")
		}
		return "27.3.1", nil
	case cmd == "docker info --format {{.LiveRestoreEnabled}}":
		return map[bool]string{true: "true", false: "false"}[f.liveRestore], nil
	case cmd == "docker info --format {{.Swarm.LocalNodeState}}":
		return map[bool]string{true: "active", false: "inactive"}[f.swarm], nil
	case cmd == "systemctl reload docker":
		// o reload le o live-restore do daemon.json
		f.liveRestore = strings.Contains(f.files[dockerDaemonPath], `"live-restore": true`)
	case name == "docker" && args[0] == "ps":
		return f.ps, nil
	case name == "ss":
		return f.ss, nil
	case name == "dpkg-query":
		if slices.ContainsFunc(f.calls, func(c string) bool { return strings.Contains(c, "install sudo ufw") }) {
			return "install ok installed", nil
		}
		return "", errors.New("no packages found")
	}
	return "", nil
}

func (f *fakeSys) ReadFile(path string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.files[path]; ok {
		return []byte(s), nil
	}
	return nil, fs.ErrNotExist
}

func (f *fakeSys) WriteFile(path string, data []byte, _ fs.FileMode) error {
	f.calls = append(f.calls, "write "+path)
	f.files[path] = string(data)
	return nil
}

func (f *fakeSys) Remove(path string) error {
	f.calls = append(f.calls, "rm "+path)
	delete(f.files, path)
	return nil
}

func (f *fakeSys) Exists(path string) bool { _, ok := f.files[path]; return ok }
func (f *fakeSys) Geteuid() int            { return f.euid }

func (f *fakeSys) index(t *testing.T, cmd string) int {
	t.Helper()
	i := slices.IndexFunc(f.calls, func(c string) bool { return strings.HasPrefix(c, cmd) })
	if i < 0 {
		t.Fatalf("nao rodou %q:\n%s", cmd, strings.Join(f.calls, "\n"))
	}
	return i
}

func harden(t *testing.T, f *fakeSys, opts Options) (Result, map[string]string, error) {
	t.Helper()
	status := map[string]string{}
	res, err := Harden(context.Background(), f, opts, func(s Step) { status[s.ID] = s.Status })
	return res, status, err
}

// VPS nova da Hostinger: so o root, com a chave do painel
func TestHardenVPSNova(t *testing.T) {
	f := newFake()
	res, status, err := harden(t, f, Options{User: "deploy", Password: "s3nha forte"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.PendingConfirm {
		t.Error("o SSH mudou: o confirm tem que ficar pendente")
	}
	for _, id := range []string{"packages", "updates", "docker-logs", "admin", "firewall", "fail2ban", "ssh"} {
		if status[id] != StepChanged {
			t.Errorf("%s: %s", id, status[id])
		}
	}
	if f.files["/home/deploy/.ssh/authorized_keys"] != "ssh-ed25519 AAAAC3Nza patrick@note\n" {
		t.Errorf("chaves do admin: %q", f.files["/home/deploy/.ssh/authorized_keys"])
	}
	if !slices.Contains(f.groups["deploy"], "sudo") || !slices.Contains(f.groups["deploy"], "docker") || !f.pw["deploy"] {
		t.Errorf("admin sem sudo ou sem senha: %v %v", f.groups, f.pw)
	}
	// a senha so pelo stdin
	if f.inputs[0] != "deploy:s3nha forte\n" || slices.ContainsFunc(f.calls, func(c string) bool { return strings.Contains(c, "s3nha") }) {
		t.Errorf("senha fora do stdin: %v", f.calls)
	}
	if !f.ufwOn || !f.ufwDeny || !slices.Equal(f.ufwRules, firewallPorts) {
		t.Errorf("ufw: on=%v deny=%v %v", f.ufwOn, f.ufwDeny, f.ufwRules)
	}
	// ordem: chaves do admin, firewall com o 22, a volta agendada e so
	// depois o reload do ssh
	keys, ufw := f.index(t, "write /home/deploy/.ssh/authorized_keys"), f.index(t, "ufw --force enable")
	allow22, rollback := f.index(t, "ufw allow 22/tcp"), f.index(t, "systemd-run")
	reload := f.index(t, "systemctl try-reload-or-restart ssh.service")
	if !(keys < allow22 && allow22 < ufw && ufw < rollback && rollback < reload) {
		t.Errorf("ordem errada:\n%s", strings.Join(f.calls, "\n"))
	}
	if !strings.Contains(f.calls[rollback], "--on-active=5min") || !strings.Contains(f.calls[rollback], "rm -f "+SSHDropin) {
		t.Errorf("volta automatica: %s", f.calls[rollback])
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(f.files[dockerDaemonPath]), &cfg); err != nil || cfg["log-driver"] != "local" {
		t.Errorf("daemon.json: %v %s", err, f.files[dockerDaemonPath])
	}

	// confirm de um login novo cancela a volta
	if ok, err := Confirm(context.Background(), f); !ok || err != nil {
		t.Fatalf("confirm: %v %v", ok, err)
	}
	if ok, _ := Confirm(context.Background(), f); ok {
		t.Error("segundo confirm: nada pendente")
	}

	// rodar de novo nao muda nada (fora o apt) e nao agenda outra volta
	f.calls = nil
	res, status, err = harden(t, f, Options{User: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if res.PendingConfirm {
		t.Error("nada mudou no SSH: sem confirm")
	}
	for _, id := range []string{"updates", "docker-logs", "admin", "firewall", "fail2ban", "ssh"} {
		if status[id] != StepOK {
			t.Errorf("segunda rodada, %s: %s", id, status[id])
		}
	}
	if slices.ContainsFunc(f.calls, func(c string) bool { return strings.HasPrefix(c, "systemd-run") || strings.HasPrefix(c, "write ") }) {
		t.Errorf("segunda rodada mudou algo:\n%s", strings.Join(f.calls, "\n"))
	}
}

func TestHardenRecusa(t *testing.T) {
	cases := map[string]struct {
		setup func(*fakeSys)
		opts  Options
		want  string
	}{
		"sem chave":   {func(f *fakeSys) { delete(f.files, rootKeys) }, Options{User: "deploy"}, "nenhuma chave SSH"},
		"sem root":    {func(f *fakeSys) { f.euid = 1000 }, Options{User: "deploy"}, "rode como root"},
		"arch":        {func(f *fakeSys) { f.files[osReleasePath] = "ID=arch\n" }, Options{User: "deploy"}, "so prepara Ubuntu e Debian"},
		"sem user":    {func(*fakeSys) {}, Options{}, "-user NOME"},
		"user root":   {func(*fakeSys) {}, Options{User: "root"}, "usuario proprio"},
		"user feio":   {func(*fakeSys) {}, Options{User: "Deploy;rm"}, "invalido"},
		"sem senha":   {func(*fakeSys) {}, Options{User: "deploy"}, "falta a senha do sudo"},
		"senha curta": {func(*fakeSys) {}, Options{User: "deploy", Password: "1234"}, "curta"},
		"sshd -t":     {func(f *fakeSys) { f.fail["sshd -t"] = "line 3: Bad configuration option" }, Options{User: "deploy", Password: "12345678"}, "o sshd recusou"},
		"systemdrun":  {func(f *fakeSys) { f.fail["systemd-run"] = "no bus" }, Options{User: "deploy", Password: "12345678"}, "volta automatica"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			c.setup(f)
			_, _, err := harden(t, f, c.opts)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("erro: %v", err)
			}
			if _, ok := f.files[SSHDropin]; ok {
				t.Error("a config do ssh ficou depois do erro")
			}
			if slices.ContainsFunc(f.calls, func(c string) bool { return strings.HasPrefix(c, "systemctl try-reload") }) {
				t.Error("recarregou o ssh depois do erro")
			}
		})
	}
}

// chave so pelo -key, e o admin ja existente com senha: nao pede outra
func TestPrepareAdminExistente(t *testing.T) {
	f := newFake()
	delete(f.files, rootKeys)
	f.users["deploy"], f.pw["deploy"] = true, true
	f.files["/home/deploy/.ssh/authorized_keys"] = "ssh-ed25519 AAAA velho\n"
	p, err := Prepare(context.Background(), f, Options{User: "deploy", Keys: "ssh-ed25519 BBBB novo\n"})
	if err != nil {
		t.Fatal(err)
	}
	if !p.UserExists || p.NeedPassword || len(p.Keys) != 2 || p.OS != "Ubuntu 24.04.1 LTS" {
		t.Errorf("%+v", p)
	}
}

func TestDockerLogsMantemOResto(t *testing.T) {
	f := newFake()
	f.files[dockerDaemonPath] = `{"data-root": "/data/docker", "log-opts": {"labels": "x"}}`
	h := &hardener{sys: f}
	if _, changed, err := h.dockerLogs(context.Background()); !changed || err != nil {
		t.Fatal(changed, err)
	}
	var cfg map[string]any
	json.Unmarshal([]byte(f.files[dockerDaemonPath]), &cfg)
	opts := cfg["log-opts"].(map[string]any)
	if cfg["data-root"] != "/data/docker" || opts["labels"] != "x" || opts["max-size"] != "10m" || cfg["log-driver"] != "local" {
		t.Errorf("%s", f.files[dockerDaemonPath])
	}
}

func checkByID(items []Item) map[string]Item {
	m := map[string]Item{}
	for _, it := range items {
		m[it.ID] = it
	}
	return m
}

func TestCheck(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("so no Linux")
	}
	// VPS nova: tudo aberto
	f := newFake()
	f.files[rebootRequiredPath] = ""
	f.ss = strings.Join([]string{
		"tcp   LISTEN 0 4096 0.0.0.0:22        0.0.0.0:*",
		"tcp   LISTEN 0 4096 [::]:22           [::]:*",
		"tcp   LISTEN 0 4096 127.0.0.1:5432    0.0.0.0:*",
		`tcp   LISTEN 0 4096 0.0.0.0:3306      0.0.0.0:* users:(("mysqld",pid=812,fd=21))`,
		"tcp   LISTEN 0 4096 0.0.0.0:5000      0.0.0.0:*",
		"udp   UNCONN 0 0    127.0.0.53%lo:53  0.0.0.0:*",
		"udp   UNCONN 0 0    0.0.0.0:68        0.0.0.0:*",
	}, "\n")
	f.ps = "proxy-traefik-1\t0.0.0.0:80->80/tcp, [::]:80->80/tcp, 0.0.0.0:443->443/tcp\nloja-web-1\t0.0.0.0:5000->80/tcp, :::5000->80/tcp\npostgres\t127.0.0.1:5432->5432/tcp"
	got := checkByID(Check(context.Background(), f))
	for _, id := range []string{"ssh-root", "ssh-password", "firewall", "fail2ban", "updates", "ports", "docker-logs", "reboot"} {
		if got[id].OK || got[id].Detail == "" {
			t.Errorf("%s deveria falhar: %+v", id, got[id])
		}
	}
	if !got["os"].OK {
		t.Errorf("os: %+v", got["os"])
	}
	if d := got["ports"].Detail; !strings.Contains(d, "3306/tcp (mysqld)") || !strings.Contains(d, "5000/tcp (container loja-web-1)") ||
		strings.Contains(d, "5432") || strings.Contains(d, "22/") || strings.Contains(d, ":53") || strings.Contains(d, "68/") {
		t.Errorf("portas: %s", d)
	}
	if !strings.Contains(got["ports"].Fix, "127.0.0.1:5000:80") {
		t.Errorf("conserto das portas: %s", got["ports"].Fix)
	}

	// depois do harden: tudo ok
	f = newFake()
	if _, _, err := harden(t, f, Options{User: "deploy", Password: "12345678"}); err != nil {
		t.Fatal(err)
	}
	f.active["fail2ban"] = true
	// com o Tailscale: o WireGuard dele e o que escuta so no IP da tailnet
	f.ss = strings.Join([]string{
		"tcp LISTEN 0 4096 0.0.0.0:22 0.0.0.0:*",
		"tcp LISTEN 0 4096 *:443 *:*",
		"udp UNCONN 0 0 0.0.0.0:41641 0.0.0.0:*",
		"udp UNCONN 0 0 [::]:41641 [::]:*",
		"tcp LISTEN 0 4096 100.90.100.127:60123 0.0.0.0:*",
		"tcp LISTEN 0 4096 [fd7a:115c:a1e0::1]:60123 [::]:*",
		// como o ss -tlnp mostra numa VPS de verdade: a interface depois do colchete
		`tcp LISTEN 0 4096 [fd7a:115c:a1e0::dc28:f44c]%tailscale0:59208 [::]:* users:(("tailscaled",pid=126376,fd=26))`,
		`tcp LISTEN 0 4096 100.118.244.75%tailscale0:41234 0.0.0.0:* users:(("tailscaled",pid=126376,fd=27))`,
	}, "\n")
	f.ps = "proxy-traefik-1\t0.0.0.0:443->443/tcp, 0.0.0.0:443->443/udp"
	// depois do harden falta so o Tailscale; com ele conectado e o SSH so
	// pela tailnet, tudo certo
	items := Check(context.Background(), f)
	if bad := Failed(items); len(bad) != 1 || bad[0].ID != "tailscale" {
		t.Errorf("depois do harden: %+v", bad)
	}
	f.tsState = "Running"
	if _, err := CloseSSH(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	if bad := Failed(Check(context.Background(), f)); len(bad) > 0 {
		t.Errorf("com o Tailscale: %+v", bad)
	}

	f.euid = 1000
	if items := Check(context.Background(), f); len(items) != 1 || items[0].ID != "root" {
		t.Errorf("sem root: %+v", items)
	}
}

// o verbose de uma VPS com o perfil do nginx: o nome do perfil nao e porta
func TestUFWPerfilDeApp(t *testing.T) {
	s := parseUFW(`Status: active
Default: deny (incoming), allow (outgoing), disabled (routed)

To                         Action      From
--                         ------      ----
22/tcp                     ALLOW IN    Anywhere
80,443/tcp (Nginx Full)    ALLOW IN    Anywhere
22/tcp on tailscale0       ALLOW IN    Anywhere
22/tcp (v6)                ALLOW IN    Anywhere (v6)
80,443/tcp (Nginx Full (v6)) ALLOW IN    Anywhere (v6)
22/tcp (v6) on tailscale0  ALLOW IN    Anywhere (v6)
`)
	if want := []string{"22/tcp", "80,443/tcp", "22/tcp on tailscale0"}; !slices.Equal(s.Allowed, want) {
		t.Errorf("%q", s.Allowed)
	}
	if extra := s.extraRules(); len(extra) > 0 {
		t.Errorf("%v", extra)
	}
}

func TestUFWRegrasExtras(t *testing.T) {
	s := ufwStatus{Allowed: []string{"OpenSSH", "22", "80/tcp", "443", "443/udp", "8080/tcp", "80,443/tcp", "3000:3010/tcp"}}
	if got := s.extraRules(); !slices.Equal(got, []string{"8080/tcp", "3000:3010/tcp"}) {
		t.Errorf("%v", got)
	}
}

// corrigir um item so: instala o pacote que falta e aplica o passo, sem
// mexer no SSH nem no admin
func TestFixUmItem(t *testing.T) {
	f := newFake()
	if _, err := Fix(context.Background(), f, "firewall"); err != nil {
		t.Fatal(err)
	}
	if !f.ufwOn || !slices.Equal(f.ufwRules, firewallPorts) {
		t.Errorf("ufw: %v %v", f.ufwOn, f.ufwRules)
	}
	f.index(t, "env DEBIAN_FRONTEND=noninteractive apt-get -q -y -o DPkg::Lock::Timeout=600 -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold install ufw")
	if slices.ContainsFunc(f.calls, func(c string) bool {
		return strings.Contains(c, "full-upgrade") || strings.Contains(c, "sshd") || strings.Contains(c, "adduser")
	}) {
		t.Errorf("mexeu alem do firewall:\n%s", strings.Join(f.calls, "\n"))
	}
	if _, err := Fix(context.Background(), f, "fail2ban"); err != nil || !f.active["fail2ban"] {
		t.Fatalf("fail2ban: %v", err)
	}
	// reiniciar: agendado (a resposta chega antes), nunca na hora
	f.calls = nil
	if d, err := Fix(context.Background(), f, "reboot"); err != nil || !strings.Contains(d, "5 segundos") {
		t.Fatalf("reboot: %q %v", d, err)
	}
	if i := f.index(t, "systemd-run --unit="+RebootUnit); !strings.Contains(f.calls[i], "--on-active=5s") ||
		!strings.HasSuffix(f.calls[i], "/bin/systemctl reboot") {
		t.Errorf("reboot: %s", f.calls[i])
	}
	if slices.ContainsFunc(f.calls, func(c string) bool { return strings.HasPrefix(c, "reboot") || c == "systemctl reboot" }) {
		t.Errorf("reiniciou na hora:\n%s", strings.Join(f.calls, "\n"))
	}
	if _, err := Fix(context.Background(), f, "ssh-root"); err == nil || !strings.Contains(err.Error(), "Preparar VPS") {
		t.Errorf("ssh-root avulso: %v", err)
	}

	// a checagem marca o que tem correcao avulsa
	if runtime.GOOS == "linux" {
		got := checkByID(Check(context.Background(), newFake()))
		if !got["firewall"].Fixable || !got["updates"].Fixable || got["ssh-root"].Fixable || got["ports"].Fixable {
			t.Errorf("fixable: %+v", got)
		}
	}
}

// install.sh --ssh com o root ainda sem chave (entrou por senha) e o
// ssh-copy-id so depois: o cais ficava sem chave e o ssh cais@vps dava
// Permission denied. A checagem avisa, o Corrigir e o harden copiam.
func TestChavesDoCais(t *testing.T) {
	f := newFake()
	f.users["cais"] = true
	f.files[caisSSHDropin] = "Match User cais\n"
	f.files["/home/cais/.ssh/authorized_keys"] = ""
	if runtime.GOOS == "linux" {
		got := checkByID(Check(context.Background(), f))
		if it := got["ssh-cais"]; it.OK || !it.Fixable || !strings.Contains(it.Detail, "Permission denied") {
			t.Errorf("cais sem chave: %+v", it)
		}
	}

	// Corrigir: as do root e as dos admins do sudo, sem repetir
	f.users["ops"] = true
	f.groups["ops"] = []string{"sudo"}
	f.files["/home/ops/.ssh/authorized_keys"] = "ssh-ed25519 OPS ops@pc\nssh-ed25519 AAAAC3Nza patrick@note"
	if _, err := Fix(context.Background(), f, "ssh-cais"); err != nil {
		t.Fatal(err)
	}
	if got := f.files["/home/cais/.ssh/authorized_keys"]; got != "ssh-ed25519 AAAAC3Nza patrick@note\nssh-ed25519 OPS ops@pc\n" {
		t.Errorf("chaves do cais: %q", got)
	}
	f.index(t, "chown -R cais: /home/cais/.ssh")
	if runtime.GOOS == "linux" {
		if it := checkByID(Check(context.Background(), f))["ssh-cais"]; !it.OK {
			t.Errorf("depois do Corrigir: %+v", it)
		}
	}

	// o harden tambem passa as chaves pro cais
	f = newFake()
	f.users["cais"] = true
	f.files[caisSSHDropin] = "Match User cais\n"
	_, status, err := harden(t, f, Options{User: "deploy", Password: "12345678"})
	if err != nil || status["ssh-cais"] != StepChanged {
		t.Fatalf("harden: %v %v", err, status)
	}
	if got := f.files["/home/cais/.ssh/authorized_keys"]; got != "ssh-ed25519 AAAAC3Nza patrick@note\n" {
		t.Errorf("harden, chaves do cais: %q", got)
	}

	// sem o usuario cais (install.sh sem --ssh): nem checa nem mexe
	f = newFake()
	if _, status, err := harden(t, f, Options{User: "deploy", Password: "12345678"}); err != nil || status["ssh-cais"] != StepOK {
		t.Fatalf("harden sem cais: %v %v", err, status)
	}
	if _, ok := f.files["/home/cais/.ssh/authorized_keys"]; ok {
		t.Error("criou chave pra um cais que nao existe")
	}
	if runtime.GOOS == "linux" {
		if _, ok := checkByID(Check(context.Background(), f))["ssh-cais"]; ok {
			t.Error("checou o cais sem o usuario")
		}
	}
	// sem nenhuma chave de admin pra copiar: diz o que fazer
	f.users["cais"] = true
	f.files[caisSSHDropin] = ""
	delete(f.files, rootKeys)
	f.files["/home/deploy/.ssh/authorized_keys"] = ""
	if _, err := Fix(context.Background(), f, "ssh-cais"); err == nil || !strings.Contains(err.Error(), "ssh-copy-id") {
		t.Errorf("sem chave nenhuma: %v", err)
	}
}

// o Tailscale: instala, mostra o link de login, e com ele conectado o SSH
// fecha pra internet (com a volta agendada antes) e o harden nao reabre
func TestTailscale(t *testing.T) {
	loginWait, loginPoll = time.Second, time.Millisecond
	t.Cleanup(func() { loginWait, loginPoll = 30*time.Second, 500*time.Millisecond })
	ctx := context.Background()
	f := newFake()
	f.ufwOn, f.ufwDeny, f.ufwRules = true, true, slices.Clone(firewallPorts)

	if runtime.GOOS == "linux" {
		got := checkByID(Check(ctx, f))
		if it := got["tailscale"]; it.OK || !strings.Contains(it.Detail, "nao instalado") || it.Fixable {
			t.Errorf("sem tailscale: %+v", it)
		}
		if _, ok := got["ssh-tailnet"]; ok {
			t.Error("ssh-tailnet sem o tailscale conectado")
		}
	}
	if _, err := CloseSSH(ctx, f); err == nil || !strings.Contains(err.Error(), "conecte o Tailscale") {
		t.Fatalf("fechar sem tailscale: %v", err)
	}

	ts, err := ConnectTailscale(ctx, f)
	if err != nil || ts.AuthURL != "https://login.tailscale.com/a/abc123" || ts.Connected() {
		t.Fatalf("conectar: %+v %v", ts, err)
	}
	if !strings.Contains(f.files["/etc/apt/sources.list.d/tailscale.list"], "https://pkgs.tailscale.com/stable/ubuntu noble main") {
		t.Errorf("repositorio: %q", f.files["/etc/apt/sources.list.d/tailscale.list"])
	}
	up := f.index(t, "systemd-run --unit="+TailscaleUpUnit)
	if !strings.HasSuffix(f.calls[up], "tailscale up --accept-dns=false") {
		t.Errorf("tailscale up: %s", f.calls[up])
	}

	// autorizado no navegador; a chave vence em 180 dias (o padrao)
	f.tsState, f.tsAuth = "Running", ""
	f.tsExpiry = time.Now().Add(180 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if runtime.GOOS == "linux" {
		got := checkByID(Check(ctx, f))
		if it := got["tailscale-key"]; it.OK || !strings.Contains(it.Detail, "vence em") || !strings.Contains(it.Detail, "(179 dias)") ||
			!strings.Contains(it.Fix, "Disable key expiry") {
			t.Errorf("chave vencendo: %+v", it)
		}
	}
	f.tsExpiry = "" // Disable key expiry no painel
	if ts, err := ConnectTailscale(ctx, f); err != nil || !ts.Connected() || ts.IP4() != "100.90.100.127" || ts.Name != "srv1902144.tail1234.ts.net" {
		t.Fatalf("conectado: %+v %v", ts, err)
	}
	if runtime.GOOS == "linux" {
		got := checkByID(Check(ctx, f))
		if !got["tailscale"].OK || !got["tailscale-key"].OK || got["ssh-tailnet"].OK {
			t.Errorf("conectado, 22 aberto: %+v %+v", got["tailscale"], got["ssh-tailnet"])
		}
	}

	f.calls = nil
	res, err := CloseSSH(ctx, f)
	if err != nil || !res.PendingConfirm {
		t.Fatalf("fechar o SSH: %+v %v", res, err)
	}
	allow, rollback, del := f.index(t, "ufw allow in on tailscale0"), f.index(t, "systemd-run --unit="+RollbackUnit), f.index(t, "ufw delete allow 22/tcp")
	if !(allow < rollback && rollback < del) || !strings.HasSuffix(f.calls[rollback], "/usr/sbin/ufw allow 22/tcp") {
		t.Errorf("ordem (liberar na tailnet, agendar a volta, fechar):\n%s", strings.Join(f.calls, "\n"))
	}
	if !SSHTailnetOnly(ctx, f) || slices.Contains(f.ufwRules, "22/tcp") {
		t.Fatalf("regras: %v", f.ufwRules)
	}
	if res, err := CloseSSH(ctx, f); err != nil || res.PendingConfirm {
		t.Errorf("fechar de novo: %+v %v", res, err)
	}
	if runtime.GOOS == "linux" {
		got := checkByID(Check(ctx, f))
		if !got["ssh-tailnet"].OK || !got["firewall"].OK || !strings.Contains(got["firewall"].Detail, "so 80 e 443") {
			t.Errorf("fechado: %+v %+v", got["ssh-tailnet"], got["firewall"])
		}
	}

	// o firewall do harden nao abre o 22 de novo
	f.calls = nil
	if _, err := Fix(ctx, f, "firewall"); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(f.ufwRules, "22/tcp") || slices.ContainsFunc(f.calls, func(c string) bool { return c == "ufw allow 22/tcp" }) {
		t.Errorf("o firewall reabriu o 22: %v", f.ufwRules)
	}
}

// o history.log e o apt-get -s como no Ubuntu 26.04 (capturados num
// container): so conta a atualizacao feita a mao; a do unattended-upgrades
// e as instalacoes nao
const aptHistoryLog = `
Start-Date: 2026-10-16  17:16:32
Commandline: apt-get --yes -oDebug::pkgDepCache::AutoInstall=yes --no-install-recommends --force-yes upgrade
Upgrade: libc-bin:amd64 (2.43-2ubuntu2.1, 2.43-2ubuntu2.4)
End-Date: 2026-10-16  17:16:40

Start-Date: 2026-11-20  20:17:21
Commandline: /usr/bin/unattended-upgrade -v
Upgrade: libc-bin:amd64 (2.43-2ubuntu2.4, 2.43-2ubuntu2.7)
End-Date: 2026-11-20  20:17:28

Start-Date: 2026-11-21  09:00:00
Commandline: apt-get install -y -qq --allow-downgrades diffutils=1:3.12-1
Downgrade: diffutils:amd64 (1:3.12-1ubuntu0.1, 1:3.12-1)
End-Date: 2026-11-21  09:00:02
`

const aptSimulation = `Reading package lists...
Building dependency tree...
Calculating upgrade...
The following packages will be upgraded:
  base-files diffutils distro-info-data
3 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.
Inst base-files [14ubuntu6] (14ubuntu6.3 Ubuntu:26.04/resolute-updates [amd64])
Inst diffutils [1:3.12-1] (1:3.12-1ubuntu0.1 Ubuntu:26.04/resolute-updates [amd64])
Inst distro-info-data [0.68build1] (2026.10.12-0ubuntu0.26.04.1 Ubuntu:26.04/resolute-updates [all])
Conf base-files (14ubuntu6.3 Ubuntu:26.04/resolute-updates [amd64])
Conf diffutils (1:3.12-1ubuntu0.1 Ubuntu:26.04/resolute-updates [amd64])
Conf distro-info-data (2026.10.12-0ubuntu0.26.04.1 Ubuntu:26.04/resolute-updates [all])
`

func TestPacotesDoSistema(t *testing.T) {
	want := time.Date(2026, 10, 16, 17, 16, 32, 0, time.Local)
	if got := lastUpgradeIn(aptHistoryLog); !got.Equal(want) {
		t.Fatalf("ultima a mao: %v", got)
	}
	ctx := context.Background()
	f := newFake()
	f.aptSim = aptSimulation
	if n, _ := pendingUpgrades(ctx, f); n != 3 {
		t.Fatalf("esperando: %d", n)
	}

	// o do mes vazio, o anterior comprimido (o logrotate)
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write([]byte(aptHistoryLog))
	zw.Close()
	f.files["/var/log/apt/history.log"] = ""
	f.files["/var/log/apt/history.log.1.gz"] = gz.String()
	if it := checkUpgrades(ctx, f); it.OK || !strings.Contains(it.Detail, "3 pacotes esperando") || !strings.Contains(it.Detail, "hora de atualizar") {
		t.Errorf("40 dias: %+v", it)
	}

	// atualizado a mao ha pouco: esperando, mas dentro do mes
	recent := time.Now().Add(-5 * 24 * time.Hour).Format("2006-01-02  15:04:05")
	f.files["/var/log/apt/history.log"] = "Start-Date: " + recent + "\nCommandline: apt-get -q -y full-upgrade\nEnd-Date: " + recent + "\n"
	if it := checkUpgrades(ctx, f); !it.OK || !strings.Contains(it.Detail, "3 esperando; ultima atualizacao ha 5 dias; a proxima ate") {
		t.Errorf("5 dias: %+v", it)
	}

	// nada esperando: em dia
	f.aptSim = "0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n"
	if it := checkUpgrades(ctx, f); !it.OK || !strings.HasPrefix(it.Detail, "em dia") {
		t.Errorf("em dia: %+v", it)
	}

	// o Corrigir: update e full-upgrade, e o item tem correcao avulsa
	f.aptSim, f.calls = aptSimulation, nil
	if d, err := Fix(ctx, f, "upgrades"); err != nil || d != "3 pacotes atualizados" {
		t.Fatalf("corrigir: %q %v", d, err)
	}
	upd, full := f.index(t, "env DEBIAN_FRONTEND=noninteractive apt-get -q -y -o DPkg::Lock::Timeout=600 -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold update"),
		f.index(t, "env DEBIAN_FRONTEND=noninteractive apt-get -q -y -o DPkg::Lock::Timeout=600 -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold full-upgrade")
	if upd > full {
		t.Error("full-upgrade antes do update")
	}
	if runtime.GOOS == "linux" {
		f.files["/var/log/apt/history.log"] = ""
		if it := checkByID(Check(ctx, f))["upgrades"]; it.OK || !it.Fixable {
			t.Errorf("na checagem: %+v", it)
		}
	}
}

// o Preparar VPS conta tudo de cara (todos os passos pendentes) e o
// andamento da atualizacao: a fase com o total e quantos o dpkg ja instalou
func TestHardenAndamento(t *testing.T) {
	old := dpkgEvery
	dpkgEvery = 5 * time.Millisecond
	t.Cleanup(func() { dpkgEvery = old })
	f := newFake()
	f.aptSim = "Inst libc6 [2.39-0] (2.39-1 Ubuntu:24.04/noble-updates [amd64])\nInst openssl [3.0.13-0] (3.0.13-1 Ubuntu:24.04/noble-updates [amd64])\n"
	f.files[dpkgLog] = "2026-10-01 10:00:00 upgrade velho:amd64 1 2\n"
	f.dpkgLines = []string{"2026-11-27 12:40:01 upgrade libc6:amd64 2.39-0 2.39-1", "2026-11-27 12:40:02 status installed libc6:amd64 2.39-1",
		"2026-11-27 12:40:03 upgrade openssl:amd64 3.0.13-0 3.0.13-1"}
	var steps []Step
	_, err := Harden(context.Background(), f, Options{User: "deploy", Password: "12345678"}, func(s Step) { steps = append(steps, s) })
	if err != nil {
		t.Fatal(err)
	}
	for i := range 8 {
		if steps[i].Status != StepPending {
			t.Fatalf("passo %d nao comecou pendente: %+v", i, steps[i])
		}
	}
	var progress []string
	phase := false
	for _, s := range steps {
		if s.ID == "packages" && s.Status == StepRunning {
			phase = phase || strings.Contains(s.Detail, "atualizando 2 pacotes")
			if s.Progress != "" && (len(progress) == 0 || progress[len(progress)-1] != s.Progress) {
				progress = append(progress, s.Progress)
			}
			if s.Started.IsZero() {
				t.Error("rodando sem a hora do comeco")
			}
		}
		if s.ID == "packages" && s.Status == StepChanged && s.Detail != "2 pacotes atualizados" {
			t.Errorf("fim: %+v", s)
		}
	}
	if !phase || !slices.Equal(progress, []string{"baixando", "1 de 2 instalados", "2 de 2 instalados"}) {
		t.Errorf("fase %v, andamento %q", phase, progress)
	}
}

// o passo do Docker liga o live-restore pelo reload (sem restart), mantem
// o que ja estava no daemon.json e fica de fora num swarm
func TestHardenDockerLiveRestore(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	f.files[dockerDaemonPath] = `{"log-driver":"json-file","log-opts":{"max-size":"10m","max-file":"3"}}`
	h := &hardener{sys: f}
	if it := checkDockerLogs(ctx, f); it.OK || !strings.Contains(it.Detail, "sem o live-restore") {
		t.Fatalf("antes: %+v", it)
	}
	detail, changed, err := h.dockerLogs(ctx)
	if err != nil || !changed || !strings.Contains(detail, "live-restore ligado") {
		t.Fatalf("%q %v %v", detail, changed, err)
	}
	if !strings.Contains(f.files[dockerDaemonPath], `"log-driver": "json-file"`) || !f.liveRestore {
		t.Errorf("daemon.json: %s (live %v)", f.files[dockerDaemonPath], f.liveRestore)
	}
	if slices.ContainsFunc(f.calls, func(c string) bool { return strings.Contains(c, "restart docker") }) {
		t.Error("reiniciou o Docker")
	}
	if it := checkDockerLogs(ctx, f); !it.OK {
		t.Errorf("depois: %+v", it)
	}
	if _, changed, _ := h.dockerLogs(ctx); changed {
		t.Error("segunda rodada mudou")
	}

	g := newFake()
	g.swarm = true
	if _, _, err := (&hardener{sys: g}).dockerLogs(ctx); err != nil || strings.Contains(g.files[dockerDaemonPath], "live-restore") {
		t.Errorf("swarm: %v %s", err, g.files[dockerDaemonPath])
	}
	if it := checkDockerLogs(ctx, g); !it.OK {
		t.Errorf("swarm: %+v", it)
	}
}

// a atualizacao avisa quando inclui o Docker (e se os containers caem)
func TestUpgradeAvisaDoDocker(t *testing.T) {
	for live, want := range map[bool]string{true: "inclui o Docker (os containers ficam no ar)", false: "inclui o Docker: os containers reiniciam junto"} {
		f := newFake()
		f.liveRestore = live
		f.aptSim = "Inst docker-ce [5:29.0.1-1] (5:29.1.5-1 Docker CE:noble [amd64])\nInst libc6 [2.39-0] (2.39-1 Ubuntu:24.04/noble-updates [amd64])\n"
		var phases []string
		h := &hardener{sys: f, note: func(d, _ string) { phases = append(phases, d) }}
		if n, err := h.fullUpgrade(context.Background()); err != nil || n != 2 {
			t.Fatalf("%d %v", n, err)
		}
		if !slices.ContainsFunc(phases, func(p string) bool { return strings.Contains(p, want) }) {
			t.Errorf("live=%v: %q", live, phases)
		}
	}
}
