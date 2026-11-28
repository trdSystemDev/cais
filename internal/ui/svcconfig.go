package ui

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/stacks"
)

// svcConfig e a config de um servico, na secao Geral do card dele na aba
// Infra. No proxy: os certificados (Let's Encrypt com o e-mail, ou a CA
// local com o dominio do teste) e as portas no host (padrao 80/443, ou
// atras de outro proxy da VPS). No Garage: o endereco publico do S3. Nos
// bancos e no Garage: a versao (tag da imagem). Salvar grava a config e, com o servico ligado, aplica (ele sobe de novo
// com ela). shown enquanto o card do servico esta selecionado; active diz
// se o foco do teclado esta nele.
type svcConfig struct {
	api   core.API
	zones *zone.Manager
	th    theme

	shown  bool
	name   string // o servico: proxy ou garage
	active bool
	s3     *textinput.Model // garage: endereco publico do S3
	// version e a tag da imagem (postgres, mysql, garage); vazio e a do cais
	version *textinput.Model
	// port e a porta do banco no loopback da VPS (postgres, mysql); vazio e
	// a padrao
	port *textinput.Model
	// running e a tag do container no ar (vazio: parado ou sem subir)
	running string
	email   *textinput.Model
	local   bool             // CA local (teste) em vez do Let's Encrypt
	dns     bool             // Let's Encrypt pela prova no DNS da Cloudflare
	token   *textinput.Model // token da API da Cloudflare (vazio mantem o gravado)
	wild    *textinput.Model // dominios curinga, separados por virgula
	domain  *textinput.Model
	http    *textinput.Model
	https   *textinput.Model
	behind  bool // atras de outro proxy da VPS (so no 127.0.0.1)
	focus   int  // indice em ids()
	busy    bool
	err     string // da validacao: varias linhas
}

// svcConfigSavedMsg e a volta do Salvar do painel de config do servico.
type svcConfigSavedMsg struct {
	name string
	err  error
}

func newSvcConfig(api core.API, zones *zone.Manager, th theme) *svcConfig {
	p := &svcConfig{api: api, zones: zones}
	mk := func(placeholder string, limit, width int) *textinput.Model {
		ti := textinput.New()
		ti.Prompt = ""
		ti.Placeholder = placeholder
		ti.CharLimit = limit
		ti.SetWidth(width)
		return &ti
	}
	p.email = mk("avisos do Let's Encrypt", 254, 28)
	p.token = mk("vazio mantem o gravado", 200, 28)
	p.token.EchoMode = textinput.EchoPassword
	p.wild = mk("ex: dev.exemplo.com.br", 1000, 28)
	p.domain = mk("ex: trd.localhost", 253, 28)
	p.s3 = mk("ex: s3.exemplo.com.br", 253, 28)
	p.version = mk("", 128, 28)
	p.port = mk("", 5, 8)
	p.http, p.https = mk(strconv.Itoa(config.DefaultHTTPPort), 5, 8), mk(strconv.Itoa(config.DefaultHTTPSPort), 5, 8)
	p.setTheme(th)
	return p
}

func (p *svcConfig) setTheme(th theme) {
	p.th = th
	for _, ti := range p.inputs() {
		th.styleInput(ti)
	}
}

func (p *svcConfig) inputs() []*textinput.Model {
	return []*textinput.Model{p.email, p.token, p.wild, p.domain, p.http, p.https, p.s3, p.version, p.port}
}

// hasConfig diz se o servico tem o painel de config.
func hasConfig(name string) bool { return name == "proxy" || stacks.Versioned(name) }

// ids sao os itens na ordem do Tab. No proxy, o e-mail so com o Let's
// Encrypt e o dominio do teste so com a CA local.
func (p *svcConfig) ids() []string {
	switch p.name {
	case "garage":
		return []string{"s3-host", "svc-version", "cfg-save"}
	case "postgres", "mysql":
		return []string{"svc-version", "svc-port", "cfg-save"}
	}
	ids := []string{"cert-tls"}
	switch {
	case p.local:
		ids = append(ids, "cert-domain")
	case p.dns:
		ids = append(ids, "cert-email", "cert-token", "cert-wild")
	default:
		ids = append(ids, "cert-email")
	}
	return append(ids, "ports-http", "ports-https", "ports-behind", "cfg-save")
}

func (p *svcConfig) input(id string) *textinput.Model {
	switch id {
	case "cert-email":
		return p.email
	case "cert-domain":
		return p.domain
	case "cert-token":
		return p.token
	case "cert-wild":
		return p.wild
	case "ports-http":
		return p.http
	case "ports-https":
		return p.https
	case "s3-host":
		return p.s3
	case "svc-version":
		return p.version
	case "svc-port":
		return p.port
	}
	return nil
}

