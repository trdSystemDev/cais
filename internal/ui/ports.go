package ui

import (
	"context"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/core"
)

// portsPanel sao as portas do proxy no host (padrao 80/443), na secao
// Geral do card do proxy na aba Infra. Salvar grava a config e, com o
// proxy ligado, aplica (o proxy sobe de novo nas portas novas). shown
// enquanto o card do proxy esta selecionado; active diz se o foco do
// teclado esta nele.
type portsPanel struct {
	api   core.API
	zones *zone.Manager
	th    theme

	shown       bool
	active      bool
	http, https *textinput.Model
	behind      bool // atras de outro proxy da VPS (so no 127.0.0.1)
	focus       int
	busy        bool
	err         string // da validacao: varias linhas
}

// portsSavedMsg e a volta do Salvar das portas.
type portsSavedMsg struct{ err error }

var portsIDs = []string{"ports-http", "ports-https", "ports-behind", "ports-save"}

func newPortsPanel(api core.API, zones *zone.Manager, th theme) *portsPanel {
	p := &portsPanel{api: api, zones: zones}
	mk := func(placeholder string) *textinput.Model {
		ti := textinput.New()
		ti.Prompt = ""
		ti.Placeholder = placeholder
		ti.CharLimit = 5
		ti.SetWidth(8)
		return &ti
	}
	p.http, p.https = mk(strconv.Itoa(config.DefaultHTTPPort)), mk(strconv.Itoa(config.DefaultHTTPSPort))
	p.setTheme(th)
	return p
}

func (p *portsPanel) setTheme(th theme) {
	p.th = th
	th.styleInput(p.http)
	th.styleInput(p.https)
}

func (p *portsPanel) input(id string) *textinput.Model {
	switch id {
	case "ports-http":
		return p.http
	case "ports-https":
		return p.https
	}
	return nil
}

func (p *portsPanel) typing() bool { return p.shown && p.active && p.input(portsIDs[p.focus]) != nil }

func (p *portsPanel) blur() {
	p.active = false
	p.http.Blur()
	p.https.Blur()
}

func (p *portsPanel) setFocus(i int) tea.Cmd {
	n := len(portsIDs)
	p.focus = ((i % n) + n) % n
	var cmd tea.Cmd
	for j, id := range portsIDs {
		if ti := p.input(id); ti != nil {
			if j == p.focus && p.active {
				cmd = ti.Focus()
			} else {
				ti.Blur()
			}
		}
	}
	return cmd
}

// show abre o painel com as portas da config salva.
func (p *portsPanel) show(c config.Proxy) {
	p.shown, p.busy, p.err, p.active = true, false, "", false
	p.fill(c)
}

// fill poe nos campos as portas da config salva (a config pode chegar
// depois do painel abrir); nao mexe no que a pessoa esta editando.
func (p *portsPanel) fill(c config.Proxy) {
	if p.active || p.busy || p.err != "" {
		return
	}
	p.http.SetValue("")
	p.https.SetValue("")
	p.behind = c.BehindProxy
	if c.HTTPPort > 0 {
		p.http.SetValue(strconv.Itoa(c.HTTPPort))
	}
	if c.HTTPSPort > 0 {
		p.https.SetValue(strconv.Itoa(c.HTTPSPort))
	}
}

func (p *portsPanel) close() {
	p.shown = false
	p.blur()
}

// save grava as portas na config de agora (so elas mudam).
func (p *portsPanel) save() tea.Cmd {
	if p.busy {
		return nil
	}
	// vazio e o padrao; texto que nao e numero vira 0, e a validacao explica
	port := func(ti *textinput.Model, def int) int {
		if v := strings.TrimSpace(ti.Value()); v != "" {
			n, _ := strconv.Atoi(v)
			return n
		}
		return def
	}
	httpPort, httpsPort := port(p.http, config.DefaultHTTPPort), port(p.https, config.DefaultHTTPSPort)
	behind := p.behind
	p.busy, p.err = true, ""
	return func() tea.Msg {
		ctx := context.Background()
		info, err := p.api.LoadConfig(ctx)
		if err != nil {
			return portsSavedMsg{err}
		}
		c := info.Config
		c.Proxy.HTTPPort, c.Proxy.HTTPSPort, c.Proxy.BehindProxy = httpPort, httpsPort, behind
		return portsSavedMsg{p.api.SaveConfig(ctx, c)}
	}
}

