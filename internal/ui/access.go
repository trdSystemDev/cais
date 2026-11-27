package ui

import (
	"context"
	"fmt"
	"image/color"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/host"
)

// A sub-aba Acessos do Setup: quem entra na VPS pelo SSH (as chaves e os
// admins), dar acesso a uma chave (so a TUI ou como admin) e tirar. Fora do
// MCP: a IA nao da nem tira acesso.

type (
	accessLoadedMsg struct {
		a   host.Access
		err error
	}
	accessDoneMsg struct {
		detail string
		added  bool // foi um Dar acesso
		err    error
	}
)

// accessState e o que a sub-aba guarda.
type accessState struct {
	a      host.Access
	loaded bool
	err    string
	msg    string
	msgC   string
	// arm: o Remover apertado uma vez (o segundo tira)
	arm                    string
	key, user, pass, pass2 *textinput.Model
}

func (p *setupPage) newAccessInputs(newInput func(string, bool) *textinput.Model) {
	p.acc.key = newInput("ssh-ed25519 AAAA... nome@computador", false)
	p.acc.key.SetWidth(60)
	p.acc.user = newInput("ex: maria", false)
	p.acc.pass = newInput("pelo menos 8 caracteres", true)
	p.acc.pass2 = newInput("a mesma senha", true)
}

// loadAccess pede ao daemon quem entra.
func (p *setupPage) loadAccess() tea.Cmd {
	api := p.api
	return func() tea.Msg {
		a, err := api.HostAccess(context.Background())
		return accessLoadedMsg{a, err}
	}
}

// accessItems sao os itens com teclado dos Acessos, na ordem do Tab.
func (p *setupPage) accessItems() []*item {
	var items []*item
	add := func(id, label string, kind int, input *textinput.Model) {
		items = append(items, &item{id: id, label: label, kind: kind, input: input, section: secAcessos})
	}
	for _, k := range p.acc.a.Keys {
		if k.Key != "" {
			add("akuse:"+k.Fingerprint, "Usar", kindButton, nil)
		}
		add("akrm:"+k.Fingerprint, "Remover", kindButton, nil)
	}
	for _, ad := range p.acc.a.Admins {
		add("adrm:"+ad.Name, "Remover admin", kindButton, nil)
	}
	add("accKey", "Chave publica", kindInput, p.acc.key)
	add("accUser", "Admin", kindInput, p.acc.user)
	add("accPass", "Senha do sudo", kindInput, p.acc.pass)
	add("accPass2", "De novo", kindInput, p.acc.pass2)
	if p.acc.a.TUI {
		add("accAddTUI", "Dar acesso a TUI", kindButton, nil)
	}
	add("accAddAdmin", "Dar acesso de admin", kindButton, nil)
	return items
}

// accessUpdate trata as respostas do daemon.
func (p *setupPage) accessUpdate(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case accessLoadedMsg:
		p.acc.loaded = true
		p.acc.err = ""
		if msg.err != nil {
			p.acc.err = firstLine(msg.err.Error())
		} else {
			p.acc.a = msg.a
		}
		p.rebuild()
	case accessDoneMsg:
		if msg.err != nil {
			p.acc.msgC, p.acc.msg = "danger", "Nao deu: "+firstLine(msg.err.Error())
			return nil
		}
		// a chave sai do campo so quando deu certo (com erro, corrige e
		// manda de novo)
		if msg.added {
			p.acc.key.SetValue("")
			p.acc.user.SetValue("")
		}
		p.acc.msgC, p.acc.msg = "ok", msg.detail
		return p.loadAccess()
	}
	return nil
}

