package ui

import (
	"context"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/trdSystemDev/cais/internal/bootstrap"
	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/github"
)

const formWidth = 50

// item e cada coisa focavel do formulario, na ordem do Tab.
type item struct {
	id      string
	label   string
	kind    int
	input   *textinput.Model
	section int // indice em setupSections
}

// setupSections separam o formulario: o que cada grupo de campos configura.
var setupSections = []struct{ title, hint string }{
	{"Geral", "cada app traz o proprio dominio no compose"},
	{"GitHub App do cais", "baixa o codigo dos repositorios privados no deploy"},
	{"Avisos por e-mail", "pela Brevo: deploy automatico ou backup que falhou"},
	{"Hosts", "S3: obrigatorio com o Garage ligado"},
}

const (
	kindInput = iota
	kindSelect
	kindButton
)

// setupPage e a aba Setup: as configuracoes da VPS (dominio, certificados,
// GitHub, nomes) a esquerda e, a direita, o plano ou o andamento. Quais
// servicos ligar (bancos, storage) fica na aba Infra. So
// chama acoes do core; o progresso chega pelo estado, entao um Setup
// disparado pelo CLI tambem aparece aqui.
type setupPage struct {
	api   core.API
	zones *zone.Manager
	th    theme

	items []*item
	focus int
	tls   int // 0 Let's Encrypt, 1 CA local

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
		cfg  config.Config
		err  error
		keys []string // chaves gravadas junto (GitHub App, Brevo)
	}
	runDoneMsg    struct{ err error }
	notifyTestMsg struct{ err error }
)

func newSetupPage(api core.API, zones *zone.Manager, th theme) *setupPage {
	p := &setupPage{api: api, zones: zones}
	section := 0
	input := func(id, label, placeholder string) {
		ti := textinput.New()
		ti.Prompt = ""
		ti.Placeholder = placeholder
		ti.SetWidth(28)
		p.items = append(p.items, &item{id: id, label: label, kind: kindInput, input: &ti, section: section})
	}
	input("email", "E-mail", "avisos do Let's Encrypt")
	p.items = append(p.items, &item{id: "tls", label: "Certificados", kind: kindSelect})
	// so no modo de teste (CA local): os nomes que ela assina
	input("testDomain", "Dominio (teste)", "ex: trd.localhost")

	section = 1
	input("ghAppID", "App ID", "numero na pagina do app")
	input("ghKey", "Chave privada", "cole o .pem ou o caminho")
	p.get("ghKey").input.EchoMode = textinput.EchoPassword
	p.get("ghKey").input.CharLimit = 16384 // o .pem inteiro colado

	section = 2
	input("notifyFrom", "Remetente", "validado na Brevo")
	input("notifyTo", "Destinatarios", "a@x.com, b@x.com")
	p.get("notifyTo").input.CharLimit = 1024
	input("brevoKey", "Chave da Brevo", "xkeysib-...")
	p.get("brevoKey").input.EchoMode = textinput.EchoPassword

	section = 3
	input("hostS3", "S3", "")
	p.items = append(p.items,
		&item{id: "save", label: "Salvar", kind: kindButton, section: section},
		&item{id: "run", label: "Executar setup", kind: kindButton, section: section},
		&item{id: "notifyTest", label: "Testar e-mail", kind: kindButton, section: section})
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
		th.styleInput(it.input)
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
		p.get("ghKey").input.SetValue("")
		p.get("brevoKey").input.SetValue("")
		if len(msg.keys) > 0 {
			p.setStatus("ok", "Config salva e "+strings.Join(msg.keys, " e ")+" gravada.")
		} else {
			p.setStatus("ok", "Config salva.")
		}
		return p.planCmd()
	case notifyTestMsg:
		if msg.err != nil {
			p.setStatus("danger", "E-mail de teste nao saiu: "+firstLine(msg.err.Error()))
		} else {
			p.setStatus("ok", "E-mail de teste enviado.")
		}
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
	p.get("testDomain").input.SetValue(c.TestDomain)
	p.get("email").input.SetValue(c.Email)
	p.tls = 0
	if !c.LetsEncrypt() {
		p.tls = 1
	}
	p.get("ghAppID").input.SetValue("")
	if c.GitHub.AppID > 0 {
		p.get("ghAppID").input.SetValue(strconv.FormatInt(c.GitHub.AppID, 10))
	}
	p.get("hostS3").input.SetValue(c.Hosts.S3)
	p.get("notifyFrom").input.SetValue(c.Notify.From)
	p.get("notifyTo").input.SetValue(strings.Join(c.Notify.To, ", "))
}

// formConfig monta a config do formulario, mantendo o que ele nao mostra
// (portas do proxy, servicos ligados na aba Infra) como estava.
func (p *setupPage) formConfig() config.Config {
	c := p.cfg
	val := func(id string) string { return strings.TrimSpace(p.get(id).input.Value()) }
	c.Email = val("email")
	c.TLS, c.TestDomain = "", ""
	if p.tls == 1 {
		c.TLS, c.TestDomain = config.TLSSelfSigned, val("testDomain")
	}
	c.GitHub.AppID = 0
	if id, err := strconv.ParseInt(val("ghAppID"), 10, 64); err == nil {
		c.GitHub.AppID = id
	} else if val("ghAppID") != "" {
		c.GitHub.AppID = -1 // a validacao da config explica
	}
	c.Hosts.S3 = val("hostS3")
	c.Notify.From = val("notifyFrom")
	c.Notify.To = nil
	for _, to := range strings.FieldsFunc(val("notifyTo"), func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
		c.Notify.To = append(c.Notify.To, to)
	}
	return c
}

