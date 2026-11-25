package host

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"runtime"
	"slices"
	"strings"
	"testing"
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
	ss       string
	ps       string
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
	case cmd == "getent group sudo":
		var members []string
		for u, gs := range f.groups {
			if slices.Contains(gs, "sudo") {
				members = append(members, u)
			}
		}
		slices.Sort(members)
		return "sudo:x:27:" + strings.Join(members, ","), nil
	case name == "getent":
		if !f.users[last] {
			return "", errors.New("exit status 2")
		}
		return last + ":x:1000:1000::/home/" + last + ":/bin/bash", nil
	case name == "adduser":
		f.users[last] = true
	case cmd == "id -nG "+last:
		return strings.Join(append([]string{last}, f.groups[last]...), " "), nil
	case strings.HasPrefix(cmd, "usermod -aG sudo"):
		f.groups[last] = append(f.groups[last], "sudo")
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
	if !slices.Contains(f.groups["deploy"], "sudo") || !f.pw["deploy"] {
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
		"tcp   LISTEN 0 4096 0.0.0.0:3306      0.0.0.0:*",
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
	if d := got["ports"].Detail; !strings.Contains(d, "3306/tcp") || !strings.Contains(d, "5000/tcp (container loja-web-1)") ||
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
	f.ss = "tcp LISTEN 0 4096 0.0.0.0:22 0.0.0.0:*\ntcp LISTEN 0 4096 *:443 *:*"
	f.ps = "proxy-traefik-1\t0.0.0.0:443->443/tcp, 0.0.0.0:443->443/udp"
	items := Check(context.Background(), f)
	if bad := Failed(items); len(bad) > 0 {
		t.Errorf("depois do harden: %+v", bad)
	}

	f.euid = 1000
	if items := Check(context.Background(), f); len(items) != 1 || items[0].ID != "root" {
		t.Errorf("sem root: %+v", items)
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
