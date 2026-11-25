package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Options e o que o harden recebe do CLI.
type Options struct {
	// User e o usuario admin (sudo, so chave) que substitui o root no SSH.
	User string
	// Keys sao chaves publicas a mais pro admin (conteudo de um .pub ou
	// authorized_keys); as do root ja entram.
	Keys string
	// Password e a senha do sudo do admin; vazia mantem a atual.
	Password string
}

// Plan e o que o harden vai encontrar, antes de mudar algo.
type Plan struct {
	OS           string
	UserExists   bool
	NeedPassword bool     // admin novo ou sem senha: o sudo pede uma
	Keys         []string // chaves que o admin vai ter
}

// Arquivos que o harden escreve.
const (
	SSHDropin    = "/etc/ssh/sshd_config.d/00-cais-harden.conf"
	fail2banJail = "/etc/fail2ban/jail.d/cais.conf"
	rootKeys     = "/root/.ssh/authorized_keys"
	// CaisUser e o usuario do ssh cais@vps (a TUI), criado pelo install.sh
	// --ssh com o comando forcado no caisSSHDropin.
	CaisUser      = "cais"
	caisSSHDropin = "/etc/ssh/sshd_config.d/cais.conf"
	RollbackUnit  = "cais-ssh-rollback"
	RollbackAfter = "5min"
	// RollbackDelay e o RollbackAfter como duracao (contagem na TUI).
	RollbackDelay = 5 * time.Minute
)

// O 00- vem antes do 50-cloud-init.conf (que liga a senha nas imagens de
// nuvem): no sshd vale o primeiro valor lido.
const sshdHarden = `# Escrito pelo cais host harden: SSH so com chave e sem o root.
# Entre com o usuario admin (sudo). Pra desfazer: apague este arquivo e
# rode systemctl reload ssh.
PermitRootLogin no
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
X11Forwarding no
`

// backend systemd: o Ubuntu 24.04 e o Debian 12 nao tem mais o auth.log.
const fail2banConf = `# Escrito pelo cais host harden.
[sshd]
enabled = true
backend = systemd
maxretry = 5
findtime = 10m
bantime = 1h
`

// So as de seguranca (padrao do 50unattended-upgrades) e sem reiniciar
// sozinho; o cais avisa quando um reinicio fica pendente.
const autoUpgrades = `APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
APT::Periodic::AutocleanInterval "7";
`

var validUser = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// Prepare confere se da pra rodar o harden e devolve o plano. Recusa sem
// root, fora do Ubuntu/Debian e sem nenhuma chave SSH pro admin (sem chave,
// desligar a senha trancaria todo mundo do lado de fora).
func Prepare(ctx context.Context, sys System, opts Options) (Plan, error) {
	var p Plan
	if sys.Geteuid() != 0 {
		return p, errors.New("rode como root: sudo cais host harden -user " + cmpOr(opts.User, "NOME"))
	}
	r, err := readOSRelease(sys)
	if err != nil || !r.Supported() {
		return p, errors.New("o cais host harden so prepara Ubuntu e Debian (" + cmpOr(r.String(), "sistema desconhecido") + ")")
	}
	p.OS = r.String()
	switch {
	case opts.User == "":
		return p, errors.New("falta o usuario admin: -user NOME (ex: -user deploy)")
	case opts.User == "root" || opts.User == "cais":
		return p, errors.New("o admin precisa ser um usuario proprio (root e cais nao servem): ex -user deploy")
	case !validUser.MatchString(opts.User):
		return p, errors.New("nome de usuario invalido: use minusculas, numeros, _ e -, comecando por letra")
	}

	home, exists := userHome(ctx, sys, opts.User)
	p.UserExists = exists
	p.Keys = parseKeys(opts.Keys)
	if b, err := sys.ReadFile(rootKeys); err == nil {
		p.Keys = mergeKeys(p.Keys, parseKeys(string(b)))
	}
	if exists {
		if b, err := sys.ReadFile(home + "/.ssh/authorized_keys"); err == nil {
			p.Keys = mergeKeys(p.Keys, parseKeys(string(b)))
		}
		out, _ := sys.Run(ctx, "passwd", "-S", opts.User)
		f := strings.Fields(out)
		p.NeedPassword = len(f) < 2 || f[1] != "P"
	} else {
		p.NeedPassword = true
	}
	if len(p.Keys) == 0 {
		return p, errors.New("nenhuma chave SSH pro admin: sem chave, desligar a senha tranca voce fora.\n" +
			"Do seu computador: ssh-copy-id root@<ip-da-vps> (ou cadastre a chave no painel da Hostinger),\n" +
			"ou passe o arquivo: -key minha-chave.pub")
	}
	return p, nil
}

