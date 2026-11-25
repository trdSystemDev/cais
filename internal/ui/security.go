package ui

import (
	"context"
	"fmt"
	"image/color"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/host"
)

// A sub-aba Seguranca do Setup: a checagem da VPS (host.Check, feita pelo
// daemon) com o Corrigir de cada item que da pra corrigir sozinho, e o
// Preparar VPS (o cais host harden pela TUI). Fica fora do MCP: a IA nao
// mexe no SSH nem no firewall.

// itemHints sao os itens que se corrigem numa secao abaixo, e onde.
var itemHints = map[string]string{
	"ssh-root": "corrige no Preparar VPS, abaixo", "ssh-password": "corrige no Preparar VPS, abaixo",
	"tailscale": "conecta no Tailscale, abaixo", "ssh-tailnet": "fecha no Tailscale, abaixo",
}

// tsLinkShown e o link de login do Tailscale a mostrar: o da ultima
// conexao pedida ou o que o tailscaled espera (vazio ja conectado).
func (p *setupPage) tsLinkShown() string {
	if p.env.Tailscale.Connected() {
		return ""
	}
	if p.env.Tailscale.AuthURL != "" {
		return p.env.Tailscale.AuthURL
	}
	return p.tsLink
}

// sshPublic diz se o SSH ainda abre pra internet com o Tailscale conectado.
func (p *setupPage) sshPublic() bool {
	for _, it := range p.env.Security {
		if it.ID == "ssh-tailnet" {
			return !it.OK
		}
	}
	return false
}

// canConfirm diz se esta sessao pode confirmar o SSH: so uma aberta depois
// da mudanca prova que o login por chave funciona.
func (p *setupPage) canConfirm() bool {
	return p.hard.Pending() && p.started.After(p.hard.ReloadedAt)
}

// securityItems sao os itens com teclado da Seguranca, na ordem do Tab.
func (p *setupPage) securityItems() []*item {
	var items []*item
	add := func(id, label string, kind int, input *textinput.Model) {
		items = append(items, &item{id: id, label: label, kind: kind, input: input, section: secSeguranca})
	}
	for _, it := range p.env.Security {
		if it.Fixable && p.hard.Fixing != it.ID && !p.hard.Running {
			add("fix:"+it.ID, "Corrigir", kindButton, nil)
		}
	}
	// o Tailscale: conectar (e o link), depois fechar o SSH publico
	ts := p.env.Tailscale
	busy := p.hard.Running || p.hard.Fixing != "" || p.hard.Pending()
	switch {
	case busy:
	case !ts.Connected():
		add("tsConnect", "Conectar ao Tailscale", kindButton, nil)
		if p.tsLinkShown() != "" {
			add("tsCopy", "Copiar link", kindButton, nil)
			add("tsCheck", "Ja autorizei", kindButton, nil)
		}
	case p.sshPublic():
		add("sshClose", "Fechar o SSH publico", kindButton, nil)
	}
	switch {
	case p.hard.Running:
	case p.canConfirm():
		add("hostConfirm", "Confirmar", kindButton, nil)
	case p.hard.Pending():
	case p.hardenForm:
		add("hUser", "Admin", kindInput, p.hUser)
		add("hPass", "Senha do sudo", kindInput, p.hPass)
		add("hPass2", "De novo", kindInput, p.hPass2)
		add("harden-apply", "Aplicar", kindButton, nil)
		add("harden-cancel", "Cancelar", kindButton, nil)
	default:
		add("harden-open", "Preparar VPS", kindButton, nil)
	}
	add("hostCheck", "Checar de novo", kindButton, nil)
	return items
}

// setState aplica o estado na Seguranca: refaz os itens se mudou o que eles
// mostram e liga a contagem regressiva do SSH pendente.
func (p *setupPage) setState(s core.State) tea.Cmd {
	p.env, p.hard = s.Environment, s.Hardening
	if sig := p.securitySig(); sig != p.secSig {
		p.secSig = sig
		p.rebuild()
		if p.inForm && p.sec == secSeguranca && !p.visible(p.items[p.focus]) {
			p.enter(false)
		}
	}
	if p.hard.Pending() && !p.ticking {
		p.ticking = true
		return hostTickCmd()
	}
	return nil
}

func (p *setupPage) securitySig() string {
	var b strings.Builder
	for _, it := range p.env.Security {
		if it.Fixable {
			b.WriteString(it.ID + ",")
		}
	}
	fmt.Fprintf(&b, "|%v|%v|%v|%v|%s", p.hard.Running, p.hard.Pending(), p.canConfirm(), p.hardenForm, p.hard.Fixing)
	fmt.Fprintf(&b, "|%v|%v|%v", p.env.Tailscale.Connected(), p.tsLinkShown() != "", p.sshPublic())
	return b.String()
}

func hostTickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return hostTick{} })
}

func (p *setupPage) closeHardenForm() {
	p.hardenForm = false
	p.hPass.SetValue("")
	p.hPass2.SetValue("")
	p.rebuild()
}

// hardenApply confere o formulario e manda o Preparar VPS pro daemon. A
// senha sai dos campos na hora.
func (p *setupPage) hardenApply() tea.Cmd {
	user := strings.TrimSpace(p.hUser.Value())
	pw, pw2 := p.hPass.Value(), p.hPass2.Value()
	switch {
	case user == "":
		p.setStatus("danger", "Falta o nome do admin (ex: deploy).")
		return p.focusAt(p.index("hUser"))
	case pw != pw2:
		p.setStatus("danger", "As senhas nao conferem.")
		p.hPass2.SetValue("")
		return p.focusAt(p.index("hPass2"))
	case pw != "" && len(pw) < 8:
		p.setStatus("danger", "Senha curta: use pelo menos 8 caracteres.")
		return p.focusAt(p.index("hPass"))
	}
	p.closeHardenForm()
	p.setStatus("warn", "Preparando a VPS (a atualizacao do sistema pode levar alguns minutos)...")
	return func() tea.Msg {
		return hardenMsg{p.api.HardenHost(context.Background(), core.HardenRequest{User: user, Password: pw})}
	}
}

var stepMarks = map[string]string{
	host.StepRunning: "..", host.StepChanged: "ok", host.StepOK: "--", host.StepFailed: "!!",
}

