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
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/trdSystemDev/cais/internal/bootstrap"
	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/stacks"
)

const formWidth = 50

// item e cada coisa focavel do formulario, na ordem do Tab.
type item struct {
	id    string
	label string
	kind  int
	input *textinput.Model
}

const (
	kindInput = iota
	kindSelect
	kindCheck
	kindButton
)

// setupPage e a aba Setup: formulario do config.yaml a esquerda e, a
// direita, o plano (o que o Setup faria) ou o andamento da execucao. So
// chama acoes do core; o progresso chega pelo estado, entao um Setup
// disparado pelo CLI tambem aparece aqui.
type setupPage struct {
	api   core.API
	zones *zone.Manager
	th    theme

	items []*item
	focus int
	tls   int // 0 Let's Encrypt, 1 CA local
	on    map[string]bool

	cfg     config.Config // ultima lida: base do que o form nao mostra
	plan    bootstrap.Plan
	setup   core.Setup // ultima foto do Setup no estado
	status  string
	statusC string // ok, warn, danger
	saveErr string // por que o ultimo Salvar falhou (varias linhas)
	showRun bool   // painel mostra a execucao em vez do plano
	running bool   // o clique em Executar ainda nao voltou

	// inicio da ultima execucao ja vista: uma execucao nova troca o painel
	// pro andamento. Nao da pra depender de ver Running, porque o core so
	// entrega a foto mais recente e um Setup rapido pode passar inteiro
	// entre duas leituras.
	lastStart time.Time
	gotState  bool
}

// Respostas das chamadas ao core (feitas fora do Update).
type (
	loadedMsg struct {
		info core.ConfigInfo
		plan bootstrap.Plan
		err  error
	}
	planMsg struct {
		plan bootstrap.Plan
		err  error
	}
	savedMsg struct {
		cfg config.Config
		err error
	}
	runDoneMsg struct{ err error }
)

func newSetupPage(api core.API, zones *zone.Manager, th theme) *setupPage {
	p := &setupPage{api: api, zones: zones, on: map[string]bool{}}
	input := func(id, label, placeholder string) {
		ti := textinput.New()
		ti.Prompt = ""
		ti.Placeholder = placeholder
		ti.SetWidth(28)
		p.items = append(p.items, &item{id: id, label: label, kind: kindInput, input: &ti})
	}
	input("domain", "Dominio", "exemplo.com.br")
	input("email", "E-mail", "voce@exemplo.com.br")
	p.items = append(p.items, &item{id: "tls", label: "Certificados", kind: kindSelect})
	for _, st := range stacks.All {
		p.items = append(p.items, &item{id: "stack:" + st.Name, label: st.Name, kind: kindCheck})
	}
	input("ghClient", "GitHub client ID", "so com woodpecker")
	input("ghAdmin", "GitHub admin", "seu usuario")
	input("ghSecret", "GitHub secret", "so pra gravar ou trocar")
	p.get("ghSecret").input.EchoMode = textinput.EchoPassword
	input("hostCI", "Host do CI", "padrao ci.<dominio>")
	input("hostReg", "Host do registry", "padrao registry.<dominio>")
	p.items = append(p.items,
		&item{id: "save", label: "Salvar", kind: kindButton},
		&item{id: "run", label: "Executar setup", kind: kindButton})
	p.setTheme(th)
	p.setFocus(0)
	return p
}

func (p *setupPage) get(id string) *item {
	for _, it := range p.items {
		if it.id == id {
			return it
		}
	}
	return nil
}

func (p *setupPage) setTheme(th theme) {
	p.th = th
	for _, it := range p.items {
		if it.input == nil {
			continue
		}
		s := textinput.DefaultStyles(true)
		for _, st := range []*textinput.StyleState{&s.Focused, &s.Blurred} {
			st.Text = lipgloss.NewStyle().Foreground(th.fieldFg).Background(th.fieldBg)
			st.Placeholder = lipgloss.NewStyle().Foreground(th.muted).Background(th.fieldBg).Italic(true)
		}
		s.Cursor.Color = th.accent
		it.input.SetStyles(s)
	}
}

// typing diz se o foco esta num campo de texto (numeros nao trocam de aba).
func (p *setupPage) typing() bool { return p.items[p.focus].kind == kindInput }

func (p *setupPage) load() tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		info, err := p.api.LoadConfig(ctx)
		if err != nil {
			return loadedMsg{err: err}
		}
		plan, err := p.api.PlanSetup(ctx)
		return loadedMsg{info: info, plan: plan, err: err}
	}
}

func (p *setupPage) planCmd() tea.Cmd {
	return func() tea.Msg {
		plan, err := p.api.PlanSetup(context.Background())
		return planMsg{plan, err}
	}
}