// userHome e a pasta do usuario (getent passwd) e se ele existe.
func userHome(ctx context.Context, sys System, user string) (string, bool) {
	out, err := sys.Run(ctx, "getent", "passwd", user)
	if err != nil || out == "" {
		return "", false
	}
	f := strings.Split(out, ":")
	if len(f) < 6 {
		return "", false
	}
	return f[5], true
}

// parseKeys sao as linhas de chave publica (sem comentario nem linha vazia).
func parseKeys(s string) []string {
	var keys []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		keys = append(keys, l)
	}
	return keys
}

func mergeKeys(a, b []string) []string {
	for _, k := range b {
		if !slices.Contains(a, k) {
			a = append(a, k)
		}
	}
	return a
}

// Status de um passo do harden.
const (
	StepRunning = "running"
	StepChanged = "changed" // mudou algo
	StepOK      = "ok"      // ja estava certo
	StepFailed  = "failed"
)

// Step e um passo do harden, pro progresso.
type Step struct {
	ID, Desc, Status, Detail string
}

// Result e o fim do harden.
type Result struct {
	// PendingConfirm: o SSH mudou e volta sozinho em RollbackAfter sem o
	// cais host confirm (dado de um login novo).
	PendingConfirm bool
}

type hardener struct {
	sys    System
	opts   Options
	plan   Plan
	result Result
}

type stepFunc func(ctx context.Context) (detail string, changed bool, err error)

// Harden aplica a seguranca na VPS, um passo por vez, parando no primeiro
// erro. Cada passo confere antes e so muda o que falta: rodar de novo e
// seguro. O SSH vem por ultimo, depois do admin com as chaves, e volta
// sozinho se ninguem confirmar de um login novo.
func Harden(ctx context.Context, sys System, opts Options, progress func(Step)) (Result, error) {
	plan, err := Prepare(ctx, sys, opts)
	if err != nil {
		return Result{}, err
	}
	if plan.NeedPassword && opts.Password == "" {
		return Result{}, errors.New("falta a senha do sudo do " + opts.User + " (o admin e novo ou esta sem senha)")
	}
	if opts.Password != "" && len(opts.Password) < 8 {
		return Result{}, errors.New("senha do sudo curta: use pelo menos 8 caracteres")
	}
	if strings.ContainsAny(opts.Password, "\n\r") {
		return Result{}, errors.New("a senha do sudo nao pode ter quebra de linha")
	}
	if progress == nil {
		progress = func(Step) {}
	}
	h := &hardener{sys: sys, opts: opts, plan: plan}
	steps := []struct {
		id, desc string
		run      stepFunc
	}{
		{"packages", "Atualizar o sistema e instalar ufw, fail2ban e unattended-upgrades", h.packages},
		{"updates", "Atualizacoes de seguranca automaticas", h.updates},
		{"docker-logs", "Limite nos logs dos containers", h.dockerLogs},
		{"admin", "Usuario admin " + opts.User + " (sudo, entra so com chave)", h.admin},
		{"ssh-cais", "Chaves do usuario cais (ssh cais@vps abre a TUI)", h.caisKeys},
		{"firewall", "Firewall: entram so 22, 80 e 443", h.firewall},
		{"fail2ban", "fail2ban no SSH", h.fail2ban},
		{"ssh", "SSH sem root e sem senha", h.ssh},
	}
	for _, s := range steps {
		progress(Step{ID: s.id, Desc: s.desc, Status: StepRunning})
		detail, changed, err := s.run(ctx)
		if err != nil {
			progress(Step{ID: s.id, Desc: s.desc, Status: StepFailed, Detail: err.Error()})
			return h.result, errors.New(s.desc + ": " + err.Error())
		}
		st := StepOK
		if changed {
			st = StepChanged
		}
		progress(Step{ID: s.id, Desc: s.desc, Status: st, Detail: detail})
	}
	return h.result, nil
}