// viewSecurity desenha a Seguranca: os itens da checagem e o Preparar VPS.
func (p *setupPage) viewSecurity(w int) string {
	t, env := p.th, p.env
	muted := t.fg(t.muted)
	wrap := lipgloss.NewStyle().Width(w)
	focusID := ""
	if p.inForm {
		focusID = p.items[p.focus].id
	}
	button := func(id, label string, k btnKind) string {
		return focusMarked(focusID == id, p.zones.Mark(id, t.button(label, k, focusID == id)))
	}
	var rows []string

	// a checagem
	switch {
	case env.Checking && len(env.Security) == 0:
		rows = append(rows, muted.Render("Checando..."))
	case len(env.Security) == 0:
		rows = append(rows, muted.Width(w).Render("Sem checagem: fora do Linux nao ha o que conferir."))
	default:
		bad := len(host.Failed(env.Security))
		head := t.fg(t.ok).Bold(true).Render("Tudo certo.")
		if bad > 0 {
			head = t.fg(t.danger).Bold(true).Render(fmt.Sprintf("%d de %d itens pra corrigir.", bad, len(env.Security)))
		}
		if !env.CheckedAt.IsZero() {
			head += muted.Render("  checado as " + env.CheckedAt.Local().Format("15:04"))
		}
		rows = append(rows, head, "")
		for _, it := range env.Security {
			mark := t.fg(t.ok).Render("✓")
			if !it.OK {
				mark = t.fg(t.danger).Render("✗")
			}
			line := mark + " " + t.bold().Render(it.Title) + muted.Render("  "+it.Detail)
			switch {
			case p.hard.Fixing == it.ID:
				line += "  " + t.fg(t.warn).Render("corrigindo...")
			case it.Fixable && !p.hard.Running:
				line += "  " + focusMarked(focusID == "fix:"+it.ID, p.zones.Mark("fix:"+it.ID, t.chip("Corrigir", btnNormal, focusID == "fix:"+it.ID)))
			}
			rows = append(rows, line)
			switch {
			case it.OK:
			case itemHints[it.ID] != "":
				rows = append(rows, wrap.PaddingLeft(2).Foreground(t.warn).Render(itemHints[it.ID]))
			case !it.Fixable && it.Fix != "":
				rows = append(rows, wrap.PaddingLeft(2).Foreground(t.warn).Render(it.Fix))
			}
		}
	}

	rows = append(rows, p.viewTailscale(w, button)...)

	// o Preparar VPS
	rows = append(rows, "", t.rule(w), t.bold().Render("Preparar VPS"))
	h := p.hard
	tailnetPending := h.Pending() && h.Kind == core.KindSSHTailnet
	if len(h.Steps) > 0 && (h.Running || h.Pending() || h.Error != "") {
		for _, st := range h.Steps {
			line := fmt.Sprintf("[%s] %s", stepMarks[st.Status], st.Desc)
			if st.Detail != "" && st.Status != host.StepFailed {
				line += muted.Render(" -- " + st.Detail)
			}
			c := t.text
			switch st.Status {
			case host.StepFailed:
				c = t.danger
			case host.StepRunning:
				c = t.warn
			}
			rows = append(rows, wrap.Foreground(c).Render(line))
		}
		rows = append(rows, "")
	}
	switch {
	case h.Running:
		rows = append(rows, t.fg(t.warn).Render("Aplicando... pode fechar a TUI: o daemon termina sozinho."))
	case tailnetPending:
		rows = append(rows, muted.Width(w).Render("Esperando a confirmacao do SSH pela tailnet (Tailscale, acima)."))
	case h.Pending():
		left := max(0, time.Until(h.RollbackAt).Round(time.Second))
		rows = append(rows,
			t.fg(t.warn).Bold(true).Render(fmt.Sprintf("O SSH mudou: confirme em %d:%02d.", int(left.Minutes()), int(left.Seconds())%60)),
			wrap.Render("Sem fechar esta janela, abra OUTRA no seu computador e entre de novo: ssh cais@<ip-da-vps> (ou ssh "+
				h.User+"@<ip-da-vps>). Se entrar, confirme por ela. Sem a confirmacao, o SSH volta sozinho a aceitar o root "+
				"e a senha, e voce nao fica trancado fora."))
		if p.canConfirm() {
			rows = append(rows, "", "  "+button("hostConfirm", "Confirmar", btnNormal))
		} else {
			rows = append(rows, muted.Width(w).Render("Esta janela foi aberta antes da mudanca: o Confirmar aparece na janela nova."))
		}
	default:
		switch {
		case h.Error != "":
			rows = append(rows, wrap.Foreground(t.danger).Render("Parou: "+h.Error), "")
		case h.RolledBack:
			rows = append(rows, wrap.Foreground(t.danger).Render(
				"Ninguem confirmou e o SSH voltou ao que era (root e senha entram de novo). Confira a chave e prepare de novo."), "")
		case h.Confirmed:
			rows = append(rows, t.fg(t.ok).Render("SSH confirmado: entra so com chave, sem o root."), "")
		}
		rows = append(rows, muted.Width(w).Render(
			"Pra VPS nova (Ubuntu ou Debian): atualiza o sistema; liga as atualizacoes de seguranca; limita os logs dos "+
				"containers; cria o admin com sudo e as chaves SSH do root; firewall so com 22, 80 e 443; fail2ban; e o SSH "+
				"sem o root e sem senha. Rodar de novo so completa o que falta."))
		if p.hardenForm {
			rows = append(rows, "")
			for _, f := range []struct {
				id, label string
				in        *textinput.Model
			}{{"hUser", "Admin", p.hUser}, {"hPass", "Senha do sudo", p.hPass}, {"hPass2", "De novo", p.hPass2}} {
				marker, label := "  ", t.fg(t.text).Width(16).Render(f.label)
				if focusID == f.id {
					marker = t.fg(t.accent).Bold(true).Render("› ")
					label = t.fg(t.accent).Bold(true).Width(16).Render(f.label)
				}
				rows = append(rows, focusMarked(focusID == f.id, p.zones.Mark(f.id, marker+label+t.input(f.in, 30))))
			}
			rows = append(rows, muted.Width(w).Render(
				"A senha e so pro sudo do admin (o SSH continua so por chave); deixe vazia se o admin ja existe com senha."),
				"", "  "+button("harden-apply", "Aplicar", btnNormal)+" "+button("harden-cancel", "Cancelar", btnInfo))
		} else {
			rows = append(rows, "", "  "+button("harden-open", "Preparar VPS", btnNormal))
		}
	}
	rows = append(rows, "", "  "+button("hostCheck", "Checar de novo", btnInfo))
	return "\n" + strings.Join(rows, "\n")
}