func (p *svcConfig) focusID() string {
	ids := p.ids()
	return ids[min(p.focus, len(ids)-1)]
}

func (p *svcConfig) typing() bool { return p.shown && p.active && p.input(p.focusID()) != nil }

func (p *svcConfig) blur() {
	p.active = false
	for _, ti := range p.inputs() {
		ti.Blur()
	}
}

func (p *svcConfig) setFocus(i int) tea.Cmd {
	ids := p.ids()
	n := len(ids)
	p.focus = ((i % n) + n) % n
	var cmd tea.Cmd
	for j, id := range ids {
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

// show abre o painel do servico name com a config salva.
func (p *svcConfig) show(name string, c config.Config) {
	p.shown, p.name, p.busy, p.err, p.active, p.focus = true, name, false, "", false, 0
	p.fill(c)
}

// fill poe nos campos a config salva (ela pode chegar depois do painel
// abrir); nao mexe no que a pessoa esta editando.
func (p *svcConfig) fill(c config.Config) {
	if p.active || p.busy || p.err != "" {
		return
	}
	p.email.SetValue(c.Email)
	p.local = !c.LetsEncrypt()
	p.dns = c.DNSChallenge()
	p.token.SetValue("")
	p.wild.SetValue(strings.Join(c.Proxy.Wildcards, ", "))
	p.domain.SetValue(c.TestDomain)
	p.http.SetValue("")
	p.https.SetValue("")
	if c.Proxy.HTTPPort > 0 {
		p.http.SetValue(strconv.Itoa(c.Proxy.HTTPPort))
	}
	if c.Proxy.HTTPSPort > 0 {
		p.https.SetValue(strconv.Itoa(c.Proxy.HTTPSPort))
	}
	p.behind = c.Proxy.BehindProxy
	p.s3.SetValue(c.Hosts.S3)
	p.version.SetValue(c.Versions[p.name])
	p.version.Placeholder = ""
	if img, ok := stacks.Images[p.name]; ok {
		p.version.Placeholder = "vazio: " + img.Default + " (a do cais)"
	}
	p.port.SetValue("")
	if n := c.DBPorts[p.name]; n != 0 {
		p.port.SetValue(strconv.Itoa(n))
	}
	p.port.Placeholder = strconv.Itoa(config.DefaultDBPorts[p.name])
}

func (p *svcConfig) close() {
	p.shown = false
	p.blur()
}

// save grava o que o painel mostra na config de agora (so isso muda).
func (p *svcConfig) save() tea.Cmd {
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
	email, local, domain, behind := strings.TrimSpace(p.email.Value()), p.local, strings.TrimSpace(p.domain.Value()), p.behind
	dns, token, wild := p.dns && !p.local, strings.TrimSpace(p.token.Value()), wildcards(p.wild.Value())
	name, s3, version := p.name, strings.TrimSpace(p.s3.Value()), strings.TrimSpace(p.version.Value())
	// vazio e a padrao (0); texto que nao e numero vira -1, e a validacao explica
	dbPort := 0
	if v := strings.TrimSpace(p.port.Value()); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			dbPort = n
		} else {
			dbPort = -1
		}
	}
	p.busy, p.err = true, ""
	return func() tea.Msg {
		ctx := context.Background()
		info, err := p.api.LoadConfig(ctx)
		if err != nil {
			return svcConfigSavedMsg{name, err}
		}
		c := info.Config
		if stacks.Versioned(name) {
			// copia: o mapa da config carregada nao e do painel
			c.Versions = maps.Clone(c.Versions)
			if c.Versions == nil {
				c.Versions = map[string]string{}
			}
			c.Versions[name] = version
			if version == "" {
				delete(c.Versions, name)
			}
			if name == "garage" {
				c.Hosts.S3 = s3
			}
			if config.DefaultDBPorts[name] != 0 {
				c.DBPorts = maps.Clone(c.DBPorts)
				if c.DBPorts == nil {
					c.DBPorts = map[string]int{}
				}
				c.DBPorts[name] = dbPort
				// vazio ou a padrao: sai da config
				if dbPort == 0 || dbPort == config.DefaultDBPorts[name] {
					delete(c.DBPorts, name)
				}
			}
			return svcConfigSavedMsg{name, p.api.SaveConfig(ctx, c)}
		}
		c.Email = email
		c.TLS, c.TestDomain = "", ""
		if local {
			c.TLS, c.TestDomain = config.TLSSelfSigned, domain
		}
		c.Proxy.DNS, c.Proxy.Wildcards = "", nil
		if dns {
			c.Proxy.DNS, c.Proxy.Wildcards = config.DNSCloudflare, wild
		}
		c.Proxy.HTTPPort, c.Proxy.HTTPSPort, c.Proxy.BehindProxy = httpPort, httpsPort, behind
		// o token antes da config: aplicar o proxy pelo DNS precisa dele
		if dns && token != "" {
			if err := p.api.SetSecret(ctx, config.CloudflareTokenSecret, token); err != nil {
				return svcConfigSavedMsg{name, err}
			}
		}
		return svcConfigSavedMsg{name, p.api.SaveConfig(ctx, c)}
	}
}