// key: o Tab, as setas e o esc ficam com a aba Infra.
func (p *portsPanel) key(msg tea.KeyPressMsg) tea.Cmd {
	id := portsIDs[p.focus]
	switch k := msg.String(); k {
	case "ctrl+s":
		return p.save()
	case "enter", "space":
		if id == "ports-save" {
			return p.save()
		}
		if id == "ports-behind" {
			p.behind = !p.behind
			return nil
		}
		if k == "enter" {
			return p.setFocus(p.focus + 1)
		}
	}
	if ti := p.input(id); ti != nil {
		v, cmd := ti.Update(msg)
		*ti = v
		return cmd
	}
	return nil
}

func (p *portsPanel) paste(msg tea.PasteMsg) tea.Cmd {
	if ti := p.input(portsIDs[p.focus]); ti != nil && p.active {
		v, cmd := ti.Update(msg)
		*ti = v
		return cmd
	}
	return nil
}

// click no painel; ok diz se acertou algo dele.
func (p *portsPanel) click(msg tea.MouseMsg) (tea.Cmd, bool) {
	for i, id := range portsIDs {
		if !p.zones.Get(id).InBounds(msg) {
			continue
		}
		p.active = true
		cmd := p.setFocus(i)
		switch id {
		case "ports-save":
			return tea.Batch(cmd, p.save()), true
		case "ports-behind":
			p.behind = !p.behind
		}
		return cmd, true
	}
	return nil, false
}

// view e o conteudo do painel, sem borda (vai dentro da secao Geral): c e
// a config salva (o aviso do Let's Encrypt), on diz se o proxy esta ligado
// (Salvar tambem aplica).
func (p *portsPanel) view(c config.Config, on bool, w int) string {
	t := p.th
	muted := t.fg(t.muted)
	wrap := lipgloss.NewStyle().Width(w)
	focusID := ""
	if p.active {
		focusID = portsIDs[p.focus]
	}
	field := func(id, label string) string {
		marker, ls := "  ", t.fg(t.text).Width(14)
		if id == focusID {
			marker, ls = t.fg(t.accent).Bold(true).Render("› "), t.fg(t.accent).Bold(true).Width(14)
		}
		return p.zones.Mark(id, marker+ls.Render(label)+t.input(p.input(id), 8))
	}
	btn := func(id, label string, k btnKind) string {
		if p.busy {
			k = btnOff
		}
		return p.zones.Mark(id, t.button(label, k, id == focusID))
	}

	var b strings.Builder
	b.WriteString(t.bold().Render("Portas do proxy") + "  " + muted.Render("no host; padrao 80 e 443") + "\n\n")
	b.WriteString(wrap.Inherit(muted).Render("Outras portas so pra testar ou numa migracao, com outro proxy ainda nas 80/443 "+
		"(marque Outro proxy). O HTTP redireciona pra porta HTTPS daqui.") + "\n\n")
	b.WriteString(field("ports-http", "Porta HTTP") + "\n")
	b.WriteString(field("ports-https", "Porta HTTPS") + "\n")
	marker, ls := "  ", t.fg(t.text).Width(14)
	if focusID == "ports-behind" {
		marker, ls = t.fg(t.accent).Bold(true).Render("› "), t.fg(t.accent).Bold(true).Width(14)
	}
	box := "[ ]"
	if p.behind {
		box = "[x]"
	}
	b.WriteString(p.zones.Mark("ports-behind", marker+ls.Render("Outro proxy")+t.fg(t.text).Render(box+" atras do nginx desta VPS")) + "\n")
	if p.behind {
		b.WriteString(muted.Width(max(w-16, 20)).MarginLeft(16).Render("So no 127.0.0.1 (nada aberto na internet); o Traefik "+
			"aceita o X-Forwarded-For dele, e os apps veem o IP de quem acessa.") + "\n")
	}
	b.WriteString("\n")
	label := "Salvar"
	if on {
		label = "Salvar e aplicar"
	}
	if p.busy {
		label = "Salvando..."
	}
	b.WriteString("  " + btn("ports-save", label, btnNormal) + "\n")
	if !on {
		b.WriteString("\n" + wrap.Inherit(muted).Render("O proxy esta desligado: as portas valem quando ele ligar.") + "\n")
	}
	if w := c.PortsWarning(); w != "" {
		b.WriteString("\n" + wrap.Foreground(t.warn).Render("Atencao: "+w) + "\n")
	}
	if p.err != "" {
		b.WriteString("\n" + t.fg(t.danger).Bold(true).Render("Nao salvou") + "\n")
		for _, l := range strings.Split(p.err, "\n") {
			b.WriteString(wrap.Foreground(t.danger).Render("  "+l) + "\n")
		}
	}
	return b.String()
}