// apt roda o apt-get sem perguntas, mantendo os arquivos de config editados
// e esperando a trava do apt (numa VPS nova o unattended-upgrades costuma
// estar rodando).
func (h *hardener) apt(ctx context.Context, args ...string) error {
	base := []string{"DEBIAN_FRONTEND=noninteractive", "apt-get", "-q", "-y",
		"-o", "DPkg::Lock::Timeout=600",
		"-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold"}
	_, err := h.sys.Run(ctx, "env", append(base, args...)...)
	return err
}

func (h *hardener) packages(ctx context.Context) (string, bool, error) {
	if err := h.apt(ctx, "update"); err != nil {
		return "", false, err
	}
	if err := h.apt(ctx, "full-upgrade"); err != nil {
		return "", false, err
	}
	if err := h.apt(ctx, "install", "sudo", "ufw", "fail2ban", "python3-systemd", "unattended-upgrades"); err != nil {
		return "", false, err
	}
	return "", true, nil
}

// writeIfChanged grava o arquivo se o conteudo for outro; diz se mudou.
func (h *hardener) writeIfChanged(path, content string) (bool, error) {
	if b, err := h.sys.ReadFile(path); err == nil && string(b) == content {
		return false, nil
	}
	return true, h.sys.WriteFile(path, []byte(content), 0o644)
}

func (h *hardener) updates(ctx context.Context) (string, bool, error) {
	changed, err := h.writeIfChanged(autoUpgradesPath, autoUpgrades)
	if err != nil {
		return "", false, err
	}
	if _, err := h.sys.Run(ctx, "systemctl", "enable", "--now", "unattended-upgrades"); err != nil {
		return "", false, err
	}
	return "so as de seguranca, sem reiniciar sozinho", changed, nil
}

func (h *hardener) dockerLogs(ctx context.Context) (string, bool, error) {
	if _, err := h.sys.Run(ctx, "docker", "version", "--format", "{{.Server.Version}}"); err != nil {
		return "Docker nao responde: pulado (o install.sh do cais instala)", false, nil
	}
	cfg := map[string]any{}
	if b, err := h.sys.ReadFile(dockerDaemonPath); err == nil {
		if err := json.Unmarshal(b, &cfg); err != nil {
			return "", false, errors.New(dockerDaemonPath + " invalido: " + err.Error())
		}
	}
	if dockerLogsLimited(cfg) {
		return "", false, nil
	}
	if _, ok := cfg["log-driver"]; !ok {
		cfg["log-driver"] = "local"
	}
	opts, _ := cfg["log-opts"].(map[string]any)
	if opts == nil {
		opts = map[string]any{}
	}
	opts["max-size"], opts["max-file"] = "10m", "3"
	cfg["log-opts"] = opts
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", false, err
	}
	if err := h.sys.WriteFile(dockerDaemonPath, append(b, '\n'), 0o644); err != nil {
		return "", false, err
	}
	// so os containers criados depois pegam; o restart do docker para os
	// que estao no ar, entao fica pro proximo deploy
	return "vale pros containers criados daqui pra frente (proximo deploy); reinicie o Docker num horario calmo pra valer ja", true, nil
}