func (p *setupPage) save() tea.Cmd {
	c := p.formConfig()
	var key []byte
	if text := strings.TrimSpace(p.get("ghKey").input.Value()); text != "" {
		var err error
		if key, err = readGitHubKey(text); err != nil {
			p.saveErr = err.Error()
			p.setStatus("danger", "Nao salvou: veja o motivo ao lado.")
			return nil
		}
	}
	brevo := strings.TrimSpace(p.get("brevoKey").input.Value())
	p.setStatus("warn", "Salvando...")
	return func() tea.Msg {
		ctx := context.Background()
		var keys []string
		err := p.api.SaveConfig(ctx, c)
		if err == nil && key != nil {
			if err = p.api.SetSecret(ctx, "github_app_key", string(key)); err == nil {
				keys = append(keys, "chave do GitHub App")
			}
		}
		if err == nil && brevo != "" {
			if err = p.api.SetSecret(ctx, "brevo_api_key", brevo); err == nil {
				keys = append(keys, "chave da Brevo")
			}
		}
		return savedMsg{c, err, keys}
	}
}

// notifyTest manda o e-mail de teste com o que esta salvo (nao o que esta
// no formulario sem salvar).
func (p *setupPage) notifyTest() tea.Cmd {
	p.setStatus("warn", "Mandando o e-mail de teste...")
	return func() tea.Msg {
		return notifyTestMsg{p.api.TestNotify(context.Background())}
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

// hidden diz se o item esta fora do formulario agora: o dominio do teste
// so existe com a CA local.
func (p *setupPage) hidden(it *item) bool { return it.id == "testDomain" && p.tls == 0 }

func (p *setupPage) setFocus(i int) tea.Cmd {
	n := len(p.items)
	step := 1
	if i < p.focus {
		step = -1
	}
	p.focus = ((i % n) + n) % n
	for p.hidden(p.items[p.focus]) {
		p.focus = ((p.focus+step)%n + n) % n
	}
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
	case it.id == "save":
		return p.save()
	case it.id == "run":
		return p.run()
	case it.id == "notifyTest":
		return p.notifyTest()
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

	// o padrao dos hosts aparece no campo vazio, ja com o dominio digitado
	p.get("hostS3").input.Placeholder = "ex: s3.exemplo.com.br"

	var rows, buttons []string
	last := -1
	for i, it := range p.items {
		if p.hidden(it) {
			continue
		}
		if it.kind == kindButton {
			k := btnNormal
			if it.id == "run" && !p.canRun() {
				k = btnOff
			}
			if len(buttons) > 0 {
				buttons = append(buttons, " ")
			}
			buttons = append(buttons, p.zones.Mark(it.id, t.button(it.label, k, i == p.focus)))
			continue
		}
		if it.section != last {
			sec := setupSections[it.section]
			if last >= 0 {
				rows = append(rows, "")
			}
			rows = append(rows, t.bold().Render(sec.title), t.fg(t.muted).Width(formWidth).Render(sec.hint))
			last = it.section
		}
		focused := i == p.focus
		marker, label := "  ", t.fg(t.text).Width(16).Render(it.label)
		if focused {
			marker = t.fg(t.accent).Bold(true).Render("› ")
			label = t.fg(t.accent).Bold(true).Width(16).Render(it.label)
		}
		var value string
		switch it.kind {
		case kindInput:
			value = t.input(it.input, 30)
		case kindSelect:
			value = field.Render("‹ " + []string{"Let's Encrypt", "CA local (teste)"}[p.tls] + " ›")
		}
		rows = append(rows, p.zones.Mark(it.id, marker+label+value))
	}
	return strings.Join(rows, "\n") + "\n\n" + lipgloss.JoinHorizontal(lipgloss.Top, append([]string{"  "}, buttons...)...)
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
		b.WriteString(wrap.Render("Preencha o e-mail (avisos do Let's Encrypt) e o GitHub App e salve (ctrl+s). "+
			"Nao ha dominio da VPS: cada app traz o proprio dominio no compose, e o certificado sai pra ele. "+
			"Bancos e storage voce liga depois, na aba Infra.") + "\n")
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
		b.WriteString(wrap.Render("  sudo trust anchor --store "+pl.CAFile) + "\n")
	}
	return b.String()
}

// readGitHubKey aceita o .pem colado no campo (as quebras de linha viram
// espaco e sao refeitas) ou o caminho do arquivo nesta maquina.
func readGitHubKey(text string) ([]byte, error) {
	if !strings.Contains(text, "-----BEGIN") {
		path := text
		if strings.HasPrefix(path, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, err
			}
			path = filepath.Join(home, path[2:])
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("chave do GitHub App: %w (cole o conteudo do .pem ou o caminho do arquivo)", err)
		}
		text = string(data)
	}
	return github.NormalizeKey(text)
}

// paste entrega o texto colado ao campo de texto com foco.
func (p *setupPage) paste(msg tea.PasteMsg) tea.Cmd {
	it := p.items[p.focus]
	if it.kind != kindInput {
		return nil
	}
	ti, cmd := it.input.Update(msg)
	*it.input = ti
	return cmd
}
