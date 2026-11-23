package ui

import (
	"context"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/trdSystemDev/cais/internal/bootstrap"
	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/github"
)

// item e cada coisa focavel do formulario, na ordem do Tab.
type item struct {
	id      string
	label   string
	kind    int
	input   *textinput.Model
	section int // indice em setupSections; -1 aparece em todo formulario
}

// setupSections sao as sub-abas do Setup, cada uma em tela cheia. As duas
// primeiras sao partes do mesmo formulario (um Salvar grava tudo).
var setupSections = []struct{ title, hint string }{
	{"Geral", "O GitHub App do cais. A config de cada servico (certificados e portas do proxy, endereco do S3) fica no card dele, na aba Infra."},
	{"Avisos", "E-mail pela Brevo quando um deploy automatico ou um backup falha."},
	{"Backups", ""},
}

const (
	secGeral = iota
	secAvisos
	secBackups
)

const (
	kindInput = iota
	kindButton
)

// setupPage e a aba Setup: sub-abas em tela cheia com as configuracoes da
// VPS (Geral, Avisos) e os Backups (bkPanel).
// Quais servicos ligar, e as portas do proxy, ficam na aba Infra. So chama
// acoes do core; o progresso chega pelo estado, entao um Setup disparado
// pelo CLI tambem aparece aqui.
type setupPage struct {
	api   core.API
	zones *zone.Manager
	th    theme

	items  []*item
	head   []*item            // itens antes da lista de destinatarios
	tail   []*item            // Salvar e Testar e-mail, depois dela
	to     []*textinput.Model // destinatarios dos avisos, um por linha
	focus  int
	sec    int  // sub-aba aberta
	inForm bool // o teclado esta nos campos (senao, nas sub-abas)

	cfg     config.Config // ultima lida: base do que o form nao mostra
	plan    bootstrap.Plan
	status  string
	statusC string // ok, warn, danger
	saveErr string // por que o ultimo Salvar falhou (varias linhas)
	ghMsg   string // resultado do Testar do GitHub App
	ghMsgC  string
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
	notifyTestMsg struct{ err error }
	githubTestMsg struct {
		res github.Check
		err error
	}
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

	input("ghAppID", "App ID", "numero na pagina do app")
	input("ghKey", "Chave privada", "cole o .pem ou o caminho")
	p.get("ghKey").input.EchoMode = textinput.EchoPassword
	p.get("ghKey").input.CharLimit = 16384 // o .pem inteiro colado
	p.items = append(p.items, &item{id: "ghTest", label: "Testar", kind: kindButton, section: section})

	section = secAvisos
	input("notifyFrom", "Remetente", "validado na Brevo")
	input("brevoKey", "Chave da Brevo", "xkeysib-...")
	p.get("brevoKey").input.EchoMode = textinput.EchoPassword

	p.head = p.items
	p.tail = []*item{
		{id: "save", label: "Salvar", kind: kindButton, section: -1},
		{id: "notifyTest", label: "Testar e-mail", kind: kindButton, section: secAvisos}}
	p.th = th
	p.rebuild()
	p.setTheme(th)
	return p
}

// newTo e o campo de um destinatario.
func (p *setupPage) newTo(addr string) *textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Placeholder = "e-mail"
	ti.SetWidth(40)
	ti.SetValue(addr)
	p.th.styleInput(&ti)
	return &ti
}

// rebuild remonta os itens com as linhas de destinatarios (cada uma com o
// excluir) e o "+ Novo e-mail", mantendo o foco no mesmo item.
func (p *setupPage) rebuild() {
	cur := ""
	if p.focus < len(p.items) {
		cur = p.items[p.focus].id
	}
	items := slices.Clone(p.head)
	for i, ti := range p.to {
		items = append(items,
			&item{id: fmt.Sprintf("to:%d", i), kind: kindInput, input: ti, section: secAvisos},
			&item{id: fmt.Sprintf("to-del:%d", i), label: "✕ excluir", kind: kindButton, section: secAvisos})
	}
	items = append(items, &item{id: "to-add", label: "+ Novo e-mail", kind: kindButton, section: secAvisos})
	p.items = append(items, p.tail...)
	p.focus = 0
	for i, it := range p.items {
		if it.id == cur {
			p.focus = i
		}
	}
}

