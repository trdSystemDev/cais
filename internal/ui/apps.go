package ui

import (
	"context"
	"fmt"
	"image/color"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/github"
)

// secoes do detalhe de um app
var appSections = []string{"Geral", "Variaveis", "Secrets", "Ajuda", "Logs"}

const (
	secGeneral = iota
	secEnv
	secSecrets
	secGuide
	secLogs
)

// field e cada coisa focavel de um formulario da aba Apps, na ordem do Tab:
// campo de texto ou botao.
type field struct {
	id       string
	label    string
	input    *textinput.Model
	press    func() tea.Cmd
	disabled func() bool
}

func (f *field) off() bool { return f.disabled != nil && f.disabled() }

type envRow struct{ name, value *textinput.Model }

// appsPage e a aba Apps: a lista a esquerda e, a direita, o detalhe do app
// selecionado (deploy, variaveis, secrets, ajuda, logs) ou o cadastro de um
// app novo. So chama acoes do core; o estado dos apps chega pelo estado.
type appsPage struct {
	api   core.API
	zones *zone.Manager
	th    theme

	list     []core.AppStatus
	sel      int
	inForm   bool // o foco esta no formulario (senao, na lista)
	section  int
	creating bool
	fields   []*field
	focus    int

	// editores do app selecionado: so recarregam quando troca de app ou
	// depois de salvar, pra uma foto nova do estado nao apagar a edicao
	loadedFor   string
	repo        *textinput.Model
	branch      *textinput.Model
	compose     *textinput.Model
	ref         *textinput.Model
	envRows     []*envRow
	envLoaded   string // app cujas variaveis estao nos editores
	secretName  *textinput.Model
	secretValue *textinput.Model
	guide       string
	guideFor    string
	delConfirm  *textinput.Model // nome do app digitado pra excluir
	delVolumes  bool             // excluir apaga tambem os volumes

	// app novo
	newName, newRepo, newBranch, newCompose *textinput.Model

	container  int    // logs: container do app selecionado
	pendingSel string // app recem-criado: seleciona quando chegar no estado
	scroll     int
	focusLine  int
	follow     bool
	busy       string
	status     string
	statusC    string
	detailErr  string
}

// respostas das chamadas ao core
type (
	appActionMsg struct {
		app      string
		ok       string
		err      error
		deployed *apps.DeployResult
	}
	appCreatedMsg struct {
		app apps.App
		err error
	}
	appEnvMsg struct {
		app string
		env map[string]string
		err error
	}
	appGuideMsg struct {
		app  string
		text string
		err  error
	}
	appLogsMsg struct {
		generation uint64
		text       string
		err        error
	}
)

func newAppsPage(api core.API, zones *zone.Manager, th theme) *appsPage {
	p := &appsPage{api: api, zones: zones, th: th}
	p.repo = p.input("dono/repositorio", 36)
	p.branch = p.input("a principal do repositorio", 28)
	p.compose = p.input(apps.DefaultCompose, 30)
	p.ref = p.input("vazio = mais novo da branch", 30)
	p.secretName = p.input("nome (ex: jwt_secret)", 26)
	p.secretValue = p.input("valor", 30)
	p.secretValue.EchoMode = textinput.EchoPassword
	p.delConfirm = p.input("digite o nome do app", 24)
	p.newName = p.input("nome do app (minusculas)", 30)
	p.newRepo = p.input("https://github.com/dono/repo", 40)
	p.newBranch = p.input("a principal do repositorio", 28)
	p.newCompose = p.input(apps.DefaultCompose, 30)
	p.rebuild()
	return p
}

func (p *appsPage) input(placeholder string, width int) *textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Placeholder = placeholder
	ti.SetWidth(width)
	p.style(&ti)
	return &ti
}

func (p *appsPage) style(ti *textinput.Model) {
	s := textinput.DefaultStyles(true)
	for _, st := range []*textinput.StyleState{&s.Focused, &s.Blurred} {
		st.Text = lipgloss.NewStyle().Foreground(p.th.fieldFg).Background(p.th.fieldBg)
		st.Placeholder = lipgloss.NewStyle().Foreground(p.th.muted).Background(p.th.fieldBg).Italic(true)
	}
	s.Cursor.Color = p.th.accent
	ti.SetStyles(s)
}

func (p *appsPage) setTheme(th theme) {
	p.th = th
	for _, ti := range []*textinput.Model{p.repo, p.branch, p.compose, p.ref, p.secretName, p.secretValue,
		p.newName, p.newRepo, p.newBranch, p.newCompose, p.delConfirm} {
		p.style(ti)
	}
	for _, r := range p.envRows {
		p.style(r.name)
		p.style(r.value)
	}
}

// typing diz se o foco esta num campo de texto (numeros nao trocam de aba).
func (p *appsPage) typing() bool {
	return p.inForm && p.focus < len(p.fields) && p.fields[p.focus].input != nil
}

func (p *appsPage) selected() *core.AppStatus {
	if p.creating || p.sel >= len(p.list) {
		return nil
	}
	return &p.list[p.sel]
}