func (p *setupPage) onState(s core.State) {
	p.setup = s.Setup
	start := s.Setup.StartedAt
	// a primeira foto pode trazer uma execucao antiga: so marca como vista
	if p.gotState && start.After(p.lastStart) {
		p.showRun = true // Setup disparado daqui, do CLI ou do MCP
	}
	if s.Setup.Running && !p.gotState {
		p.showRun = true // abriu a TUI com um Setup em andamento
	}
	p.lastStart, p.gotState = start, true
}

func (p *setupPage) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case loadedMsg:
		if msg.err != nil {
			p.setStatus("danger", "Erro lendo a config: "+msg.err.Error())
			return nil
		}
		p.fill(msg.info.Config)
		p.plan = msg.plan
	case planMsg:
		if msg.err != nil {
			p.setStatus("danger", "Erro montando o plano: "+msg.err.Error())
			return nil
		}
		p.plan = msg.plan
	case savedMsg:
		p.showRun = false
		if msg.err != nil {
			// a validacao junta varios erros, um por linha: vao pro painel
			p.saveErr = msg.err.Error()
			p.setStatus("danger", "Nao salvou: veja o motivo ao lado.")
			return nil
		}
		p.saveErr = ""
		p.cfg = msg.cfg
		p.get("ghSecret").input.SetValue("")
		p.setStatus("ok", "Config salva.")
		return p.planCmd()
	case runDoneMsg:
		p.running = false
		if msg.err != nil {
			p.setStatus("danger", "Setup parou: "+firstLine(msg.err.Error()))
		} else {
			p.setStatus("ok", "Setup concluido.")
		}
		// a CA e os bancos aparecem no fim; o plano novo traz as instrucoes
		return p.planCmd()
	}
	return nil
}

func (p *setupPage) fill(c config.Config) {
	p.cfg = c
	p.get("domain").input.SetValue(c.Domain)
	p.get("email").input.SetValue(c.Email)
	p.tls = 0
	if !c.LetsEncrypt() {
		p.tls = 1
	}
	for _, st := range stacks.All {
		p.on[st.Name] = c.HasStack(st.Name)
	}
	p.get("ghClient").input.SetValue(c.Woodpecker.GitHubClient)
	p.get("ghAdmin").input.SetValue(c.Woodpecker.Admin)
	p.get("hostCI").input.SetValue(c.Hosts.CI)
	p.get("hostReg").input.SetValue(c.Hosts.Registry)
}

// formConfig monta a config do formulario, mantendo o que ele nao mostra
// (portas do proxy) como estava.
func (p *setupPage) formConfig() config.Config {
	c := p.cfg
	val := func(id string) string { return strings.TrimSpace(p.get(id).input.Value()) }
	c.Domain, c.Email = val("domain"), val("email")
	c.TLS = ""
	if p.tls == 1 {
		c.TLS = config.TLSSelfSigned
	}
	c.Stacks = nil
	for _, st := range stacks.All {
		if p.on[st.Name] {
			c.Stacks = append(c.Stacks, st.Name)
		}
	}
	c.Woodpecker.GitHubClient, c.Woodpecker.Admin = val("ghClient"), val("ghAdmin")
	c.Hosts.CI, c.Hosts.Registry = val("hostCI"), val("hostReg")
	return c
}

func (p *setupPage) save() tea.Cmd {
	c := p.formConfig()
	secret := p.get("ghSecret").input.Value()
	p.setStatus("warn", "Salvando...")
	return func() tea.Msg {
		ctx := context.Background()
		err := p.api.SaveConfig(ctx, c)
		if err == nil && secret != "" {
			err = p.api.SetSecret(ctx, "github_client_secret", secret)
		}
		return savedMsg{c, err}
	}
}

func (p *setupPage) canRun() bool { return !p.running && !p.setup.Running && p.plan.Ready() }

func (p *setupPage) run() tea.Cmd {
	if !p.canRun() {
		return nil
	}
	p.showRun, p.running = true, true
	p.setStatus("warn", "Executando o Setup...")
	return func() tea.Msg {
		_, err := p.api.RunSetup(context.Background())
		return runDoneMsg{err}
	}
}

func (p *setupPage) setStatus(kind, msg string) { p.statusC, p.status = kind, msg }

func (p *setupPage) setFocus(i int) tea.Cmd {
	n := len(p.items)
	p.focus = ((i % n) + n) % n
	var cmd tea.Cmd
	for j, it := range p.items {
		if it.input == nil {
			continue
		}
		if j == p.focus {
			cmd = it.input.Focus()
		} else {
			it.input.Blur()
		}
	}
	return cmd
}

// activate e o Enter, espaco ou clique num item que nao e campo de texto.
func (p *setupPage) activate(it *item) tea.Cmd {
	switch {
	case it.kind == kindSelect:
		p.tls = 1 - p.tls
	case it.kind == kindCheck:
		name := strings.TrimPrefix(it.id, "stack:")
		p.on[name] = !p.on[name]
	case it.id == "save":
		return p.save()
	case it.id == "run":
		return p.run()
	}
	return nil
}