// toggle e o enter, espaco ou clique numa opcao.
func (p *svcConfig) toggle(id string) tea.Cmd {
	switch id {
	case "cert-tls":
		// Let's Encrypt (porta 80) -> Let's Encrypt (DNS) -> CA local -> ...
		switch {
		case p.local:
			p.local, p.dns = false, false
		case p.dns:
			p.local, p.dns = true, false
		default:
			p.dns = true
		}
		return p.setFocus(slices.Index(p.ids(), "cert-tls"))
	case "ports-behind":
		p.behind = !p.behind
	}
	return nil
}

// key: o Tab, as setas pra cima e pra baixo e o esc ficam com a aba Infra.
func (p *svcConfig) key(msg tea.KeyPressMsg) tea.Cmd {
	id := p.focusID()
	switch k := msg.String(); k {
	case "ctrl+s":
		return p.save()
	case "left", "right":
		if id == "cert-tls" {
			return p.toggle(id)
		}
	case "enter", "space":
		switch id {
		case "cfg-save":
			return p.save()
		case "cert-tls", "ports-behind":
			return p.toggle(id)
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

func (p *svcConfig) paste(msg tea.PasteMsg) tea.Cmd {
	if ti := p.input(p.focusID()); ti != nil && p.active {
		v, cmd := ti.Update(msg)
		*ti = v
		return cmd
	}
	return nil
}

// click no painel; ok diz se acertou algo dele.
func (p *svcConfig) click(msg tea.MouseMsg) (tea.Cmd, bool) {
	for i, id := range p.ids() {
		if !p.zones.Get(id).InBounds(msg) {
			continue
		}
		p.active = true
		cmd := p.setFocus(i)
		switch id {
		case "cfg-save":
			return tea.Batch(cmd, p.save()), true
		case "cert-tls", "ports-behind":
			return tea.Batch(cmd, p.toggle(id)), true
		}
		return cmd, true
	}
	return nil, false
}

// view e o conteudo do painel, sem borda (vai dentro da secao Geral): c e
// a config salva (os avisos), on diz se o proxy esta ligado (Salvar tambem
// aplica).
func (p *svcConfig) view(c config.Config, on bool, w int) string {
	t := p.th
	muted := t.fg(t.muted)
	wrap := lipgloss.NewStyle().Width(w)
	focusID := ""
	if p.active {
		focusID = p.focusID()
	}
	label := func(id, text string) string {
		if id == focusID {
			return t.fg(t.accent).Bold(true).Render("› ") + t.fg(t.accent).Bold(true).Width(14).Render(text)
		}
		return "  " + t.fg(t.text).Width(14).Render(text)
	}
	field := func(id, text string, width int) string {
		return p.zones.Mark(id, label(id, text)+t.input(p.input(id), width))
	}
	note := func(text string) string {
		return muted.Width(max(w-16, 20)).MarginLeft(16).Render(text) + "\n"
	}

	saveBtn := func() string {
		kind := btnNormal
		if p.busy {
			kind = btnOff
		}
		return "\n  " + p.zones.Mark("cfg-save", t.button(saveLabel(on, p.busy), kind, focusID == "cfg-save")) + "\n"
	}
	var b strings.Builder
	if img, ok := stacks.Images[p.name]; ok {
		if p.name == "garage" {
			b.WriteString(t.bold().Render("Endereco do S3") + "  " + muted.Render("API S3, com HTTPS pelo proxy") + "\n\n")
			b.WriteString(field("s3-host", "Endereco", 30) + "\n")
			b.WriteString(note("O nome publico por onde os apps e os clientes S3 acessam os buckets (regiao garage). " +
				"O DNS dele aponta pra VPS, como o dos apps."))
			b.WriteString("\n" + t.rule(w) + "\n")
		}
		running := "nao esta no ar"
		if p.running != "" {
			running = "no ar: " + p.running
		}
		b.WriteString(field("svc-version", "Versao", 22) + "  " + p.zones.Mark("cfg-save", t.button(saveLabel(on, p.busy), btnNormal, focusID == "cfg-save")) + "\n")
		b.WriteString(note("Tag de " + img.Repo + " (ex: " + img.Example + "), " + running + ". " + versionNote[p.name]))
		if def := config.DefaultDBPorts[p.name]; def != 0 {
			b.WriteString(field("svc-port", "Porta", 8) + "\n")
			b.WriteString(note(fmt.Sprintf("No 127.0.0.1, pro tunel SSH: outra se a %d estiver ocupada. Os apps seguem em %s:%d.", def, p.name, def)))
		}
		if !on {
			b.WriteString("\n" + wrap.Inherit(muted).Render("O "+p.name+" esta desligado: isso vale quando ele ligar.") + "\n")
		}
		p.viewErr(&b, w)
		return b.String()
	}
	b.WriteString(t.bold().Render("Certificados") + "  " + muted.Render("HTTPS dos apps") + "\n\n")
	mode := "Let's Encrypt"
	switch {
	case p.local:
		mode = "CA local (teste)"
	case p.dns:
		mode = "Let's Encrypt (DNS Cloudflare)"
	}
	sel := lipgloss.NewStyle().Background(t.fieldBg).Foreground(t.fieldFg).Width(36).Render("‹ " + mode + " ›")
	b.WriteString(p.zones.Mark("cert-tls", label("cert-tls", "Emissor")+sel) + "\n")
	if p.local {
		b.WriteString(field("cert-domain", "Dominio", 30) + "\n")
		b.WriteString(note("So pra teste: a CA local do cais assina os nomes desse dominio (apps em <app>.<dominio>)."))
	} else {
		b.WriteString(field("cert-email", "E-mail", 30) + "\n")
		if !p.dns {
			b.WriteString(note("Cada app pede o proprio certificado; o Let's Encrypt avisa nesse e-mail se algum for vencer."))
		}
	}
	if p.dns && !p.local {
		b.WriteString(field("cert-token", "Token", 30) + "\n")
		b.WriteString(note("Da Cloudflare, com DNS: Edit e Zone: Read, so na zona do dominio. Serve ate pra IP do Tailscale; os dominios precisam estar na Cloudflare."))
		b.WriteString(field("cert-wild", "Curingas", 30) + "\n")
		b.WriteString(note("Opcional: dev.exemplo.com.br cobre *.dev.exemplo.com.br com um certificado so."))
	}

	b.WriteString("\n" + t.rule(w) + "\n")
	b.WriteString(t.bold().Render("Portas") + "  " + muted.Render("no host; padrao 80 e 443") + "\n\n")
	b.WriteString(wrap.Inherit(muted).Render("Outras portas so pra testar ou numa migracao, com outro proxy ainda nas 80/443 "+
		"(marque Outro proxy). O HTTP redireciona pra porta HTTPS daqui.") + "\n\n")
	b.WriteString(field("ports-http", "Porta HTTP", 8) + "\n")
	b.WriteString(field("ports-https", "Porta HTTPS", 8) + "\n")
	box := "[ ]"
	if p.behind {
		box = "[x]"
	}
	b.WriteString(p.zones.Mark("ports-behind", label("ports-behind", "Outro proxy")+
		t.fg(t.text).Render(box+" atras do nginx desta VPS")) + "\n")
	if p.behind {
		b.WriteString(note("So no 127.0.0.1 (nada aberto na internet); o Traefik aceita o X-Forwarded-For dele, " +
			"e os apps veem o IP de quem acessa."))
	}

	b.WriteString(saveBtn())
	if !on {
		b.WriteString("\n" + wrap.Inherit(muted).Render("O proxy esta desligado: isso vale quando ele ligar.") + "\n")
	}
	if warn := c.PortsWarning(); warn != "" {
		b.WriteString("\n" + wrap.Foreground(t.warn).Render("Atencao: "+warn) + "\n")
	}
	p.viewErr(&b, w)
	return b.String()
}

// versionNote e o que a troca de versao de cada servico pode e nao pode.
var versionNote = map[string]string{
	"postgres": "Aplicar recria o container (o banco cai por segundos). So na mesma major: outra nao abre os dados.",
	"mysql":    "Aplicar recria o container (o banco cai por segundos). Nao volta pra uma versao mais velha.",
	"garage":   "Aplicar recria o container. So na mesma major (outra pede garage migrate), sem voltar versao.",
}

func saveLabel(on, busy bool) string {
	switch {
	case busy:
		return "Salvando..."
	case on:
		return "Salvar e aplicar"
	}
	return "Salvar"
}

// viewErr e o erro do ultimo Salvar (a validacao: varias linhas).
func (p *svcConfig) viewErr(b *strings.Builder, w int) {
	if p.err == "" {
		return
	}
	t := p.th
	b.WriteString("\n" + t.fg(t.danger).Bold(true).Render("Nao salvou") + "\n")
	for _, l := range strings.Split(p.err, "\n") {
		b.WriteString(lipgloss.NewStyle().Width(w).Foreground(t.danger).Render("  "+l) + "\n")
	}
}

// wildcards le a lista de curingas: separados por virgula ou espaco, sem o
// "*." (a pessoa pode colar *.dev.exemplo.com.br).
func wildcards(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		f = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(f), "*."))
		if f != "" && !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}