// onState recebe a foto nova: a lista sempre acompanha; os editores so
// recarregam quando o app selecionado muda.
func (p *appsPage) onState(s core.State) tea.Cmd {
	name := ""
	if a := p.selected(); a != nil {
		name = a.Name
	}
	p.list = s.Apps.List
	if p.pendingSel != "" {
		name = p.pendingSel
	}
	for i, a := range p.list {
		if a.Name == name {
			p.sel = i
			if name == p.pendingSel {
				p.pendingSel = ""
			}
		}
	}
	if p.sel >= len(p.list) {
		p.sel = max(0, len(p.list)-1)
	}
	if !p.creating {
		p.rebuild() // os secrets (botoes de apagar) vem do estado
	}
	return p.loadSelected(false)
}

// loadSelected preenche os editores com o app selecionado e busca as
// variaveis (ficam fora do estado: podem ter segredo) e a ajuda.
func (p *appsPage) loadSelected(force bool) tea.Cmd {
	a := p.selected()
	if a == nil {
		p.loadedFor = ""
		p.rebuild()
		return nil
	}
	if !force && a.Name == p.loadedFor {
		return nil
	}
	p.loadedFor = a.Name
	p.container = 0
	p.detailErr = ""
	p.repo.SetValue(a.Repo)
	p.branch.SetValue(a.Branch)
	p.compose.SetValue(a.Compose)
	p.ref.SetValue("")
	p.secretName.SetValue("")
	p.secretValue.SetValue("")
	p.delConfirm.SetValue("")
	p.delVolumes = false
	p.envRows, p.envLoaded, p.guide, p.guideFor = nil, "", "", ""
	p.rebuild()
	return tea.Batch(p.envCmd(a.Name), p.guideCmd(a.Name))
}

func (p *appsPage) envCmd(name string) tea.Cmd {
	return func() tea.Msg {
		env, err := p.api.AppEnv(context.Background(), name)
		return appEnvMsg{name, env, err}
	}
}

func (p *appsPage) guideCmd(name string) tea.Cmd {
	return func() tea.Msg {
		text, err := p.api.AppGuide(context.Background(), name)
		return appGuideMsg{name, text, err}
	}
}

func (p *appsPage) newEnvRow(name, value string) *envRow {
	r := &envRow{name: p.input("VARIAVEL", 22), value: p.input("valor", 40)}
	r.name.SetValue(name)
	r.value.SetValue(value)
	return r
}

func del[T any](list []T, i int) []T {
	if i < 0 || i >= len(list) {
		return list
	}
	return append(list[:i:i], list[i+1:]...)
}

// rebuild monta os campos do formulario atual (secao do app ou app novo).
func (p *appsPage) rebuild() {
	p.fields = nil
	add := func(f *field) { p.fields = append(p.fields, f) }
	btn := func(id, label string, press func() tea.Cmd) *field {
		return &field{id: id, label: label, press: press, disabled: func() bool { return p.busy != "" }}
	}

	if p.creating {
		add(&field{id: "new-name", label: "Nome do app", input: p.newName})
		add(&field{id: "new-repo", label: "Repositorio", input: p.newRepo})
		add(&field{id: "new-branch", label: "Branch", input: p.newBranch})
		add(&field{id: "new-compose", label: "Compose", input: p.newCompose})
		add(btn("new-create", "Criar", p.create))
		add(btn("new-cancel", "Cancelar", p.cancelCreate))
		p.setFocus(p.focus)
		return
	}

	a := p.selected()
	if a == nil {
		p.setFocus(0)
		return
	}
	switch p.section {
	case secGeneral:
		add(&field{id: "ref", label: "Commit ou branch", input: p.ref})
		add(btn("deploy", "Deploy", p.deploy))
		add(btn("restart", "Reiniciar", p.restart))
		add(btn("stop", "Parar", p.stop))
		add(&field{id: "repo", label: "Repositorio", input: p.repo})
		add(&field{id: "branch", label: "Branch", input: p.branch})
		add(&field{id: "compose", label: "Compose", input: p.compose})
		add(btn("app-save", "Salvar", p.saveApp))
		add(&field{id: "delete-confirm", label: "Confirmar", input: p.delConfirm})
		add(btn("delete-volumes", "", func() tea.Cmd { p.delVolumes = !p.delVolumes; return nil }))
		name := a.Name
		add(&field{id: "delete", label: "Excluir app", press: p.deleteApp, disabled: func() bool {
			return p.busy != "" || strings.TrimSpace(p.delConfirm.Value()) != name
		}})
	case secEnv:
		for i, r := range p.envRows {
			add(&field{id: fmt.Sprintf("env-name:%d", i), input: r.name})
			add(&field{id: fmt.Sprintf("env-value:%d", i), input: r.value})
			add(btn(fmt.Sprintf("env-del:%d", i), "x", func() tea.Cmd { p.envRows = del(p.envRows, i); p.rebuild(); return nil }))
		}
		add(btn("env-add", "+ variavel", func() tea.Cmd { p.envRows = append(p.envRows, p.newEnvRow("", "")); p.rebuild(); return nil }))
		add(btn("env-save", "Salvar", p.saveEnv))
	case secSecrets:
		for _, s := range a.Secrets {
			name := s.Name
			add(btn("secret-del:"+name, "apagar", func() tea.Cmd { return p.deleteSecret(name) }))
		}
		add(&field{id: "secret-name", label: "Secret", input: p.secretName})
		add(&field{id: "secret-value", label: "Valor", input: p.secretValue})
		add(btn("secret-save", "Gravar", p.saveSecret))
	case secLogs:
		add(&field{id: "log-container", label: "Container", press: func() tea.Cmd {
			if n := len(a.Containers); n > 0 {
				p.container = (p.container + 1) % n
			}
			return nil
		}})
	}
	p.setFocus(p.focus)
}