func (p *setupPage) click(msg tea.MouseMsg) tea.Cmd {
	for i, it := range p.items {
		if p.zones.Get(it.id).InBounds(msg) {
			cmd := p.setFocus(i)
			if it.kind != kindInput {
				return tea.Batch(cmd, p.activate(it))
			}
			return cmd
		}
	}
	return nil
}

func (p *setupPage) key(msg tea.KeyPressMsg) tea.Cmd {
	k := msg.String()
	it := p.items[p.focus]
	switch k {
	case "ctrl+s":
		return p.save()
	case "ctrl+r":
		return p.run()
	case "tab", "down":
		return p.setFocus(p.focus + 1)
	case "shift+tab", "up":
		return p.setFocus(p.focus - 1)
	case "enter", "space":
		if it.kind != kindInput {
			return p.activate(it)
		}
		if k == "enter" {
			return p.setFocus(p.focus + 1)
		}
	case "left", "right":
		if it.kind == kindSelect {
			return p.activate(it)
		}
	}
	if it.kind == kindInput {
		ti, cmd := it.input.Update(msg)
		*it.input = ti
		return cmd
	}
	return nil
}

func (p *setupPage) view(w, h int) string {
	t := p.th
	status := ""
	if p.status != "" {
		c := map[string]color.Color{"ok": t.ok, "warn": t.warn, "danger": t.danger}[p.statusC]
		status = lipgloss.NewStyle().Bold(true).Foreground(c).Width(formWidth).Render(p.status)
	}
	left := t.panel(true, formWidth+4, h-2).Render(t.bold().Render("Configuracao") + "\n\n" + p.viewForm() + "\n\n" + status)

	rightW := max(w-lipgloss.Width(left)-1, 20)
	title, content := "Plano", p.viewPlan(rightW-4)
	if p.showRun {
		title, content = "Execucao", p.viewRun(rightW-4)
	}
	right := t.panel(false, rightW, h-2).Render(t.bold().Render(title) + "\n" + content)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)
}

func (p *setupPage) viewForm() string {
	t := p.th
	field := lipgloss.NewStyle().Background(t.fieldBg).Foreground(t.fieldFg).Width(30)
	var rows, buttons []string
	for i, it := range p.items {
		focused := i == p.focus
		marker, label := "  ", t.fg(t.text).Width(18).Render(it.label)
		if focused {
			marker = t.fg(t.accent).Bold(true).Render("› ")
			label = t.fg(t.accent).Bold(true).Width(18).Render(it.label)
		}

		var value string
		switch it.kind {
		case kindInput:
			value = field.Render(it.input.View())
		case kindSelect:
			value = field.Render("‹ " + []string{"Let's Encrypt", "CA local (teste)"}[p.tls] + " ›")
		case kindCheck:
			value = t.fg(t.muted).Render("[ ]")
			if p.on[strings.TrimPrefix(it.id, "stack:")] {
				value = t.fg(t.accent).Bold(true).Render("[x]")
			}
		case kindButton:
			st := t.pill(focused)
			if it.id == "run" && !p.canRun() {
				st = t.disabledPill()
			}
			buttons = append(buttons, p.zones.Mark(it.id, st.Render(it.label)))
			continue
		}
		rows = append(rows, p.zones.Mark(it.id, marker+label+value))
	}
	return strings.Join(rows, "\n") + "\n\n  " + strings.Join(buttons, "  ")
}