// index e a posicao do item id (-1: nao existe).
func (p *setupPage) index(id string) int {
	return slices.IndexFunc(p.items, func(it *item) bool { return it.id == id })
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
func (p *setupPage) typing() bool {
	return p.inForm && p.sec < secBackups && p.items[p.focus].kind == kindInput
}

// blur tira o foco do formulario (volta pras sub-abas).
func (p *setupPage) blur() {
	p.inForm = false
	for _, it := range p.items {
		if it.input != nil {
			it.input.Blur()
		}
	}
}

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
		if msg.err != nil {
			// a validacao junta varios erros, um por linha: vao pro painel
			p.saveErr = msg.err.Error()
			p.setStatus("danger", "Nao salvou: veja o motivo abaixo.")
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
	case githubTestMsg:
		switch {
		case msg.err != nil:
			p.ghMsgC, p.ghMsg = "danger", firstLine(msg.err.Error())
		case len(msg.res.Installations) == 0:
			p.ghMsgC, p.ghMsg = "warn", "Conectou no app \""+msg.res.Name+"\", mas ele nao esta instalado em nenhuma conta: "+
				"instale na pagina do app (Install App) e libere os repositorios."
		default:
			p.ghMsgC, p.ghMsg = "ok", "Conectou no app \""+msg.res.Name+"\", instalado em: "+strings.Join(msg.res.Installations, ", ")+"."
		}
	case notifyTestMsg:
		if msg.err != nil {
			p.setStatus("danger", "E-mail de teste nao saiu: "+firstLine(msg.err.Error()))
		} else {
			p.setStatus("ok", "E-mail de teste enviado.")
		}
	}
	return nil
}

func (p *setupPage) fill(c config.Config) {
	p.cfg = c
	p.get("ghAppID").input.SetValue("")
	if c.GitHub.AppID > 0 {
		p.get("ghAppID").input.SetValue(strconv.FormatInt(c.GitHub.AppID, 10))
	}
	p.get("notifyFrom").input.SetValue(c.Notify.From)
	p.to = nil
	for _, addr := range c.Notify.To {
		p.to = append(p.to, p.newTo(addr))
	}
	p.rebuild()
}

// formApply le o formulario agora e devolve o que ele muda numa config: so
// os campos do formulario. O resto (os servicos e a config deles, na Infra;
// backup, no painel ao lado) fica como estiver na config na hora de gravar.
func (p *setupPage) formApply() func(*config.Config) {
	val := func(id string) string { return strings.TrimSpace(p.get(id).input.Value()) }
	appID, from := val("ghAppID"), val("notifyFrom")
	var to []string
	for _, ti := range p.to {
		if addr := strings.TrimSpace(ti.Value()); addr != "" {
			to = append(to, addr)
		}
	}
	return func(c *config.Config) {
		c.GitHub.AppID = 0
		if id, err := strconv.ParseInt(appID, 10, 64); err == nil {
			c.GitHub.AppID = id
		} else if appID != "" {
			c.GitHub.AppID = -1 // a validacao da config explica
		}
		c.Notify.From, c.Notify.To = from, to
	}
}