func (p *appsPage) setFocus(i int) tea.Cmd {
	n := len(p.fields)
	if n == 0 {
		p.focus = 0
		return nil
	}
	p.focus = ((i % n) + n) % n
	p.follow = true
	var cmd tea.Cmd
	for j, f := range p.fields {
		if f.input == nil {
			continue
		}
		// valor de variavel costuma ser segredo: so aparece no campo em foco
		if strings.HasPrefix(f.id, "env-value:") {
			f.input.EchoMode = textinput.EchoPassword
			if p.inForm && j == p.focus {
				f.input.EchoMode = textinput.EchoNormal
			}
		}
		if p.inForm && j == p.focus {
			cmd = f.input.Focus()
		} else {
			f.input.Blur()
		}
	}
	return cmd
}

func (p *appsPage) setStatus(kind, msg string) { p.statusC, p.status = kind, msg }

// acoes

func (p *appsPage) checkCmd() tea.Cmd {
	return func() tea.Msg {
		p.api.CheckApps(context.Background())
		return nil
	}
}

// do roda uma acao do core fora do Update e devolve o resultado.
func (p *appsPage) do(app, label, ok string, fn func(ctx context.Context) error) tea.Cmd {
	if p.busy != "" {
		return nil
	}
	p.busy = label
	p.detailErr = ""
	p.setStatus("warn", label+"...")
	return func() tea.Msg {
		return appActionMsg{app: app, ok: ok, err: fn(context.Background())}
	}
}

func (p *appsPage) deploy() tea.Cmd {
	a := p.selected()
	if a == nil || p.busy != "" {
		return nil
	}
	name, ref := a.Name, strings.TrimSpace(p.ref.Value())
	p.busy = "deploy"
	p.detailErr = ""
	p.setStatus("warn", "Deploy de "+name+": baixando, build (a versao atual continua no ar) e up...")
	return func() tea.Msg {
		res, err := p.api.DeployApp(context.Background(), name, ref)
		return appActionMsg{app: name, err: err, deployed: &res}
	}
}

func (p *appsPage) restart() tea.Cmd {
	a := p.selected()
	if a == nil {
		return nil
	}
	name := a.Name
	return p.do(name, "Reiniciando "+name, name+" reiniciado.", func(ctx context.Context) error {
		return p.api.RestartApp(ctx, name, "")
	})
}

func (p *appsPage) stop() tea.Cmd {
	a := p.selected()
	if a == nil {
		return nil
	}
	name := a.Name
	return p.do(name, "Parando "+name, name+" parado (Reiniciar volta).", func(ctx context.Context) error {
		return p.api.StopApp(ctx, name)
	})
}

func (p *appsPage) deleteApp() tea.Cmd {
	a := p.selected()
	if a == nil || strings.TrimSpace(p.delConfirm.Value()) != a.Name {
		return nil
	}
	name, volumes := a.Name, p.delVolumes
	msg := name + " excluido (o database da infra fica)."
	if volumes {
		msg = name + " excluido, com os volumes (o database da infra fica)."
	}
	return p.do(name, "Excluindo "+name, msg, func(ctx context.Context) error {
		return p.api.DeleteApp(ctx, name, volumes)
	})
}

func (p *appsPage) saveApp() tea.Cmd {
	a := p.selected()
	if a == nil {
		return nil
	}
	repo, err := github.ParseRepo(p.repo.Value())
	if err != nil {
		p.setStatus("danger", err.Error())
		return nil
	}
	upd := apps.App{Name: a.Name, Repo: repo.String(), Branch: strings.TrimSpace(p.branch.Value()), Compose: strings.TrimSpace(p.compose.Value())}
	return p.do(a.Name, "Salvando", a.Name+" salvo; vale no proximo deploy.", func(ctx context.Context) error {
		return p.api.UpdateApp(ctx, upd)
	})
}

func (p *appsPage) saveEnv() tea.Cmd {
	a := p.selected()
	if a == nil {
		return nil
	}
	env := map[string]string{}
	for _, r := range p.envRows {
		k := strings.TrimSpace(r.name.Value())
		if k == "" {
			continue
		}
		if _, dup := env[k]; dup {
			p.setStatus("danger", "variavel "+k+" repetida")
			return nil
		}
		env[k] = r.value.Value()
	}
	name := a.Name
	return p.do(name, "Salvando variaveis", "Variaveis de "+name+" salvas; valem no proximo deploy.", func(ctx context.Context) error {
		return p.api.SetAppEnv(ctx, name, env)
	})
}

func (p *appsPage) saveSecret() tea.Cmd {
	a := p.selected()
	if a == nil {
		return nil
	}
	secret, value := strings.TrimSpace(p.secretName.Value()), p.secretValue.Value()
	if secret == "" || value == "" {
		p.setStatus("danger", "Informe o nome e o valor do secret.")
		return nil
	}
	name := a.Name
	return p.do(name, "Gravando "+secret, secret+" gravado em ${CAIS_SECRETS}/"+secret+" (vale no proximo deploy).", func(ctx context.Context) error {
		return p.api.SetAppSecret(ctx, name, secret, value)
	})
}