func (p *setupPage) viewPlan(w int) string {
	t, pl := p.th, p.plan
	muted := t.fg(t.muted)
	wrap := lipgloss.NewStyle().Width(w)
	var b strings.Builder

	if p.saveErr != "" {
		b.WriteString("\n" + t.fg(t.danger).Bold(true).Render("Nao salvou") + "\n")
		for _, l := range strings.Split(p.saveErr, "\n") {
			b.WriteString(wrap.Foreground(t.danger).Render("  "+l) + "\n")
		}
	}
	if !pl.Configured {
		b.WriteString("\n" + t.fg(t.warn).Bold(true).Render("Ainda sem configuracao.") + "\n\n")
		b.WriteString(wrap.Render("Preencha dominio e e-mail, escolha as stacks e salve (ctrl+s).") + "\n")
		return b.String()
	}

	b.WriteString("\n" + t.bold().Render("Stacks") + "\n")
	for _, s := range pl.Stacks {
		c := map[string]color.Color{bootstrap.ChangeNew: t.ok, bootstrap.ChangeModified: t.warn}[s.Change]
		if c == nil {
			c = t.muted
		}
		b.WriteString(fmt.Sprintf("  %-11s %s %s\n", s.Name, t.fg(c).Render(fmt.Sprintf("%-9s", s.Change)), muted.Render(s.Desc)))
	}
	if len(pl.Disable) > 0 {
		b.WriteString("\n" + t.bold().Render("Desativadas") + " " + muted.Render("(derrubadas no Setup; os volumes ficam)") + "\n")
		for _, s := range pl.Disable {
			b.WriteString(fmt.Sprintf("  %-11s %s\n", s.Name, muted.Render(s.Desc)))
		}
	}
	if len(pl.CreateSecrets) > 0 {
		b.WriteString("\n" + t.bold().Render("Secrets a gerar") + "\n" + wrap.Render("  "+strings.Join(pl.CreateSecrets, ", ")) + "\n")
	}
	for _, pr := range pl.Problems {
		b.WriteString("\n" + wrap.Foreground(t.danger).Render(pr) + "\n")
	}
	if len(pl.Missing) > 0 {
		b.WriteString("\n" + t.fg(t.danger).Bold(true).Render("Falta informar") + "\n")
		for _, x := range pl.Missing {
			b.WriteString(wrap.Render("  "+t.fg(t.danger).Render(x.Name)+": "+x.Desc) + "\n")
		}
	}
	if pl.Ready() {
		b.WriteString("\n" + t.fg(t.ok).Bold(true).Render("Pronto pra executar") + muted.Render(" (ctrl+r)"))
		if pl.CAFile == "" {
			b.WriteString(muted.Render(" -- antes, aponte o DNS dos nomes pra esta VPS"))
		}
		b.WriteString("\n")
	}
	b.WriteString(p.viewAccess(w))
	return b.String()
}

func (p *setupPage) viewRun(w int) string {
	t, s := p.th, p.setup
	var b strings.Builder
	b.WriteString("\n")
	if len(s.Steps) == 0 {
		return b.String() + t.fg(t.warn).Render("Comecando...")
	}
	for _, st := range s.Steps {
		mark := map[bootstrap.Status]string{
			bootstrap.Done:    t.fg(t.ok).Render("✓"),
			bootstrap.Running: t.fg(t.warn).Render("◐"),
			bootstrap.Failed:  t.fg(t.danger).Render("✗"),
		}[st.Status]
		if mark == "" {
			mark = t.fg(t.muted).Render("·")
		}
		line := mark + " " + st.Desc
		if st.Detail != "" && st.Status != bootstrap.Failed {
			line += t.fg(t.muted).Render(" — " + st.Detail)
		}
		b.WriteString(lipgloss.NewStyle().Width(w).Render(line) + "\n")
	}
	switch {
	case s.Running || p.running:
		b.WriteString("\n" + t.fg(t.warn).Render("Executando... pode sair: o Setup continua no daemon.") + "\n")
	case s.Error != "":
		b.WriteString("\n" + lipgloss.NewStyle().Width(w).Foreground(t.danger).Render("Parou: "+s.Error) + "\n")
		b.WriteString("\n" + t.fg(t.muted).Render("Corrija e execute de novo: nada que ja existe e refeito.") + "\n")
	default:
		b.WriteString("\n" + t.fg(t.ok).Bold(true).Render("Setup concluido.") + "\n")
		b.WriteString(p.viewAccess(w))
	}
	return b.String()
}

// viewAccess mostra como chegar nos bancos e, no modo de teste, como
// confiar na CA local. Comandos na cor normal: sao pra copiar.
func (p *setupPage) viewAccess(w int) string {
	t, pl := p.th, p.plan
	muted := t.fg(t.muted)
	wrap := lipgloss.NewStyle().Width(w)
	var b strings.Builder
	if len(pl.Databases) > 0 {
		b.WriteString("\n" + t.bold().Render("Bancos") + " " + muted.Render("(so por tunel SSH)") + "\n")
		b.WriteString(wrap.Foreground(t.accent).Render("  "+pl.Tunnel) + "\n")
		for _, d := range pl.Databases {
			b.WriteString(wrap.Render(fmt.Sprintf("  %-8s localhost:%-5d usuario %-8s %s", d.Name, d.Port, d.User,
				muted.Render("senha: sudo cat "+d.PasswordFile))) + "\n")
		}
	}
	if pl.CAFile != "" {
		b.WriteString("\n" + t.bold().Render("CA local") + " " + muted.Render("(confie nela nesta maquina)") + "\n")
		if pl.Config.HasStack("registry") {
			dir := "/etc/docker/certs.d/" + pl.Config.RegistryHost()
			b.WriteString(wrap.Render("  sudo mkdir -p "+dir) + "\n")
			b.WriteString(wrap.Render("  sudo cp "+pl.CAFile+" "+dir+"/ca.crt") + "\n")
		}
		b.WriteString(wrap.Render("  sudo trust anchor --store "+pl.CAFile) + "\n")
	}
	return b.String()
}