func (h *hardener) admin(ctx context.Context) (string, bool, error) {
	u := h.opts.User
	changed := false
	if !h.plan.UserExists {
		if _, err := h.sys.Run(ctx, "adduser", "--disabled-password", "--gecos", "", u); err != nil {
			return "", false, err
		}
		changed = true
	}
	if out, _ := h.sys.Run(ctx, "id", "-nG", u); !slices.Contains(strings.Fields(out), "sudo") {
		if _, err := h.sys.Run(ctx, "usermod", "-aG", "sudo", u); err != nil {
			return "", false, err
		}
		changed = true
	}
	home, ok := userHome(ctx, h.sys, u)
	if !ok {
		return "", false, errors.New("usuario " + u + " nao encontrado depois de criar")
	}
	dir, file := home+"/.ssh", home+"/.ssh/authorized_keys"
	var current []string
	if b, err := h.sys.ReadFile(file); err == nil {
		current = parseKeys(string(b))
	}
	if keys := mergeKeys(current, h.plan.Keys); len(keys) != len(current) {
		if _, err := h.sys.Run(ctx, "install", "-d", "-m", "700", dir); err != nil {
			return "", false, err
		}
		if err := h.sys.WriteFile(file, []byte(strings.Join(keys, "\n")+"\n"), 0o600); err != nil {
			return "", false, err
		}
		if _, err := h.sys.Run(ctx, "chown", "-R", u+":", dir); err != nil {
			return "", false, err
		}
		changed = true
	}
	if h.opts.Password != "" {
		if _, err := h.sys.RunInput(ctx, u+":"+h.opts.Password+"\n", "chpasswd"); err != nil {
			return "", false, errors.New("senha do sudo: " + firstLine(err))
		}
		changed = true
	}
	return "", changed, nil
}

func (h *hardener) caisKeys(ctx context.Context) (string, bool, error) {
	return caisKeys(ctx, h.sys, h.plan.Keys)
}

// hasCaisUser diz se o install.sh --ssh criou o usuario cais (e onde fica a
// pasta dele).
func hasCaisUser(ctx context.Context, sys System) (string, bool) {
	home, ok := userHome(ctx, sys, CaisUser)
	return home, ok && sys.Exists(caisSSHDropin)
}

// adminKeys sao as chaves de quem ja administra a VPS: o root e os do grupo
// sudo.
func adminKeys(ctx context.Context, sys System) []string {
	var keys []string
	if b, err := sys.ReadFile(rootKeys); err == nil {
		keys = parseKeys(string(b))
	}
	out, _ := sys.Run(ctx, "getent", "group", "sudo")
	if f := strings.Split(out, ":"); len(f) == 4 && f[3] != "" {
		for _, u := range strings.Split(f[3], ",") {
			if home, ok := userHome(ctx, sys, u); ok {
				if b, err := sys.ReadFile(home + "/.ssh/authorized_keys"); err == nil {
					keys = mergeKeys(keys, parseKeys(string(b)))
				}
			}
		}
	}
	return keys
}

// caisKeys da ao usuario cais as chaves dos admins (root, grupo sudo e as
// extra do harden). O install.sh so copia as que o root tem na hora: quem
// instala entrando por senha e so depois faz o ssh-copy-id ficava com o cais
// sem chave (Permission denied no ssh cais@vps). Quem e admin ja abre a TUI
// com sudo cais: a chave dele no cais nao da acesso novo.
func caisKeys(ctx context.Context, sys System, extra []string) (string, bool, error) {
	home, ok := hasCaisUser(ctx, sys)
	if !ok {
		return "sem o usuario cais (install.sh sem --ssh): pulado", false, nil
	}
	dir, file := home+"/.ssh", home+"/.ssh/authorized_keys"
	var current []string
	if b, err := sys.ReadFile(file); err == nil {
		current = parseKeys(string(b))
	}
	keys := mergeKeys(mergeKeys(slices.Clone(current), extra), adminKeys(ctx, sys))
	if len(keys) == 0 {
		return "", false, errors.New("nenhuma chave dos admins pra copiar: do seu computador, ssh-copy-id root@<ip-da-vps> (ou o seu admin) e corrija de novo")
	}
	if len(keys) == len(current) {
		return "", false, nil
	}
	if _, err := sys.Run(ctx, "install", "-d", "-m", "700", dir); err != nil {
		return "", false, err
	}
	if err := sys.WriteFile(file, []byte(strings.Join(keys, "\n")+"\n"), 0o600); err != nil {
		return "", false, err
	}
	if _, err := sys.Run(ctx, "chown", "-R", CaisUser+":", dir); err != nil {
		return "", false, err
	}
	return fmt.Sprintf("%d chave(s) a mais: ssh cais@vps abre a TUI", len(keys)-len(current)), true, nil
}