func (p *appsPage) deleteSecret(secret string) tea.Cmd {
	a := p.selected()
	if a == nil {
		return nil
	}
	name := a.Name
	return p.do(name, "Apagando "+secret, secret+" apagado.", func(ctx context.Context) error {
		return p.api.DeleteAppSecret(ctx, name, secret)
	})
}

func (p *appsPage) startCreate() tea.Cmd {
	p.creating, p.inForm, p.focus = true, true, 0
	p.detailErr = ""
	for _, ti := range []*textinput.Model{p.newName, p.newRepo, p.newBranch, p.newCompose} {
		ti.SetValue("")
	}
	p.setStatus("", "")
	p.rebuild()
	return p.setFocus(0)
}

func (p *appsPage) cancelCreate() tea.Cmd {
	p.creating, p.inForm = false, false
	p.detailErr = ""
	p.setStatus("", "")
	return tea.Batch(p.loadSelected(true), p.setFocus(0))
}

// create cadastra o app. O nome vazio vira o nome do repositorio.
func (p *appsPage) create() tea.Cmd {
	repo, err := github.ParseRepo(p.newRepo.Value())
	if err != nil {
		p.setStatus("danger", err.Error())
		return nil
	}
	a := apps.App{Name: strings.TrimSpace(p.newName.Value()), Repo: repo.String(),
		Branch: strings.TrimSpace(p.newBranch.Value()), Compose: strings.TrimSpace(p.newCompose.Value())}
	if a.Name == "" {
		a.Name = strings.ToLower(repo.Name)
	}
	p.busy = "Criando"
	p.detailErr = ""
	p.setStatus("warn", "Criando "+a.Name+"...")
	return func() tea.Msg {
		out, err := p.api.CreateApp(context.Background(), a)
		return appCreatedMsg{out, err}
	}
}

func (p *appsPage) update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case appActionMsg:
		p.busy = ""
		switch {
		case msg.err != nil:
			p.detailErr = msg.err.Error()
			if msg.deployed != nil && len(msg.deployed.Warnings) > 0 {
				p.detailErr += "\nAvisos sobre o compose:\n- " + strings.Join(msg.deployed.Warnings, "\n- ")
			}
			p.setStatus("danger", "Nao deu certo:")
		case msg.deployed != nil:
			p.setStatus("ok", msg.app+" no ar no commit "+shortCommit(msg.deployed.Commit)+".")
			if len(msg.deployed.Warnings) > 0 {
				p.detailErr = "Avisos sobre o compose (nao impediram o deploy):\n- " + strings.Join(msg.deployed.Warnings, "\n- ")
			}
		default:
			p.setStatus("ok", msg.ok)
			p.secretName.SetValue("")
			p.secretValue.SetValue("")
		}
		p.rebuild()
		return tea.Batch(p.checkCmd(), p.guideCmd(msg.app)), true
	case appCreatedMsg:
		p.busy = ""
		if msg.err != nil {
			p.detailErr = msg.err.Error()
			p.setStatus("danger", "Nao criou:")
			return nil, true
		}
		p.creating, p.inForm = false, false
		p.pendingSel, p.loadedFor = msg.app.Name, ""
		p.section, p.focus, p.scroll = secGuide, 0, 0
		p.setStatus("ok", msg.app.Name+" criado. A Ajuda mostra como escrever o compose de producao; depois Variaveis, Secrets e Deploy.")
		p.rebuild()
		return p.checkCmd(), true
	case appEnvMsg:
		a := p.selected()
		if a == nil || a.Name != msg.app || p.envLoaded == msg.app {
			return nil, true
		}
		if msg.err != nil {
			p.detailErr = msg.err.Error()
			return nil, true
		}
		keys := make([]string, 0, len(msg.env))
		for k := range msg.env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		p.envRows = nil
		for _, k := range keys {
			p.envRows = append(p.envRows, p.newEnvRow(k, msg.env[k]))
		}
		p.envLoaded = msg.app
		p.rebuild()
		return nil, true
	case appGuideMsg:
		if msg.err != nil {
			p.guide, p.guideFor = "Nao consegui montar a ajuda: "+msg.err.Error(), msg.app
		} else {
			p.guide, p.guideFor = msg.text, msg.app
		}
		return nil, true
	}
	return nil, false
}

func shortCommit(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}

func (p *appsPage) activate(f *field) tea.Cmd {
	if f.off() || f.press == nil {
		return nil
	}
	return f.press()
}

func (p *appsPage) click(msg tea.MouseMsg) tea.Cmd {
	if p.zones.Get("app-new").InBounds(msg) && p.busy == "" {
		return p.startCreate()
	}
	for i, a := range p.list {
		if p.zones.Get("app:" + a.Name).InBounds(msg) {
			p.creating, p.inForm, p.sel, p.focus = false, false, i, 0
			return tea.Batch(p.loadSelected(false), p.setFocus(0))
		}
	}
	if !p.creating {
		for i, s := range appSections {
			if p.zones.Get("app-sec:" + s).InBounds(msg) {
				p.section, p.focus, p.scroll = i, 0, 0
				p.rebuild()
				return nil
			}
		}
	}
	for i, f := range p.fields {
		if p.zones.Get(f.id).InBounds(msg) {
			p.inForm = true
			cmd := p.setFocus(i)
			if f.input == nil {
				return tea.Batch(cmd, p.activate(f))
			}
			return cmd
		}
	}
	return nil
}

