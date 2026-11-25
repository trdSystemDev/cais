package host

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

// Tailscale e a VPN da VPS: com ela, o SSH (e tudo que nao e site) so entra
// pela tailnet, e da internet ficam abertas so a 80 e a 443.
type Tailscale struct {
	Installed bool `json:"installed"`
	// State e o BackendState do tailscaled: Running (conectado),
	// NeedsLogin, Stopped, NoState...
	State string `json:"state,omitempty"`
	// AuthURL e o link de login quando ele espera a autorizacao.
	AuthURL string `json:"auth_url,omitempty"`
	// Name e o nome da VPS no MagicDNS (srv.tailnet.ts.net).
	Name    string   `json:"name,omitempty"`
	IPs     []string `json:"ips,omitempty"`
	Tailnet string   `json:"tailnet,omitempty"`
	// KeyExpiry e quando a chave da VPS na tailnet vence (zero: a expiracao
	// esta desligada no painel, o certo pra servidor).
	KeyExpiry time.Time `json:"key_expiry,omitzero"`
	Error     string    `json:"error,omitempty"`
}

// Connected diz se a VPS esta na tailnet.
func (t Tailscale) Connected() bool { return t.State == "Running" }

// IP4 e o IPv4 da VPS na tailnet (vazio sem conexao).
func (t Tailscale) IP4() string {
	for _, ip := range t.IPs {
		if !strings.Contains(ip, ":") {
			return ip
		}
	}
	return ""
}

// TailscaleUpUnit e a unidade que roda o tailscale up: ele so termina
// quando o login e autorizado no navegador.
const TailscaleUpUnit = "cais-tailscale-up"

// tailscaleIface e a interface do Tailscale: as regras do ufw so dela valem
// so pra tailnet.
const tailscaleIface = "tailscale0"

// TailscaleStatus le o tailscale status --json.
func TailscaleStatus(ctx context.Context, sys System) Tailscale {
	var t Tailscale
	if _, err := sys.Run(ctx, "tailscale", "version"); err != nil {
		return t
	}
	t.Installed = true
	// sai com erro fora da tailnet, mas o JSON vem igual
	out, err := sys.Run(ctx, "tailscale", "status", "--json")
	var st struct {
		BackendState string
		AuthURL      string
		Self         *struct {
			DNSName      string
			TailscaleIPs []string
			KeyExpiry    *time.Time
		}
		CurrentTailnet *struct{ Name string }
	}
	if jerr := json.Unmarshal([]byte(out), &st); jerr != nil {
		if err != nil {
			t.Error = firstLine(err)
		} else {
			t.Error = "status do tailscale ilegivel: " + jerr.Error()
		}
		return t
	}
	t.State, t.AuthURL = st.BackendState, st.AuthURL
	if st.Self != nil && t.Connected() {
		t.Name, t.IPs = strings.TrimSuffix(st.Self.DNSName, "."), st.Self.TailscaleIPs
		if st.Self.KeyExpiry != nil {
			t.KeyExpiry = *st.Self.KeyExpiry
		}
	}
	if st.CurrentTailnet != nil {
		t.Tailnet = st.CurrentTailnet.Name
	}
	return t
}

// InstallTailscale instala o Tailscale pelo repositorio oficial (Ubuntu e
// Debian) e liga o tailscaled. Ja instalado, nao faz nada.
func InstallTailscale(ctx context.Context, sys System) error {
	if _, err := sys.Run(ctx, "tailscale", "version"); err == nil {
		return nil
	}
	r, err := readOSRelease(sys)
	if err != nil || !r.Supported() || r.Codename == "" {
		return errors.New("o cais so instala o Tailscale no Ubuntu e no Debian (" + cmpOr(r.String(), "sistema desconhecido") +
			"): instale pelo site (tailscale.com/download) e conecte de novo")
	}
	base := "https://pkgs.tailscale.com/stable/" + r.ID + "/" + r.Codename
	keyring := "/usr/share/keyrings/tailscale-archive-keyring.gpg"
	if _, err := sys.Run(ctx, "curl", "-fsSL", base+".noarmor.gpg", "-o", keyring); err != nil {
		return errors.New("baixando a chave do repositorio do Tailscale: " + firstLine(err))
	}
	list, err := sys.Run(ctx, "curl", "-fsSL", base+".tailscale-keyring.list")
	if err != nil {
		return errors.New("baixando o repositorio do Tailscale (" + r.ID + " " + r.Codename + "): " + firstLine(err))
	}
	if !strings.Contains(list, "https://pkgs.tailscale.com/") || !strings.Contains(list, "signed-by="+keyring) {
		return errors.New("lista do repositorio do Tailscale inesperada: instale pelo site (tailscale.com/download)")
	}
	if err := sys.WriteFile("/etc/apt/sources.list.d/tailscale.list", []byte(strings.TrimSpace(list)+"\n"), 0o644); err != nil {
		return err
	}
	h := &hardener{sys: sys}
	if err := h.apt(ctx, "update"); err != nil {
		return err
	}
	if err := h.apt(ctx, "install", "tailscale"); err != nil {
		return err
	}
	if _, err := sys.Run(ctx, "systemctl", "enable", "--now", "tailscaled"); err != nil {
		return errors.New("ligando o tailscaled: " + firstLine(err))
	}
	return nil
}

// loginWait e quanto o ConnectTailscale espera o link de login aparecer;
// loginPoll, de quanto em quanto tempo pergunta (trocados nos testes).
var (
	loginWait = 30 * time.Second
	loginPoll = 500 * time.Millisecond
)