// viewTailscale e a secao do Tailscale: conectar pelo link (como o GitHub)
// e, conectado, fechar o SSH pra internet, com a contagem e o Confirmar.
func (p *setupPage) viewTailscale(w int, button func(id, label string, k btnKind) string) []string {
	t, ts, h := p.th, p.env.Tailscale, p.hard
	muted := t.fg(t.muted)
	wrap := lipgloss.NewStyle().Width(w)
	rows := []string{"", t.rule(w), t.bold().Render("Tailscale") + muted.Render("  VPN pra administrar a VPS: o SSH so pela tailnet, da internet so 80 e 443")}
	msg := func() {
		if p.tsMsg != "" {
			c := map[string]color.Color{"ok": t.ok, "warn": t.warn, "danger": t.danger}[p.tsMsgC]
			rows = append(rows, wrap.Foreground(c).Render(p.tsMsg))
		}
	}
	switch {
	case p.hard.Fixing == "tailscale":
		rows = append(rows, t.fg(t.warn).Render("Instalando e conectando o Tailscale (a instalacao pode levar um minuto)..."))
		return rows
	case !ts.Connected():
		rows = append(rows, wrap.Render("Conectar instala o Tailscale (repositorio oficial) e mostra um link: abra no navegador, "+
			"entre na sua conta e autorize a VPS. Ela entra na sua tailnet com um IP 100.x."), "")
		if link := p.tsLinkShown(); link != "" {
			rows = append(rows, "Abra no navegador (ctrl+clique) e autorize:",
				t.fg(t.accent).Underline(true).Hyperlink(link).Render(link), "",
				"  "+button("tsCopy", "Copiar link", btnNormal)+" "+button("tsCheck", "Ja autorizei", btnInfo))
		} else {
			label := "Conectar ao Tailscale"
			if !ts.Installed {
				label = "Instalar e conectar"
			}
			rows = append(rows, "  "+button("tsConnect", label, btnNormal))
		}
		msg()
		return rows
	}
	rows = append(rows, t.fg(t.ok).Render("✓ conectado")+muted.Render(fmt.Sprintf("  %s  %s  tailnet %s", cmpOrUI(ts.Name, "-"), ts.IP4(), cmpOrUI(ts.Tailnet, "-"))))
	if !ts.KeyExpiry.IsZero() {
		rows = append(rows, wrap.Foreground(t.warn).Render("A chave da VPS vence em "+ts.KeyExpiry.Local().Format("02/01/2006")+
			": vencida, ela sai da tailnet. No painel do Tailscale (login.tailscale.com/admin/machines), menu ... da VPS: Disable key expiry."))
	}
	switch {
	case h.Pending() && h.Kind == core.KindSSHTailnet:
		left := max(0, time.Until(h.RollbackAt).Round(time.Second))
		rows = append(rows, "",
			t.fg(t.warn).Bold(true).Render(fmt.Sprintf("O SSH fechou pra internet: confirme em %d:%02d.", int(left.Minutes()), int(left.Seconds())%60)),
			wrap.Render("Sem fechar esta janela, abra OUTRA no seu computador (ligado no Tailscale) e entre pela tailnet: ssh cais@"+
				cmpOrUI(ts.Name, ts.IP4())+". Se entrar, confirme por ela. Sem a confirmacao, o 22 abre de novo pra internet sozinho, "+
				"e voce nao fica trancado fora."))
		if p.canConfirm() {
			rows = append(rows, "", "  "+button("hostConfirm", "Confirmar", btnNormal))
		} else {
			rows = append(rows, muted.Width(w).Render("Esta janela foi aberta antes de fechar: o Confirmar aparece na janela nova, aberta pela tailnet."))
		}
	case p.sshPublic():
		text := "O SSH (22) ainda abre pra internet. Fechar deixa ele so pela tailnet: daqui pra frente, ssh cais@" +
			cmpOrUI(ts.Name, ts.IP4()) + " (com o Tailscale ligado no seu computador). Se ninguem confirmar pela tailnet em 5 minutos, " +
			"o 22 abre de novo sozinho. O console da Hostinger continua de emergencia."
		rows = append(rows, "", wrap.Render(text))
		label := "Fechar o SSH publico"
		if p.sshCloseArm {
			label = "Confirmar: fechar o 22"
		}
		rows = append(rows, "", "  "+button("sshClose", label, btnWarn))
	case h.Confirmed && h.Kind == core.KindSSHTailnet:
		rows = append(rows, t.fg(t.ok).Render("SSH confirmado: so pela tailnet; da internet so 80 e 443."))
	case h.RolledBack && h.Kind == core.KindSSHTailnet:
		rows = append(rows, wrap.Foreground(t.danger).Render("Ninguem confirmou pela tailnet e o 22 abriu de novo pra internet."))
	default:
		rows = append(rows, muted.Render("O SSH so entra pela tailnet; da internet so 80 e 443."))
	}
	msg()
	return rows
}

// tsConnect pede ao daemon pra instalar (se faltar) e conectar o Tailscale.
func (p *setupPage) tsConnect() tea.Cmd {
	p.tsMsgC, p.tsMsg = "warn", "Conectando..."
	return func() tea.Msg {
		ts, err := p.api.ConnectTailscale(context.Background())
		return tsConnectMsg{ts, err}
	}
}

// sshClose fecha o SSH publico: o primeiro aperto so pergunta.
func (p *setupPage) sshClose() tea.Cmd {
	if !p.sshCloseArm {
		p.sshCloseArm = true
		p.tsMsgC, p.tsMsg = "warn", "Tem certeza? Confira antes que o ssh pela tailnet funciona no seu computador. Aperte de novo pra fechar."
		return nil
	}
	p.sshCloseArm = false
	p.tsMsgC, p.tsMsg = "warn", "Fechando o SSH pra internet..."
	return func() tea.Msg { return sshCloseMsg{p.api.CloseSSHPublic(context.Background())} }
}

func cmpOrUI(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