func (p *appsPage) key(msg tea.KeyPressMsg) tea.Cmd {
	k := msg.String()
	if !p.inForm {
		switch k {
		case "up", "down":
			if n := len(p.list); n > 0 && !p.creating {
				d := 1
				if k == "up" {
					d = -1
				}
				p.sel = (p.sel + d + n) % n
				p.focus = 0
				return p.loadSelected(false)
			}
		case "left", "right":
			if p.creating {
				return nil
			}
			d := 1
			if k == "left" {
				d = -1
			}
			p.section = (p.section + d + len(appSections)) % len(appSections)
			p.focus, p.scroll = 0, 0
			p.rebuild()
		case "n":
			if p.busy == "" {
				return p.startCreate()
			}
		case "r":
			return p.checkCmd()
		case "enter", "tab":
			if len(p.fields) > 0 {
				p.inForm = true
				return p.setFocus(0)
			}
		}
		return nil
	}

	if len(p.fields) == 0 {
		p.inForm = false
		return nil
	}
	f := p.fields[p.focus]
	switch k {
	case "esc":
		if p.creating {
			return p.cancelCreate()
		}
		p.inForm = false
		return p.setFocus(p.focus)
	case "tab", "down":
		return p.setFocus(p.focus + 1)
	case "shift+tab", "up":
		return p.setFocus(p.focus - 1)
	case "enter", "space":
		if f.input == nil {
			return p.activate(f)
		}
		if k == "enter" {
			return p.setFocus(p.focus + 1)
		}
	}
	if f.input != nil && !f.off() {
		ti, cmd := f.input.Update(msg)
		*f.input = ti
		return cmd
	}
	return nil
}

// paste entrega o texto colado ao campo de texto com foco.
func (p *appsPage) paste(msg tea.PasteMsg) tea.Cmd {
	if !p.typing() {
		return nil
	}
	f := p.fields[p.focus]
	if f.off() {
		return nil
	}
	ti, cmd := f.input.Update(msg)
	*f.input = ti
	return cmd
}

// visao

func (p *appsPage) viewList(w, h int) string {
	t := p.th
	var b strings.Builder
	b.WriteString(t.bold().Render("Apps") + "\n")
	newBtn := t.pill(p.creating)
	if p.busy != "" {
		newBtn = t.disabledPill()
	}
	b.WriteString(p.zones.Mark("app-new", newBtn.Render("+ Novo app")) + "\n\n")
	if len(p.list) == 0 {
		b.WriteString(t.fg(t.muted).Width(w - 4).Render("Nenhum app ainda. Crie com + Novo app a partir do link do repositorio."))
	}
	for i, a := range p.list {
		mark, status, c := appStatus(a, t)
		sel := !p.creating && i == p.sel
		title := t.bold().Render(mark+" "+a.Name) + "  " + t.fg(c).Render(status)
		sub := t.fg(t.muted).Render(a.Repo)
		card := t.panel(sel, w-4, 2).Render(ansi.Truncate(title, w-6, "…") + "\n" + ansi.Truncate(sub, w-6, "…"))
		b.WriteString(p.zones.Mark("app:"+a.Name, card) + "\n")
	}
	return lipgloss.NewStyle().Width(w).Height(h).MaxHeight(h).Render(b.String())
}

// appStatus resume o app num simbolo, texto e cor.
func appStatus(a core.AppStatus, t theme) (string, string, color.Color) {
	running, total := 0, len(a.Containers)
	healthy, restarting := true, false
	for _, c := range a.Containers {
		if c.State == "running" {
			running++
		}
		if c.State == "restarting" {
			restarting = true
		}
		if c.State != "running" || c.Health == "unhealthy" {
			healthy = false
		}
	}
	switch {
	case a.Deploying:
		return "◌", "deploy...", t.warn
	case a.Error != "":
		return "✗", "erro", t.danger
	case total == 0 && a.Last == nil:
		return "·", "sem deploy", t.muted
	case total == 0:
		return "✗", "fora do ar", t.danger
	case restarting:
		return "✗", "reiniciando", t.danger
	case running == 0:
		return "■", "parado", t.muted
	case healthy:
		return "✓", fmt.Sprintf("%d/%d no ar", running, total), t.ok
	default:
		return "✗", fmt.Sprintf("%d/%d no ar", running, total), t.danger
	}
}