func (p *setupPage) save() tea.Cmd {
	apply := p.formApply()
	base := p.cfg
	var key []byte
	if text := strings.TrimSpace(p.get("ghKey").input.Value()); text != "" {
		var err error
		if key, err = readGitHubKey(text); err != nil {
			p.saveErr = err.Error()
			p.setStatus("danger", "Nao salvou: veja o motivo abaixo.")
			return nil
		}
	}
	brevo := strings.TrimSpace(p.get("brevoKey").input.Value())
	p.setStatus("warn", "Salvando...")
	return func() tea.Msg {
		ctx := context.Background()
		var keys []string
		// a config de agora, nao a de quando a aba abriu: um servico ligado
		// na Infra depois disso nao pode ser desligado por este Salvar
		c := base
		if info, err := p.api.LoadConfig(ctx); err == nil && info.Exists {
			c = info.Config
		}
		apply(&c)
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

// githubTest confere o GitHub App com o que esta no formulario: o App ID
// digitado e a chave colada (ou, com o campo vazio, a ja gravada).
func (p *setupPage) githubTest() tea.Cmd {
	var id int64
	if v := strings.TrimSpace(p.get("ghAppID").input.Value()); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			p.ghMsgC, p.ghMsg = "danger", "App ID invalido: e o numero na pagina do app no GitHub."
			return nil
		}
		id = n
	}
	var key string
	if text := strings.TrimSpace(p.get("ghKey").input.Value()); text != "" {
		k, err := readGitHubKey(text)
		if err != nil {
			p.ghMsgC, p.ghMsg = "danger", firstLine(err.Error())
			return nil
		}
		key = string(k)
	}
	p.ghMsgC, p.ghMsg = "warn", "Testando no GitHub..."
	return func() tea.Msg {
		res, err := p.api.TestGitHub(context.Background(), id, key)
		return githubTestMsg{res, err}
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

func (p *setupPage) setStatus(kind, msg string) { p.statusC, p.status = kind, msg }

// visible diz se o item esta na sub-aba aberta (o Salvar em todo
// formulario).
func (p *setupPage) visible(it *item) bool {
	return it.section == p.sec || (it.section < 0 && p.sec < secBackups)
}

// shown sao os indices dos itens visiveis, na ordem do Tab.
func (p *setupPage) shown() []int {
	var idx []int
	for i, it := range p.items {
		if p.visible(it) {
			idx = append(idx, i)
		}
	}
	return idx
}

// focusAt leva o teclado pro item i.
func (p *setupPage) focusAt(i int) tea.Cmd {
	p.inForm = true
	p.focus = i
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

// enter entra no formulario da sub-aba, no primeiro (ou ultimo) item.
func (p *setupPage) enter(last bool) tea.Cmd {
	idx := p.shown()
	if len(idx) == 0 {
		return nil
	}
	if last {
		return p.focusAt(idx[len(idx)-1])
	}
	return p.focusAt(idx[0])
}

// move anda d itens na sub-aba, dando a volta.
func (p *setupPage) move(d int) tea.Cmd {
	idx := p.shown()
	if len(idx) == 0 {
		return nil
	}
	pos := max(0, slices.Index(idx, p.focus))
	return p.focusAt(idx[((pos+d)%len(idx)+len(idx))%len(idx)])
}

// activate e o Enter, espaco ou clique num item que nao e campo de texto.
func (p *setupPage) activate(it *item) tea.Cmd {
	switch {
	case it.id == "save":
		return p.save()
	case it.id == "notifyTest":
		return p.notifyTest()
	case it.id == "ghTest":
		return p.githubTest()
	case it.id == "to-add":
		p.to = append(p.to, p.newTo(""))
		p.rebuild()
		return p.focusAt(p.index(fmt.Sprintf("to:%d", len(p.to)-1)))
	case strings.HasPrefix(it.id, "to-del:"):
		i, _ := strconv.Atoi(strings.TrimPrefix(it.id, "to-del:"))
		p.to = slices.Delete(p.to, i, i+1)
		p.rebuild()
		// o foco fica no excluir da linha que subiu (ou no "+ Novo e-mail")
		next := p.index(fmt.Sprintf("to-del:%d", min(i, len(p.to)-1)))
		if next < 0 {
			next = p.index("to-add")
		}
		return p.focusAt(next)
	}
	return nil
}

// click no formulario; ok diz se acertou algum item.
func (p *setupPage) click(msg tea.MouseMsg) (tea.Cmd, bool) {
	for i, it := range p.items {
		if p.visible(it) && p.zones.Get(it.id).InBounds(msg) {
			cmd := p.focusAt(i)
			if it.kind != kindInput {
				return tea.Batch(cmd, p.activate(it)), true
			}
			return cmd, true
		}
	}
	return nil, false
}

// setSetupSec abre a sub-aba i, com o teclado nas sub-abas.
func (m *model) setSetupSec(i int) tea.Cmd {
	m.setup.blur()
	m.bk.deactivate()
	m.setup.sec = i
	if i == secBackups {
		m.bk.fill(m.state.Backup)
		return m.backupRefreshCmd()
	}
	return nil
}

// setupClick: as sub-abas e o que esta na aberta.
func (m *model) setupClick(msg tea.MouseMsg) tea.Cmd {
	if m.setup.sec == secBackups && m.bk.info != nil {
		m.bk.info = nil // qualquer clique fecha a ajuda
		return nil
	}
	for i, s := range setupSections {
		if m.zones.Get("setup-sec:" + s.title).InBounds(msg) {
			return m.setSetupSec(i)
		}
	}
	switch m.setup.sec {
	case secBackups:
		cmd, _ := m.bk.click(m.state.Backup, msg)
		return cmd
	}
	cmd, _ := m.setup.click(msg)
	return cmd
}

// setupKey: fora dos campos, ←→ trocam de sub-aba e enter/tab entram nela;
// dentro, o Tab percorre so a sub-aba e o esc volta.
func (m *model) setupKey(msg tea.KeyPressMsg) tea.Cmd {
	k, st, p := msg.String(), m.state.Backup, m.setup
	if p.sec == secBackups && m.bk.info != nil {
		switch k {
		case "esc", "enter", "space", "q":
			m.bk.info = nil
		}
		return nil
	}
	if k == "ctrl+s" && p.sec < secBackups {
		return p.save()
	}
	switch {
	case p.sec == secBackups && m.bk.active:
		return m.bk.key(st, msg)
	case p.sec < secBackups && p.inForm:
		return p.key(msg)
	}
	n := len(setupSections)
	switch k {
	case "left", "h":
		return m.setSetupSec((p.sec + n - 1) % n)
	case "right", "l":
		return m.setSetupSec((p.sec + 1) % n)
	case "enter", "space", "tab", "down", "shift+tab", "up":
		last := k == "shift+tab" || k == "up"
		switch p.sec {
		case secBackups:
			m.bk.active = true
			m.bk.fill(st)
			if last {
				return m.bk.setFocus(st, -1)
			}
			return m.bk.setFocus(st, 0)
		}
		return p.enter(last)
	}
	return nil
}

func (p *setupPage) key(msg tea.KeyPressMsg) tea.Cmd {
	k := msg.String()
	it := p.items[p.focus]
	switch k {
	case "esc":
		p.blur()
		return nil
	case "ctrl+s":
		return p.save()
	case "tab", "down":
		return p.move(1)
	case "shift+tab", "up":
		return p.move(-1)
	case "enter", "space":
		if it.kind != kindInput {
			return p.activate(it)
		}
		if k == "enter" {
			return p.move(1)
		}
	}
	if it.kind == kindInput {
		ti, cmd := it.input.Update(msg)
		*it.input = ti
		return cmd
	}
	return nil
}

// viewSetup: a sub-aba aberta em tela cheia, com as sub-abas no topo do
// painel, como as secoes de um app na aba Apps.
func (m *model) viewSetup(h int) string {
	p, t := m.setup, m.th
	var parts []string
	for i, s := range setupSections {
		parts = append(parts, m.zones.Mark("setup-sec:"+s.title, t.pill(i == p.sec).Render(s.title)), " ")
	}
	pills := lipgloss.JoinHorizontal(lipgloss.Top, parts...)
	bodyH := max(1, h-2)
	if p.sec == secBackups {
		return m.bk.view(m.state.Backup, m.w, bodyH, pills)
	}
	return p.view(m.w, bodyH, pills)
}

func (p *setupPage) view(w, h int, pills string) string {
	t := p.th
	inner := max(20, w-6)
	wrap := lipgloss.NewStyle().Width(inner)
	var b strings.Builder
	b.WriteString(pills + "\n\n" + wrap.Inherit(t.fg(t.muted)).Render(setupSections[p.sec].hint) + "\n")
	b.WriteString("\n" + p.viewForm(inner))
	if p.status != "" {
		c := map[string]color.Color{"ok": t.ok, "warn": t.warn, "danger": t.danger}[p.statusC]
		b.WriteString("\n\n" + wrap.Bold(true).Foreground(c).Render(p.status))
	}
	if p.saveErr != "" {
		b.WriteString("\n")
		for _, l := range strings.Split(p.saveErr, "\n") {
			b.WriteString("\n" + wrap.Foreground(t.danger).Render("  "+l))
		}
	}
	if p.sec == secGeral {
		b.WriteString("\n" + p.viewPlan(inner))
	}
	return t.panel(p.inForm, w, h).MaxHeight(h + 2).Render(b.String())
}

// viewForm desenha os campos da sub-aba (no Geral, o GitHub App com o
// Testar; nos Avisos, a lista de destinatarios).
func (p *setupPage) viewForm(w int) string {
	t := p.th
	var rows, buttons []string
	for i, it := range p.items {
		if !p.visible(it) {
			continue
		}
		focused := i == p.focus && p.inForm
		if it.id == "to-add" || strings.HasPrefix(it.id, "to:") || strings.HasPrefix(it.id, "to-del:") {
			rows = append(rows, p.viewTo(it, i, w)...)
			continue
		}
		if it.id == "ghTest" {
			// o Testar fica na secao do GitHub App, com o resultado embaixo
			rows = append(rows, "  "+p.zones.Mark(it.id, t.button(it.label, btnInfo, focused)))
			if p.ghMsg != "" {
				c := map[string]color.Color{"ok": t.ok, "warn": t.warn, "danger": t.danger}[p.ghMsgC]
				rows = append(rows, lipgloss.NewStyle().Width(w).Foreground(c).Render("  "+p.ghMsg))
			}
			continue
		}
		if it.kind == kindButton {
			if len(buttons) > 0 {
				buttons = append(buttons, " ")
			}
			buttons = append(buttons, p.zones.Mark(it.id, t.button(it.label, btnNormal, focused)))
			continue
		}
		if it.id == "ghAppID" {
			rows = append(rows, t.bold().Render("GitHub App"),
				t.fg(t.muted).Width(w).Render("baixa o codigo dos repositorios privados no deploy"))
		}
		marker, label := "  ", t.fg(t.text).Width(16).Render(it.label)
		if focused {
			marker = t.fg(t.accent).Bold(true).Render("› ")
			label = t.fg(t.accent).Bold(true).Width(16).Render(it.label)
		}
		rows = append(rows, p.zones.Mark(it.id, marker+label+t.input(it.input, 30)))
	}
	return strings.Join(rows, "\n") + "\n\n" + lipgloss.JoinHorizontal(lipgloss.Top, append([]string{"  "}, buttons...)...)
}

// viewTo desenha a lista de destinatarios, como as Variaveis de um app: um
// e-mail por linha com o [✕ excluir] ao lado e o "+ Novo e-mail" no fim.
func (p *setupPage) viewTo(it *item, i, w int) []string {
	t := p.th
	focusID := ""
	if p.inForm {
		focusID = p.items[p.focus].id
	}
	var rows []string
	// o titulo vem antes da primeira linha (ou do "+ Novo e-mail", sem linhas)
	if it.id == "to:0" || (it.id == "to-add" && len(p.to) == 0) {
		rows = append(rows, "", t.rule(w), t.bold().Render("Destinatarios"),
			t.fg(t.muted).Width(w).Render("recebem os avisos; sem nenhum, os avisos ficam desligados"))
		if len(p.to) == 0 {
			rows = append(rows, t.fg(t.muted).Render("  nenhum destinatario"))
		}
	}
	switch {
	case it.id == "to-add":
		rows = append(rows, "", "  "+p.zones.Mark(it.id, t.button(it.label, btnInfo, focusID == it.id)))
	case strings.HasPrefix(it.id, "to:"):
		del := p.items[i+1] // o to-del da mesma linha
		marker := "  "
		if focusID == it.id || focusID == del.id {
			marker = t.fg(t.accent).Bold(true).Render("› ")
		}
		rows = append(rows, marker+p.zones.Mark(it.id, t.input(it.input, 40))+" "+
			p.zones.Mark(del.id, t.chip(del.label, btnDanger, focusID == del.id)))
	}
	return rows
}

// viewPlan e o resumo da config no Geral: o que falta e o que a Infra
// precisa aplicar. Os servicos em si se ligam e se aplicam na Infra.
func (p *setupPage) viewPlan(w int) string {
	t, pl := p.th, p.plan
	muted := t.fg(t.muted)
	wrap := lipgloss.NewStyle().Width(w)
	var b strings.Builder

	if !pl.Configured {
		b.WriteString("\n" + t.fg(t.warn).Bold(true).Render("Ainda sem configuracao.") + "\n")
		b.WriteString(wrap.Render("Preencha o GitHub App e salve (ctrl+s). Salvar so grava a configuracao: nada sobe. "+
			"Depois, na aba Infra, ligue o proxy (publica os apps com HTTPS; o e-mail do Let's Encrypt fica no painel "+
			"dele) e o que mais for usar: Postgres, MySQL, Garage.") + "\n")
		return b.String()
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
	if w := pl.Config.PortsWarning(); w != "" {
		b.WriteString("\n" + wrap.Foreground(t.warn).Render("Atencao: "+w) + "\n")
	}

	if len(pl.Stacks) == 0 {
		b.WriteString("\n" + wrap.Inherit(muted).Render("Nenhum servico ligado: ligue na aba Infra. "+
			"O proxy publica os apps com HTTPS; Postgres, MySQL e Garage conforme o uso.") + "\n")
	} else {
		// so o que pede acao: o resto esta em dia e aparece na Infra
		for _, s := range pl.Stacks {
			switch s.Change {
			case bootstrap.ChangeModified:
				b.WriteString("\n" + wrap.Foreground(t.warn).Render(s.Name+": config nova, Aplicar na aba Infra"))
			case bootstrap.ChangeNew:
				b.WriteString("\n" + wrap.Foreground(t.warn).Render(s.Name+": ainda nao subiu, Aplicar na aba Infra"))
			}
		}
		b.WriteString("\n")
	}
	if len(pl.Disable) > 0 {
		var names []string
		for _, s := range pl.Disable {
			names = append(names, s.Name)
		}
		b.WriteString(wrap.Inherit(muted).Render("Desligados, ainda no disco (saem na proxima aplicacao; os volumes ficam): "+
			strings.Join(names, ", ")) + "\n")
	}
	if pl.CAFile == "" && !slices.ContainsFunc(pl.Stacks, func(s bootstrap.StackPlan) bool { return s.Name == "proxy" }) {
		b.WriteString("\n" + wrap.Inherit(muted).Render("Antes de ligar o proxy, aponte o DNS dos dominios dos apps pra esta VPS: "+
			"o Let's Encrypt confere o dominio ao emitir o certificado.") + "\n")
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
	if !p.typing() {
		return nil
	}
	ti, cmd := it.input.Update(msg)
	*it.input = ti
	return cmd
}