// accessActivate e o Enter ou clique num botao dos Acessos.
func (p *setupPage) accessActivate(id string, armed bool) tea.Cmd {
	api := p.api
	done := func(fn func(context.Context) (string, error)) tea.Cmd {
		added := id == "accAddTUI" || id == "accAddAdmin"
		return func() tea.Msg {
			d, err := fn(context.Background())
			return accessDoneMsg{d, added, err}
		}
	}
	switch {
	case strings.HasPrefix(id, "akuse:"):
		fp := strings.TrimPrefix(id, "akuse:")
		for _, k := range p.acc.a.Keys {
			if k.Fingerprint == fp {
				p.acc.key.SetValue(k.Key)
				p.acc.msgC, p.acc.msg = "ok", "Chave de "+cmpOrUI(k.Comment, k.Fingerprint)+" no campo: pra admin, preencha o Admin e a senha do sudo e clique em Dar acesso de admin."
				return p.focusAt(p.index("accUser"))
			}
		}
		return nil
	case strings.HasPrefix(id, "akrm:") || strings.HasPrefix(id, "adrm:"):
		if !armed {
			p.acc.arm = id
			what := "Tirar essa chave de todo lugar (admins, TUI e root)? Quem usa ela nao entra mais."
			if strings.HasPrefix(id, "adrm:") {
				what = "Tirar o admin " + strings.TrimPrefix(id, "adrm:") + "? Sai do sudo, a conta expira e as chaves so dele saem (a pasta fica)."
			}
			p.acc.msgC, p.acc.msg = "warn", what+" Aperte de novo pra confirmar."
			return nil
		}
		p.acc.msgC, p.acc.msg = "warn", "Tirando..."
		req := core.RemoveAccessRequest{Fingerprint: strings.TrimPrefix(id, "akrm:")}
		if strings.HasPrefix(id, "adrm:") {
			req = core.RemoveAccessRequest{Admin: strings.TrimPrefix(id, "adrm:")}
		}
		return done(func(ctx context.Context) (string, error) { return api.RemoveAccess(ctx, req) })
	case id == "accAddTUI" || id == "accAddAdmin":
		req := host.AccessRequest{Key: strings.TrimSpace(p.acc.key.Value())}
		if _, _, err := host.ParseKey(req.Key); err != nil {
			p.acc.msgC, p.acc.msg = "danger", err.Error()
			return p.focusAt(p.index("accKey"))
		}
		if id == "accAddAdmin" {
			req.Admin, req.Password = strings.TrimSpace(p.acc.user.Value()), p.acc.pass.Value()
			switch {
			case req.Admin == "":
				p.acc.msgC, p.acc.msg = "danger", "Falta o nome do admin (um usuario por pessoa, ex: maria)."
				return p.focusAt(p.index("accUser"))
			case req.Password != p.acc.pass2.Value():
				p.acc.msgC, p.acc.msg = "danger", "As senhas nao conferem."
				p.acc.pass2.SetValue("")
				return p.focusAt(p.index("accPass2"))
			}
		}
		// a senha sai dos campos na hora; a chave, quando der certo
		p.acc.pass.SetValue("")
		p.acc.pass2.SetValue("")
		p.acc.msgC, p.acc.msg = "warn", "Dando acesso..."
		return done(func(ctx context.Context) (string, error) { return api.AddAccess(ctx, req) })
	}
	return nil
}

// whereLabel diz onde a chave entra, em palavras.
func whereLabel(users []string) string {
	var out []string
	for _, u := range users {
		switch u {
		case "root":
			out = append(out, "root")
		case "cais":
			out = append(out, "TUI")
		default:
			out = append(out, "admin "+u)
		}
	}
	return strings.Join(out, " · ")
}