func (h *hardener) firewall(ctx context.Context) (string, bool, error) {
	s := readUFW(ctx, h.sys)
	if s.Unavailable != "" {
		return "", false, errors.New("ufw: " + s.Unavailable)
	}
	missing := false
	for _, p := range firewallPorts {
		ok := slices.Contains(s.Allowed, p) || (p == "22/tcp" && (slices.Contains(s.Allowed, "OpenSSH") || slices.Contains(s.Allowed, "22")))
		missing = missing || !ok
	}
	if s.Active && s.DenyIn && !missing {
		return "", false, nil
	}
	// o 22 entra antes de ligar: ligar sem ele derrubaria este ssh
	cmds := [][]string{{"default", "deny", "incoming"}, {"default", "allow", "outgoing"}}
	for _, p := range firewallPorts {
		cmds = append(cmds, []string{"allow", p})
	}
	cmds = append(cmds, []string{"--force", "enable"})
	for _, c := range cmds {
		if _, err := h.sys.Run(ctx, "ufw", c...); err != nil {
			return "", false, err
		}
	}
	detail := ""
	if extra := s.extraRules(); len(extra) > 0 {
		detail = "regras antigas mantidas: " + strings.Join(extra, ", ")
	}
	return detail, true, nil
}

func (h *hardener) fail2ban(ctx context.Context) (string, bool, error) {
	changed, err := h.writeIfChanged(fail2banJail, fail2banConf)
	if err != nil {
		return "", false, err
	}
	active, _ := h.sys.Run(ctx, "systemctl", "is-active", "fail2ban")
	if !changed && active == "active" {
		return "", false, nil
	}
	if _, err := h.sys.Run(ctx, "systemctl", "enable", "fail2ban"); err != nil {
		return "", false, err
	}
	if _, err := h.sys.Run(ctx, "systemctl", "restart", "fail2ban"); err != nil {
		return "", false, err
	}
	return "5 erros em 10 min bloqueiam o IP por 1 hora", true, nil
}

func (h *hardener) ssh(ctx context.Context) (string, bool, error) {
	cur, _ := h.sys.ReadFile(SSHDropin)
	eff, err := sshdConfig(ctx, h.sys, "")
	if err != nil {
		return "", false, err
	}
	if string(cur) == sshdHarden && eff["permitrootlogin"] == "no" && eff["passwordauthentication"] == "no" {
		return "", false, nil
	}

	undo := func() {
		if cur != nil {
			h.sys.WriteFile(SSHDropin, cur, 0o644)
		} else {
			h.sys.Remove(SSHDropin)
		}
	}
	if err := h.sys.WriteFile(SSHDropin, []byte(sshdHarden), 0o644); err != nil {
		return "", false, err
	}
	if _, err := h.sys.Run(ctx, "sshd", "-t"); err != nil {
		undo()
		return "", false, errors.New("o sshd recusou a config (nada mudou): " + firstLine(err))
	}
	// a config efetiva: outro arquivo lido antes (ou um sshd_config sem o
	// Include do sshd_config.d) pode ganhar do nosso
	eff, err = sshdConfig(ctx, h.sys, "")
	if err == nil && (eff["permitrootlogin"] != "no" || eff["passwordauthentication"] != "no") {
		err = errors.New("outra config do sshd ganha da do cais (PermitRootLogin " + eff["permitrootlogin"] +
			", PasswordAuthentication " + eff["passwordauthentication"] + "): confira /etc/ssh/sshd_config e o Include do sshd_config.d")
	}
	if err != nil {
		undo()
		return "", false, err
	}
	admin, err := sshdConfig(ctx, h.sys, "user="+h.opts.User+",host=localhost,addr=127.0.0.1")
	if err == nil && admin["pubkeyauthentication"] != "yes" {
		err = errors.New("o " + h.opts.User + " nao entraria por chave (PubkeyAuthentication " + admin["pubkeyauthentication"] + ")")
	}
	if err != nil {
		undo()
		return "", false, err
	}

	// rede de seguranca: sem o confirm de um login novo, o arquivo sai e o
	// SSH volta a aceitar o root e a senha
	h.sys.Run(ctx, "systemctl", "stop", RollbackUnit+".timer")
	h.sys.Run(ctx, "systemctl", "reset-failed", RollbackUnit+".service")
	if _, err := h.sys.Run(ctx, "systemd-run", "--unit="+RollbackUnit, "--on-active="+RollbackAfter,
		"--description=cais: desfaz o SSH do harden (sem o cais host confirm)",
		"/bin/sh", "-c", "rm -f "+SSHDropin+" && "+reloadSSHShell); err != nil {
		undo()
		return "", false, errors.New("nao deu pra agendar a volta automatica (nada mudou): " + firstLine(err))
	}
	if err := reloadSSH(ctx, h.sys); err != nil {
		undo()
		h.sys.Run(ctx, "systemctl", "stop", RollbackUnit+".timer")
		return "", false, err
	}
	h.result.PendingConfirm = true
	return "volta sozinho em 5 minutos sem o cais host confirm", true, nil
}