func (p *appsPage) viewSections() string {
	var parts []string
	for i, s := range appSections {
		parts = append(parts, p.zones.Mark("app-sec:"+s, p.th.pill(i == p.section).Render(s)), " ")
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

// render mostra um campo: texto ou botao, com o foco marcado.
func (p *appsPage) render(f *field, i int) string {
	t := p.th
	focused := p.inForm && i == p.focus
	if f.input != nil {
		st := lipgloss.NewStyle().Background(t.fieldBg).Foreground(t.fieldFg).Width(f.input.Width() + 1)
		return st.Render(f.input.View())
	}
	st := t.pill(focused)
	if f.off() {
		st = t.disabledPill()
	}
	return st.Render(f.label)
}

// row escreve um campo com rotulo, marcado pro clique.
func (p *appsPage) row(b *strings.Builder, id string) {
	for i, f := range p.fields {
		if f.id != id {
			continue
		}
		t := p.th
		marker, label := "  ", t.fg(t.text).Width(18).Render(f.label)
		if p.inForm && i == p.focus {
			marker = t.fg(t.accent).Bold(true).Render("› ")
			label = t.fg(t.accent).Bold(true).Width(18).Render(f.label)
			p.focusLine = strings.Count(b.String(), "\n")
		}
		b.WriteString(p.zones.Mark(f.id, marker+label+p.render(f, i)) + "\n")
		return
	}
}

// inline escreve varios campos na mesma linha (botoes, linhas de tabela).
func (p *appsPage) inline(b *strings.Builder, ids ...string) {
	var parts []string
	for _, id := range ids {
		for i, f := range p.fields {
			if f.id == id {
				if p.inForm && i == p.focus {
					p.focusLine = strings.Count(b.String(), "\n")
				}
				parts = append(parts, p.zones.Mark(f.id, p.render(f, i)))
			}
		}
	}
	b.WriteString("  " + strings.Join(parts, " ") + "\n")
}

func (p *appsPage) viewDetail(w, h int) string {
	t := p.th
	inner := w - 6
	wrap := lipgloss.NewStyle().Width(inner)
	muted := t.fg(t.muted)
	var head, body strings.Builder

	a := p.selected()
	switch {
	case p.creating:
		head.WriteString(t.bold().Render("Novo app") + "\n")
	case a == nil:
		head.WriteString(t.bold().Render("Apps") + "\n")
	default:
		mark, status, c := appStatus(*a, t)
		head.WriteString(t.bold().Render(a.Name) + "  " + t.fg(c).Render(mark+" "+status) + "\n")
		head.WriteString(p.viewSections() + "\n")
	}
	if p.status != "" {
		c := map[string]color.Color{"ok": t.ok, "warn": t.warn, "danger": t.danger}[p.statusC]
		head.WriteString(lipgloss.NewStyle().Bold(true).Foreground(c).Width(inner).Render(p.status) + "\n")
	}
	if p.detailErr != "" {
		c := t.danger
		if p.statusC == "ok" {
			c = t.warn
		}
		for _, l := range strings.Split(p.detailErr, "\n") {
			head.WriteString(wrap.Foreground(c).Render("  "+l) + "\n")
		}
	}

	p.focusLine = -1
	body.WriteString("\n")
	switch {
	case p.creating:
		p.viewCreate(&body, inner)
	case a == nil:
		body.WriteString(muted.Width(inner).Render(
			"Um app e um repositorio do GitHub com o compose de producao pronto. Clique em + Novo app e " +
				"informe o link do repositorio. No deploy o cais baixa o commit, grava o .env com as " +
				"variaveis do app, faz o build e sobe. A Ajuda de cada app mostra como escrever o compose."))
	default:
		switch p.section {
		case secGeneral:
			p.viewGeneral(&body, *a, inner)
		case secEnv:
			p.viewEnv(&body, inner)
		case secSecrets:
			p.viewSecrets(&body, *a, inner)
		case secGuide:
			if p.guideFor == a.Name {
				body.WriteString(p.guide)
			} else {
				body.WriteString(muted.Render("carregando..."))
			}
		}
	}

	headStr := strings.TrimSuffix(head.String(), "\n")
	lines := strings.Split(strings.TrimSuffix(body.String(), "\n"), "\n")
	avail := max(1, h-lipgloss.Height(headStr))
	if p.focusLine >= 0 && p.follow {
		p.follow = false
		if p.focusLine < p.scroll {
			p.scroll = p.focusLine
		}
		if p.focusLine >= p.scroll+avail {
			p.scroll = p.focusLine - avail + 1
		}
	}
	p.scroll = max(0, min(p.scroll, len(lines)-avail))
	visible := lines[p.scroll:min(len(lines), p.scroll+avail)]
	if p.scroll > 0 {
		visible[0] = muted.Render("  ↑ mais acima")
	}
	if p.scroll+avail < len(lines) {
		visible[len(visible)-1] = muted.Render("  ↓ mais abaixo")
	}
	return p.zones.Mark("app-detail", t.panel(p.inForm, w-2, h).MaxHeight(h+2).Render(headStr+"\n"+strings.Join(visible, "\n")))
}

// wheel rola o formulario com a roda do mouse.
func (p *appsPage) wheel(up bool) {
	if up {
		p.scroll = max(0, p.scroll-3)
	} else {
		p.scroll += 3 // o desenho limita ao fim
	}
}

func (p *appsPage) viewCreate(b *strings.Builder, w int) {
	p.row(b, "new-name")
	p.row(b, "new-repo")
	p.row(b, "new-branch")
	p.row(b, "new-compose")
	b.WriteString("\n")
	p.inline(b, "new-create", "new-cancel")
	b.WriteString("\n" + p.th.fg(p.th.muted).Width(w).Render(
		"Nome vazio = o nome do repositorio. O repositorio precisa do compose de producao pronto (labels do "+
			"Traefik, redes, secrets): depois de criar, a Ajuda mostra o modelo. O codigo e baixado pelo GitHub "+
			"App do cais (aba Setup).") + "\n")
}

func (p *appsPage) viewGeneral(b *strings.Builder, a core.AppStatus, w int) {
	t := p.th
	muted := t.fg(t.muted)
	fmt.Fprintf(b, "  %-14s github.com/%s\n", "repositorio", a.Repo)
	fmt.Fprintf(b, "  %-14s %s\n", "compose", a.ComposeFile())
	if a.Commit != "" {
		fmt.Fprintf(b, "  %-14s %s\n", "no ar", shortCommit(a.Commit))
	}
	if l := a.Last; l != nil {
		line := l.At.Local().Format("02/01 15:04") + " " + shortCommit(l.Commit)
		c := t.ok
		if l.Error != "" {
			c = t.danger
			line += " (falhou"
			if l.RolledBack {
				line += "; versao anterior no ar"
			}
			line += ")"
		}
		fmt.Fprintf(b, "  %-14s %s\n", "ult. deploy", t.fg(c).Render(line))
		if len(l.Problems) > 0 {
			b.WriteString(lipgloss.NewStyle().Width(w).Foreground(t.danger).Render("    compose recusado (corrija no repositorio; modelo na Ajuda):") + "\n")
		}
		for _, pr := range l.Problems {
			b.WriteString(lipgloss.NewStyle().Width(w).Foreground(t.danger).Render("    - "+pr) + "\n")
		}
		for _, wn := range l.Warnings {
			b.WriteString(lipgloss.NewStyle().Width(w).Foreground(t.warn).Render("    aviso: "+wn) + "\n")
		}
	}
	if len(a.Containers) > 0 {
		b.WriteString("\n" + t.bold().Render("Containers") + "\n")
		for _, c := range a.Containers {
			state := c.State
			if c.Health != "" {
				state += " (" + c.Health + ")"
			}
			fmt.Fprintf(b, "  %-30s %s\n", ansi.Truncate(c.Name, 30, "…"), state)
		}
	}
	b.WriteString("\n" + t.bold().Render("Deploy") + "\n")
	p.row(b, "ref")
	p.inline(b, "deploy", "restart", "stop")
	b.WriteString(muted.Width(w).Render("O build roda com a versao atual no ar; se o up falhar, o commit anterior volta.") + "\n")
	b.WriteString("\n" + t.bold().Render("Repositorio") + muted.Render(" (vale no proximo deploy)") + "\n")
	p.row(b, "repo")
	p.row(b, "branch")
	p.row(b, "compose")
	p.inline(b, "app-save")

	b.WriteString("\n" + t.bold().Render("Excluir app") + "\n")
	b.WriteString(muted.Width(w).Render("Apaga containers, imagens, codigo, variaveis e secrets do app. O database da infra fica (aba Infra). Digite "+a.Name+" pra confirmar.") + "\n")
	p.row(b, "delete-confirm")
	for _, f := range p.fields {
		if f.id == "delete-volumes" {
			f.label = "Volumes (dados): manter"
			if p.delVolumes {
				f.label = "Volumes (dados): APAGAR"
			}
		}
	}
	p.inline(b, "delete-volumes", "delete")
}

func (p *appsPage) viewEnv(b *strings.Builder, w int) {
	muted := p.th.fg(p.th.muted)
	b.WriteString(muted.Render("  variavel               valor") + "\n")
	for i := range p.envRows {
		p.inline(b, fmt.Sprintf("env-name:%d", i), fmt.Sprintf("env-value:%d", i), fmt.Sprintf("env-del:%d", i))
	}
	if len(p.envRows) == 0 {
		b.WriteString(muted.Render("  nenhuma variavel") + "\n")
	}
	p.inline(b, "env-add")
	b.WriteString("\n")
	p.inline(b, "env-save")
	b.WriteString("\n" + muted.Width(w).Render(
		"Viram o .env na pasta do projeto no deploy: o compose usa com env_file: .env ou ${VAR}. "+
			"O cais acrescenta CAIS_APP, CAIS_COMMIT e CAIS_SECRETS.") + "\n")
}

func (p *appsPage) viewSecrets(b *strings.Builder, a core.AppStatus, w int) {
	t := p.th
	muted := t.fg(t.muted)
	if len(a.Secrets) == 0 {
		b.WriteString(muted.Render("  nenhum secret") + "\n")
	}
	for _, s := range a.Secrets {
		fmt.Fprintf(b, "  %-26s %s ", s.Name, muted.Render("${CAIS_SECRETS}/"+s.Name))
		for i, f := range p.fields {
			if f.id == "secret-del:"+s.Name {
				if p.inForm && i == p.focus {
					p.focusLine = strings.Count(b.String(), "\n")
				}
				b.WriteString(p.zones.Mark(f.id, p.render(f, i)))
			}
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
	p.row(b, "secret-name")
	p.row(b, "secret-value")
	p.inline(b, "secret-save")
	b.WriteString("\n" + muted.Width(w).Render(
		"Cada secret e um arquivo em ${CAIS_SECRETS}/<nome>. No compose: secrets: {x: {file: ${CAIS_SECRETS}/x}} "+
			"e o service le /run/secrets/x. O valor nunca aparece de novo; vale no proximo deploy.") + "\n")
}

// integracao com o modelo (logs usam o visualizador comum com a Infra)

func (m *model) appsClick(msg tea.MouseMsg) tea.Cmd {
	before, cont := m.apps.section, m.apps.container
	cmd := m.apps.click(msg)
	return tea.Batch(cmd, m.syncAppLogs(m.apps.section != before || m.apps.container != cont))
}

func (m *model) appsKey(msg tea.KeyPressMsg) tea.Cmd {
	k := msg.String()
	if m.apps.section == secLogs && !m.apps.creating {
		switch k {
		case "pgup", "pgdown":
			m.scrollLogs(k == "pgup")
			return nil
		case "end":
			m.logsOffset = 0
			return nil
		}
	}
	sel, sec, cont := m.apps.sel, m.apps.section, m.apps.container
	cmd := m.apps.key(msg)
	force := sel != m.apps.sel || sec != m.apps.section || cont != m.apps.container
	return tea.Batch(cmd, m.syncAppLogs(force))
}

// scrollLogs rola os logs 10 linhas pra cima (antigos) ou pra baixo.
func (m *model) scrollLogs(up bool) {
	m.logSelection = logSelection{}
	if up {
		m.logsOffset = min(m.logsOffset+10, max(0, len(m.logArea.allLines)-m.logArea.height))
	} else {
		m.logsOffset = max(0, m.logsOffset-10)
	}
}

// syncAppLogs troca o container dos logs quando a secao Logs esta aberta.
func (m *model) syncAppLogs(force bool) tea.Cmd {
	if m.tab != tabApps || m.disconnected {
		return nil
	}
	target := ""
	if a := m.apps.selected(); a != nil && m.apps.section == secLogs && len(a.Containers) > 0 {
		target = a.Containers[min(m.apps.container, len(a.Containers)-1)].Name
	}
	if !force && target == m.logsTarget {
		return nil
	}
	m.logSelection = logSelection{}
	m.logsGeneration++
	m.logsTarget, m.logsText, m.logsError, m.logsOffset = target, "", "", 0
	if target == "" {
		return nil
	}
	return m.fetchAppLogs()
}

func (m *model) fetchAppLogs() tea.Cmd {
	a := m.apps.selected()
	if a == nil || m.logsTarget == "" {
		return nil
	}
	generation, app, target := m.logsGeneration, a.Name, m.logsTarget
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		out, err := m.api.AppLogs(ctx, app, target)
		return appLogsMsg{generation, out, err}
	}
}

func (m *model) onAppLogs(msg appLogsMsg) tea.Cmd {
	if msg.generation != m.logsGeneration || m.tab != tabApps || m.disconnected {
		return nil
	}
	m.logsText, m.logsError = msg.text, ""
	if msg.err != nil {
		m.logsError = msg.err.Error()
	} else if strings.TrimSpace(msg.text) == "" {
		m.logsText = "(o container ainda nao escreveu nada no log)"
	}
	generation := m.logsGeneration
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return appLogsTick(generation) })
}

type appLogsTick uint64

func (m *model) onAppLogsTick(g appLogsTick) tea.Cmd {
	if uint64(g) != m.logsGeneration || m.tab != tabApps || m.disconnected {
		return nil
	}
	if len(m.logSelection.lines) > 0 {
		return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return g })
	}
	return m.fetchAppLogs()
}