// viewAccess desenha os Acessos: as chaves, os admins e o dar acesso.
func (p *setupPage) viewAccess(w int) string {
	t, a := p.th, p.acc.a
	muted := t.fg(t.muted)
	wrap := lipgloss.NewStyle().Width(w)
	focusID := ""
	if p.inForm {
		focusID = p.items[p.focus].id
	}
	button := func(id, label string, k btnKind) string {
		return focusMarked(focusID == id, p.zones.Mark(id, t.button(label, k, focusID == id)))
	}
	chip := func(id, label string) string {
		if p.acc.arm == id {
			label = "Confirmar"
		}
		return focusMarked(focusID == id, p.zones.Mark(id, t.chip(label, btnNormal, focusID == id)))
	}
	var rows []string
	if p.env.Tailscale.Connected() && !p.sshPublic() {
		rows = append(rows, "", wrap.Foreground(t.warn).Render("O SSH so entra pela tailnet: alem da chave, a pessoa precisa da VPS no Tailscale dela. "+
			"No painel do Tailscale (login.tailscale.com/admin/machines), menu ... da VPS > Share (so esta maquina), "+
			"ou Users > Invite (a tailnet inteira)."))
	}
	switch {
	case p.acc.err != "":
		rows = append(rows, "", wrap.Foreground(t.danger).Render("Nao deu pra ler os acessos: "+p.acc.err))
	case !p.acc.loaded:
		rows = append(rows, "", muted.Render("Lendo..."))
	default:
		rows = append(rows, "", t.bold().Render("Chaves")+muted.Render("  cada pessoa com a propria; tirar uma chave tira de todo lugar"))
		if len(a.Keys) == 0 {
			rows = append(rows, muted.Render("  nenhuma"))
		}
		for _, k := range a.Keys {
			fp := k.Fingerprint
			if len(fp) > 19 {
				fp = fp[:19] + "…"
			}
			line := "  " + t.bold().Render(cmpOrUI(k.Comment, "(sem nome)")) + muted.Render("  "+k.Type+"  "+fp+"  ") +
				whereLabel(k.Users) + "  "
			if k.Key != "" {
				line += chip("akuse:"+k.Fingerprint, "Usar") + " "
			}
			line += chip("akrm:"+k.Fingerprint, "Remover")
			rows = append(rows, wrap.Render(line))
		}
		rows = append(rows, "", t.bold().Render("Admins")+muted.Render("  usuario proprio com shell, sudo e docker; entram tambem na TUI"))
		if len(a.Admins) == 0 {
			rows = append(rows, muted.Render("  nenhum (o Preparar VPS, na Seguranca, cria o primeiro)"))
		}
		for _, ad := range a.Admins {
			n := fmt.Sprintf("%d chave(s)", ad.Keys)
			if ad.Keys == 0 {
				n = "sem chave: nao entra pelo SSH"
			}
			rows = append(rows, wrap.Render("  "+t.bold().Render(ad.Name)+muted.Render("  "+n+"  ")+chip("adrm:"+ad.Name, "Remover admin")))
		}
	}

	rows = append(rows, "", t.rule(w), t.bold().Render("Dar acesso"))
	tui := "so a TUI: ssh cais@<vps> abre direto a TUI (deploy, logs, apps; sem shell). Ela controla a VPS inteira, entao so pra quem administra os apps."
	if !a.TUI {
		tui = "sem o usuario cais (install.sh sem --ssh): so da pra dar acesso de admin."
	}
	rows = append(rows, wrap.Render("Peca a chave PUBLICA da pessoa (o conteudo do ~/.ssh/id_ed25519.pub; se ela nao tiver: ssh-keygen -t ed25519). "+
		"Dois jeitos:"), muted.Width(w).PaddingLeft(2).Render(tui),
		muted.Width(w).PaddingLeft(2).Render("admin: usuario proprio com shell, sudo e o grupo docker (ssh nome@<vps>); a senha e so do sudo, o SSH continua so por chave."), "")
	for _, f := range []struct {
		id, label, note string
		in              *textinput.Model
		width           int
	}{
		{"accKey", "Chave publica", "", p.acc.key, min(80, max(20, w-20))},
		{"accUser", "Admin", "  so pro acesso de admin: um usuario por pessoa", p.acc.user, 20},
		{"accPass", "Senha do sudo", "  admin novo ou sem senha", p.acc.pass, 20},
		{"accPass2", "De novo", "", p.acc.pass2, 20},
	} {
		marker, label := "  ", t.fg(t.text).Width(16).Render(f.label)
		if focusID == f.id {
			marker = t.fg(t.accent).Bold(true).Render("› ")
			label = t.fg(t.accent).Bold(true).Width(16).Render(f.label)
		}
		rows = append(rows, focusMarked(focusID == f.id, p.zones.Mark(f.id, marker+label+t.input(f.in, f.width)))+muted.Render(f.note))
		if f.id == "accKey" {
			if hint, c := p.keyHint(); hint != "" {
				rows = append(rows, lipgloss.NewStyle().Width(w).PaddingLeft(18).Foreground(c).Render(hint))
			}
		}
	}
	btns := button("accAddAdmin", "Dar acesso de admin", btnNormal)
	if a.TUI {
		btns = button("accAddTUI", "Dar acesso a TUI", btnNormal) + " " + btns
	}
	rows = append(rows, "", "  "+btns)
	if p.acc.msg != "" {
		c := map[string]color.Color{"ok": t.ok, "warn": t.warn, "danger": t.danger}[p.acc.msgC]
		rows = append(rows, "", wrap.Foreground(c).Render(p.acc.msg))
	}
	return strings.Join(rows, "\n")
}

// keyHint diz, enquanto a chave e colada, se ela ja entra (e onde) ou e
// nova; com a chave invalida, o porque.
func (p *setupPage) keyHint() (string, color.Color) {
	t := p.th
	v := strings.TrimSpace(p.acc.key.Value())
	if v == "" {
		return "", nil
	}
	_, k, err := host.ParseKey(v)
	if err != nil {
		return err.Error(), t.danger
	}
	for _, have := range p.acc.a.Keys {
		if have.Fingerprint == k.Fingerprint {
			return "Essa chave ja entra: " + whereLabel(have.Users) + ". Dar acesso so acrescenta o que falta.", t.warn
		}
	}
	return "Chave nova: " + cmpOrUI(k.Comment, k.Fingerprint) + " ainda nao entra.", t.ok
}