// O servico e ssh no Ubuntu/Debian e sshd em outros. try-reload: com o
// ssh.socket (Ubuntu 24.04) o servico pode nem estar no ar, e cada conexao
// nova le a config de novo.
const reloadSSHShell = "(systemctl try-reload-or-restart ssh.service || systemctl try-reload-or-restart sshd.service)"

func reloadSSH(ctx context.Context, sys System) error {
	if _, err := sys.Run(ctx, "systemctl", "try-reload-or-restart", "ssh.service"); err == nil {
		return nil
	}
	if _, err := sys.Run(ctx, "systemctl", "try-reload-or-restart", "sshd.service"); err != nil {
		return errors.New("recarregar o ssh: " + firstLine(err))
	}
	return nil
}

// Confirm cancela a volta automatica do SSH. Diz se havia uma pendente.
func Confirm(ctx context.Context, sys System) (bool, error) {
	if sys.Geteuid() != 0 {
		return false, errors.New("rode como root: sudo cais host confirm")
	}
	if out, _ := sys.Run(ctx, "systemctl", "is-active", RollbackUnit+".timer"); out != "active" {
		return false, nil
	}
	if _, err := sys.Run(ctx, "systemctl", "stop", RollbackUnit+".timer"); err != nil {
		return false, err
	}
	return true, nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// fixers corrigem um item sozinho (sem o admin nem o SSH): o pacote que
// falta e o passo do harden.
var fixers = map[string]struct {
	packages []string
	step     func(*hardener) stepFunc
}{
	"firewall":    {[]string{"ufw"}, func(h *hardener) stepFunc { return h.firewall }},
	"fail2ban":    {[]string{"fail2ban", "python3-systemd"}, func(h *hardener) stepFunc { return h.fail2ban }},
	"updates":     {[]string{"unattended-upgrades"}, func(h *hardener) stepFunc { return h.updates }},
	"docker-logs": {nil, func(h *hardener) stepFunc { return h.dockerLogs }},
	"ssh-cais":    {nil, func(h *hardener) stepFunc { return h.caisKeys }},
}

// Fix corrige um item da checagem (firewall, fail2ban, updates,
// docker-logs, ssh-cais), instalando o pacote se faltar. Devolve o detalhe do que fez.
func Fix(ctx context.Context, sys System, id string) (string, error) {
	f, ok := fixers[id]
	if !ok {
		return "", errors.New("o item " + id + " nao tem correcao avulsa: use o Preparar VPS (cais host harden)")
	}
	if sys.Geteuid() != 0 {
		return "", errors.New("precisa de root")
	}
	r, err := readOSRelease(sys)
	if err != nil || !r.Supported() {
		return "", errors.New("so corrige no Ubuntu e no Debian (" + cmpOr(r.String(), "sistema desconhecido") + ")")
	}
	h := &hardener{sys: sys}
	var missing []string
	for _, p := range f.packages {
		if out, err := sys.Run(ctx, "dpkg-query", "-W", "-f=${Status}", p); err != nil || !strings.Contains(out, "install ok installed") {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		if err := h.apt(ctx, "update"); err != nil {
			return "", err
		}
		if err := h.apt(ctx, append([]string{"install"}, missing...)...); err != nil {
			return "", err
		}
	}
	detail, _, err := f.step(h)(ctx)
	return detail, err
}

// SSHHardened diz se a config do SSH do harden esta no lugar (depois da
// volta automatica, nao esta mais).
func SSHHardened(sys System) bool { return sys.Exists(SSHDropin) }