// ConnectTailscale instala (se faltar) e poe a VPS na tailnet. Ja
// conectada, devolve o status. Senao, roda o tailscale up numa unidade
// propria (ele so termina quando o login e autorizado) e devolve o status
// com o AuthURL, o link pra autorizar no navegador. Sem o DNS da tailnet
// (--accept-dns=false): o DNS da VPS e dos containers fica como esta.
func ConnectTailscale(ctx context.Context, sys System) (Tailscale, error) {
	if sys.Geteuid() != 0 {
		return Tailscale{}, errors.New("precisa de root")
	}
	if err := InstallTailscale(ctx, sys); err != nil {
		return Tailscale{}, err
	}
	if t := TailscaleStatus(ctx, sys); t.Connected() {
		return t, nil
	}
	sys.Run(ctx, "systemctl", "stop", TailscaleUpUnit+".service")
	sys.Run(ctx, "systemctl", "reset-failed", TailscaleUpUnit+".service")
	if _, err := sys.Run(ctx, "systemd-run", "--unit="+TailscaleUpUnit, "--collect",
		"--description=cais: tailscale up (espera o login no navegador)",
		"tailscale", "up", "--accept-dns=false"); err != nil {
		return Tailscale{}, errors.New("nao deu pra rodar o tailscale up: " + firstLine(err))
	}
	deadline := time.Now().Add(loginWait)
	for {
		t := TailscaleStatus(ctx, sys)
		if t.Connected() || t.AuthURL != "" {
			return t, nil
		}
		if time.Now().After(deadline) {
			log, _ := sys.Run(ctx, "journalctl", "-u", TailscaleUpUnit, "-n", "5", "--no-pager", "-o", "cat")
			return t, errors.New("o Tailscale nao mostrou o link de login (estado " + cmpOr(t.State, "desconhecido") + "): " +
				cmpOr(lastLine(log), "veja journalctl -u "+TailscaleUpUnit))
		}
		select {
		case <-ctx.Done():
			return t, ctx.Err()
		case <-time.After(loginPoll):
		}
	}
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// publicSSHRules sao as regras do ufw que abrem o SSH pra internet.
func publicSSHRules(s ufwStatus) []string {
	var out []string
	for _, r := range s.Allowed {
		if r == "22/tcp" || r == "22" || r == "OpenSSH" {
			out = append(out, r)
		}
	}
	return out
}

// tailnetSSH diz se o ufw libera o SSH pela interface do Tailscale.
func tailnetSSH(s ufwStatus) bool {
	return slices.Contains(s.Allowed, "22/tcp on "+tailscaleIface) || slices.Contains(s.Allowed, "22 on "+tailscaleIface)
}

// SSHTailnetOnly diz se o SSH so entra pela tailnet: liberado no
// tailscale0 e sem regra que abra o 22 pra internet.
func SSHTailnetOnly(ctx context.Context, sys System) bool {
	s := readUFW(ctx, sys)
	return s.Active && s.DenyIn && tailnetSSH(s) && len(publicSSHRules(s)) == 0
}

// CloseSSH fecha o SSH pra internet: ele passa a entrar so pela tailnet, e
// de fora ficam so a 80 e a 443. A volta e agendada antes de fechar: sem o
// cais host confirm (ou o Confirmar da TUI) de uma sessao aberta pela
// tailnet em RollbackAfter, o 22 abre de novo sozinho. As sessoes ja
// abertas continuam (o ufw deixa passar as conexoes estabelecidas).
func CloseSSH(ctx context.Context, sys System) (Result, error) {
	if sys.Geteuid() != 0 {
		return Result{}, errors.New("precisa de root")
	}
	if t := TailscaleStatus(ctx, sys); !t.Connected() {
		return Result{}, errors.New("conecte o Tailscale antes: sem ele, fechar o SSH trancaria voce fora")
	}
	s := readUFW(ctx, sys)
	switch {
	case s.Unavailable != "":
		return Result{}, errors.New("ufw: " + s.Unavailable)
	case !s.Active || !s.DenyIn:
		return Result{}, errors.New("o firewall (ufw) precisa estar ligado e bloqueando a entrada: corrija o Firewall antes")
	}
	public := publicSSHRules(s)
	if tailnetSSH(s) && len(public) == 0 {
		return Result{}, nil
	}
	if _, err := sys.Run(ctx, "ufw", "allow", "in", "on", tailscaleIface, "to", "any", "port", "22", "proto", "tcp"); err != nil {
		return Result{}, errors.New("liberando o SSH na tailnet: " + firstLine(err))
	}
	// a volta antes de fechar: se algo der errado daqui pra frente, o 22
	// abre de novo sozinho
	sys.Run(ctx, "systemctl", "stop", RollbackUnit+".timer")
	sys.Run(ctx, "systemctl", "reset-failed", RollbackUnit+".service")
	if _, err := sys.Run(ctx, "systemd-run", "--unit="+RollbackUnit, "--on-active="+RollbackAfter,
		"--description=cais: abre o SSH pra internet de novo (sem o cais host confirm)",
		"/usr/sbin/ufw", "allow", "22/tcp"); err != nil {
		return Result{}, errors.New("nao deu pra agendar a volta automatica (nada fechou): " + firstLine(err))
	}
	for _, r := range public {
		if _, err := sys.Run(ctx, "ufw", "delete", "allow", r); err != nil {
			// reabre na hora o que ja fechou: nao fica pela metade
			sys.Run(ctx, "ufw", "allow", "22/tcp")
			sys.Run(ctx, "systemctl", "stop", RollbackUnit+".timer")
			return Result{}, errors.New("fechando o SSH (" + r + "): " + firstLine(err) + "; o 22 continua aberto")
		}
	}
	return Result{PendingConfirm: true}, nil
}