func (m *model) viewApps(h int) string {
	t := m.th
	if m.w < 80 {
		m.logArea = logArea{}
		return t.panel(false, m.w-2, max(1, h-2)).Render("Amplie o terminal para pelo menos 80 colunas para ver os apps.")
	}
	leftW := min(36, max(26, m.w/4))
	left := m.apps.viewList(leftW, h)
	rightW := m.w - leftW - 1
	innerH := max(1, h-2)
	var right string
	if a := m.apps.selected(); a != nil && m.apps.section == secLogs {
		right = m.zones.Mark("app-logs", m.viewAppLogs(*a, rightW, innerH))
	} else {
		m.logArea = logArea{}
		right = m.apps.viewDetail(rightW, innerH)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)
}

func (m *model) viewAppLogs(a core.AppStatus, w, h int) string {
	t := m.th
	p := m.apps
	var b strings.Builder
	mark, status, c := appStatus(a, t)
	b.WriteString(t.bold().Render(a.Name) + "  " + t.fg(c).Render(mark+" "+status) + "\n")
	b.WriteString(p.viewSections() + "\n\n")
	name := "nenhum"
	if len(a.Containers) > 0 {
		name = a.Containers[min(p.container, len(a.Containers)-1)].Name
	}
	for i, f := range p.fields {
		if f.id == "log-container" {
			b.WriteString(p.zones.Mark(f.id, "  "+t.fg(t.text).Width(18).Render("Container")+p.render(&field{label: "‹ " + name + " ›", press: f.press}, i)) + "\n")
		}
	}
	b.WriteString("\n")
	empty := ""
	if len(a.Containers) == 0 {
		empty = "Nenhum container: o app ainda nao teve deploy ou esta parado."
	}
	m.writeLogs(&b, empty, w, h)
	return t.panel(p.inForm, w-2, h).MaxHeight(h + 2).Render(b.String())
}
