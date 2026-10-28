package ui

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/detect"
)

// secoes do detalhe de um app
var appSections = []string{"Geral", "Rotas", "Banco", "Secrets", "Logs"}

const (
	secGeneral = iota
	secRoutes
	secDatabase
	secSecrets
	secLogs
)

// opcoes do banco na tela; "" e nenhum
var (
	dbEngines     = []string{"", "postgres", "mysql"}
	dbEngineNames = []string{"Nenhum", "Postgres", "MySQL"}
	// de proposito sem password/url: senha so em arquivo
	dbKinds = []string{"host", "port", "name", "user", "password_file", "url_file"}
)

// field e cada coisa focavel de um formulario da aba Apps, na ordem do Tab:
// campo de texto, select (←→ troca) ou botao.
type field struct {
	id       string
	label    string
	input    *textinput.Model
	show     func() string
	change   func(delta int)
	press    func() tea.Cmd
	disabled func() bool
}

func (f *field) off() bool { return f.disabled != nil && f.disabled() }

// editores das partes de um app que a tela edita em linhas

type routeRow struct{ host, path, service, port *textinput.Model }

type dbRow struct {
	name *textinput.Model
	kind int
}

type envRow struct{ name, value *textinput.Model }

// appsPage e a aba Apps: a lista a esquerda e, a direita, o detalhe do app
// selecionado (deploy, rotas, banco, secrets, logs) ou o import de um app
// novo. So chama acoes do core; o estado dos apps chega pelo estado, entao
// um deploy do CI ou do CLI aparece aqui na hora.
type appsPage struct {
	api   core.API
	zones *zone.Manager
	th    theme

	list      []core.AppStatus
	sel       int
	inForm    bool // o foco esta no formulario (senao, na lista)
	section   int
	importing bool
	fields    []*field
	focus     int

	// editores do app selecionado; so recarregam quando troca de app ou
	// depois de salvar, pra uma foto nova do estado nao apagar a edicao
	loadedFor   string
	tag         *textinput.Model
	routes      []*routeRow
	dbEngine    int
	dbServices  *textinput.Model
	dbRows      []*dbRow
	secretName  *textinput.Model
	secretValue *textinput.Model

	// import
	impName, impDir                          *textinput.Model
	plan                                     *apps.Plan
	impPort, impHost, impVolumes, impSecrets *textinput.Model
	impEnv                                   []*envRow

	container  int    // logs: container do app selecionado
	pendingSel string // app recem-criado: seleciona quando chegar no estado
	// rolagem do formulario: a linha do campo com foco (anotada ao
	// desenhar) fica sempre visivel
	scroll    int
	focusLine int
	follow    bool // o foco mudou: a rolagem vai ate ele no proximo desenho
	busy      string
	status    string
	statusC   string
	detailErr string // erro longo (validacao): vai pro painel
}

// respostas das chamadas ao core
type (
	appActionMsg struct {
		app    string
		ok     string // mensagem de sucesso
		err    error
		reload bool // recarrega os editores (salvou)
	}
	appPlanMsg struct {
		plan apps.Plan
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
	p.tag = p.input("tag do build (ex: commit)", 24)
	p.dbServices = p.input("todos", 24)
	p.secretName = p.input("nome (ex: jwt_secret)", 24)
	p.secretValue = p.input("valor", 24)
	p.secretValue.EchoMode = textinput.EchoPassword
	p.impName = p.input("nome do repositorio", 28)
	p.impDir = p.input("pasta com Dockerfile", 28)
	p.impPort = p.input("8080", 8)
	p.impHost = p.input("vazio = sem rota", 28)
	p.impVolumes = p.input("/app/uploads, /data", 28)
	p.impSecrets = p.input("jwt_secret, api_key", 28)
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
	for _, ti := range p.allInputs() {
		p.style(ti)
	}
}

func (p *appsPage) allInputs() []*textinput.Model {
	out := []*textinput.Model{p.tag, p.dbServices, p.secretName, p.secretValue,
		p.impName, p.impDir, p.impPort, p.impHost, p.impVolumes, p.impSecrets}
	for _, r := range p.routes {
		out = append(out, r.host, r.path, r.service, r.port)
	}
	for _, r := range p.dbRows {
		out = append(out, r.name)
	}
	for _, r := range p.impEnv {
		out = append(out, r.name, r.value)
	}
	return out
}

// typing diz se o foco esta num campo de texto (numeros nao trocam de aba).
func (p *appsPage) typing() bool {
	return p.inForm && p.focus < len(p.fields) && p.fields[p.focus].input != nil
}

func (p *appsPage) selected() *core.AppStatus {
	if p.importing || p.sel >= len(p.list) {
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
	// mantem o mesmo app selecionado se ele continua na lista
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
	return p.loadSelected(false)
}

// loadSelected preenche os editores com o app selecionado.
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

	p.routes = nil
	for _, r := range a.Routes {
		p.addRoute(r)
	}
	p.loadDB(a.Database)
	p.secretName.SetValue("")
	p.secretValue.SetValue("")
	p.rebuild()
	return nil
}

func (p *appsPage) addRoute(r apps.Route) {
	row := &routeRow{host: p.input("app.exemplo.com.br", 24), path: p.input("/", 8),
		service: p.input("web", 10), port: p.input("porta", 6)}
	row.host.SetValue(r.Host)
	row.path.SetValue(r.Path)
	row.service.SetValue(r.Service)
	if r.Port > 0 {
		row.port.SetValue(strconv.Itoa(r.Port))
	}
	p.routes = append(p.routes, row)
}

func (p *appsPage) loadDB(db *apps.Database) {
	p.dbEngine = 0
	p.dbServices.SetValue("")
	p.dbRows = nil
	env := apps.DefaultDBEnv()
	if db != nil {
		for i, e := range dbEngines {
			if e == db.Engine {
				p.dbEngine = i
			}
		}
		p.dbServices.SetValue(strings.Join(db.Services, ", "))
		env = db.Env
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p.addDBRow(k, env[k])
	}
}

func (p *appsPage) addDBRow(name, kind string) {
	row := &dbRow{name: p.input("VARIAVEL", 22)}
	row.name.SetValue(name)
	for i, k := range dbKinds {
		if k == kind {
			row.kind = i
		}
	}
	p.dbRows = append(p.dbRows, row)
}

func (p *appsPage) dbValue() (*apps.Database, error) {
	if p.dbEngine == 0 {
		return nil, nil
	}
	env := map[string]string{}
	for _, r := range p.dbRows {
		name := strings.TrimSpace(r.name.Value())
		if name == "" {
			continue
		}
		if _, dup := env[name]; dup {
			return nil, fmt.Errorf("variavel %s repetida", name)
		}
		env[name] = dbKinds[r.kind]
	}
	return &apps.Database{Engine: dbEngines[p.dbEngine], Services: splitComma(p.dbServices.Value()), Env: env}, nil
}

func (p *appsPage) routesValue() ([]apps.Route, error) {
	var out []apps.Route
	for i, r := range p.routes {
		host := strings.TrimSpace(r.host.Value())
		if host == "" {
			continue
		}
		port, err := strconv.Atoi(strings.TrimSpace(r.port.Value()))
		if err != nil {
			return nil, fmt.Errorf("rota %d: porta invalida", i+1)
		}
		out = append(out, apps.Route{Host: host, Path: strings.TrimSpace(r.path.Value()),
			Service: strings.TrimSpace(r.service.Value()), Port: port})
	}
	return out, nil
}

func splitComma(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// rebuild monta os campos do formulario atual (secao do app ou import).
func (p *appsPage) rebuild() {
	p.fields = nil
	add := func(f *field) { p.fields = append(p.fields, f) }
	btn := func(id, label string, press func() tea.Cmd) *field {
		return &field{id: id, label: label, press: press, disabled: func() bool { return p.busy != "" }}
	}

	if p.importing {
		add(&field{id: "imp-name", label: "Nome do app", input: p.impName})
		if p.plan == nil {
			add(&field{id: "imp-dir", label: "Pasta do repositorio", input: p.impDir})
			add(btn("imp-analyze", "Analisar", p.analyze))
			add(btn("imp-cancel", "Cancelar", p.cancelImport))
		} else {
			add(&field{id: "imp-port", label: "Porta", input: p.impPort})
			add(&field{id: "imp-host", label: "Dominio", input: p.impHost})
			add(&field{id: "imp-volumes", label: "Volumes", input: p.impVolumes})
			for i, r := range p.impEnv {
				i := i
				add(&field{id: fmt.Sprintf("imp-env-name:%d", i), input: r.name})
				add(&field{id: fmt.Sprintf("imp-env-value:%d", i), input: r.value})
				add(btn(fmt.Sprintf("imp-env-del:%d", i), "x", func() tea.Cmd { p.impEnv = del(p.impEnv, i); p.rebuild(); return nil }))
			}
			add(btn("imp-env-add", "+ variavel", func() tea.Cmd { p.impEnv = append(p.impEnv, p.newEnvRow("", "")); p.rebuild(); return nil }))
			add(&field{id: "imp-secrets", label: "Secrets", input: p.impSecrets})
			p.dbFields(add, btn, "imp-")
			add(btn("imp-apply", "Aplicar", p.apply))
			add(btn("imp-back", "Voltar", func() tea.Cmd { p.plan = nil; p.detailErr = ""; p.rebuild(); return nil }))
		}
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
		noSpec := func() bool { return a.Spec == nil || p.busy != "" }
		add(&field{id: "tag", label: "Tag da imagem", input: p.tag, disabled: func() bool { return a.Spec == nil }})
		add(&field{id: "deploy", label: "Deploy", press: p.deploy, disabled: noSpec})
		add(btn("restart", "Reiniciar", p.restart))
		add(btn("stop", "Parar", p.stop))
	case secRoutes:
		for i, r := range p.routes {
			i := i
			add(&field{id: fmt.Sprintf("route-host:%d", i), input: r.host})
			add(&field{id: fmt.Sprintf("route-path:%d", i), input: r.path})
			add(&field{id: fmt.Sprintf("route-service:%d", i), input: r.service})
			add(&field{id: fmt.Sprintf("route-port:%d", i), input: r.port})
			add(btn(fmt.Sprintf("route-del:%d", i), "x", func() tea.Cmd { p.routes = del(p.routes, i); p.rebuild(); return nil }))
		}
		add(btn("route-add", "+ rota", func() tea.Cmd { p.addRoute(apps.Route{Service: apps.WebService}); p.rebuild(); return nil }))
		add(btn("routes-save", "Salvar rotas", p.saveRoutes))
	case secDatabase:
		p.dbFields(add, btn, "")
		add(btn("db-save", "Salvar banco", p.saveDB))
	case secSecrets:
		add(&field{id: "secret-name", label: "Secret", input: p.secretName})
		add(&field{id: "secret-value", label: "Valor", input: p.secretValue})
		add(btn("secret-save", "Gravar", p.saveSecret))
	case secLogs:
		add(&field{id: "log-container", label: "Container",
			show: func() string {
				if len(a.Containers) == 0 {
					return "nenhum"
				}
				return a.Containers[min(p.container, len(a.Containers)-1)].Name
			},
			change: func(d int) {
				if n := len(a.Containers); n > 0 {
					p.container = (p.container + d + n) % n
				}
			}})
	}
	p.setFocus(p.focus)
}

// dbFields sao os campos do banco, usados no import e na secao Banco.
func (p *appsPage) dbFields(add func(*field), btn func(string, string, func() tea.Cmd) *field, prefix string) {
	add(&field{id: prefix + "db-engine", label: "Banco",
		show:   func() string { return dbEngineNames[p.dbEngine] },
		change: func(d int) { p.dbEngine = (p.dbEngine + d + len(dbEngines)) % len(dbEngines); p.rebuild() }})
	if p.dbEngine == 0 {
		return
	}
	add(&field{id: prefix + "db-services", label: "Services", input: p.dbServices})
	for i, r := range p.dbRows {
		i, r := i, r
		add(&field{id: fmt.Sprintf("%sdb-name:%d", prefix, i), input: r.name})
		add(&field{id: fmt.Sprintf("%sdb-kind:%d", prefix, i),
			show:   func() string { return dbKinds[r.kind] },
			change: func(d int) { r.kind = (r.kind + d + len(dbKinds)) % len(dbKinds) }})
		add(btn(fmt.Sprintf("%sdb-del:%d", prefix, i), "x", func() tea.Cmd { p.dbRows = del(p.dbRows, i); p.rebuild(); return nil }))
	}
	add(btn(prefix+"db-add", "+ variavel", func() tea.Cmd { p.addDBRow("", "host"); p.rebuild(); return nil }))
}

func del[T any](list []T, i int) []T {
	if i < 0 || i >= len(list) {
		return list
	}
	return append(list[:i:i], list[i+1:]...)
}

func (p *appsPage) newEnvRow(name, value string) *envRow {
	r := &envRow{name: p.input("VARIAVEL", 18), value: p.input("valor", 18)}
	r.name.SetValue(name)
	r.value.SetValue(value)
	return r
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
func (p *appsPage) do(app, label, ok string, reload bool, fn func(ctx context.Context) error) tea.Cmd {
	if p.busy != "" {
		return nil
	}
	p.busy = label
	p.detailErr = ""
	p.setStatus("warn", label+"...")
	return func() tea.Msg {
		err := fn(context.Background())
		return appActionMsg{app: app, ok: ok, err: err, reload: reload}
	}
}

func (p *appsPage) deploy() tea.Cmd {
	a := p.selected()
	tag := strings.TrimSpace(p.tag.Value())
	if a == nil || a.Spec == nil {
		return nil
	}
	if tag == "" {
		p.setStatus("danger", "Informe a tag da imagem (o commit do build).")
		return nil
	}
	name := a.Name
	return p.do(name, "Deploy de "+name+":"+tag, name+" no ar com a tag "+tag+".", false, func(ctx context.Context) error {
		_, err := p.api.DeployAppTag(ctx, name, tag)
		return err
	})
}

func (p *appsPage) restart() tea.Cmd {
	a := p.selected()
	if a == nil {
		return nil
	}
	name := a.Name
	return p.do(name, "Reiniciando "+name, name+" reiniciado.", false, func(ctx context.Context) error {
		return p.api.RestartApp(ctx, name, "")
	})
}

func (p *appsPage) stop() tea.Cmd {
	a := p.selected()
	if a == nil {
		return nil
	}
	name := a.Name
	return p.do(name, "Parando "+name, name+" parado (Reiniciar volta).", false, func(ctx context.Context) error {
		return p.api.StopApp(ctx, name)
	})
}

func (p *appsPage) saveRoutes() tea.Cmd {
	a := p.selected()
	if a == nil {
		return nil
	}
	routes, err := p.routesValue()
	if err != nil {
		p.setStatus("danger", err.Error())
		return nil
	}
	name := a.Name
	return p.do(name, "Salvando rotas", "Rotas de "+name+" salvas.", true, func(ctx context.Context) error {
		return p.api.SetAppRoutes(ctx, name, routes)
	})
}

func (p *appsPage) saveDB() tea.Cmd {
	a := p.selected()
	if a == nil {
		return nil
	}
	db, err := p.dbValue()
	if err != nil {
		p.setStatus("danger", err.Error())
		return nil
	}
	name := a.Name
	if db == nil {
		return p.do(name, "Desligando o banco", "Banco desligado (os dados ficam).", true, func(ctx context.Context) error {
			return p.api.RemoveAppDatabase(ctx, name)
		})
	}
	return p.do(name, "Criando o banco", "Banco "+db.Engine+" ligado a "+name+".", true, func(ctx context.Context) error {
		return p.api.SetAppDatabase(ctx, name, db.Engine, db.Services, db.Env)
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
	return p.do(name, "Gravando "+secret, secret+" gravado (o app le em /run/secrets/"+secret+").", true, func(ctx context.Context) error {
		return p.api.SetAppSecret(ctx, name, secret, value)
	})
}

func (p *appsPage) startImport() tea.Cmd {
	p.importing, p.plan, p.inForm, p.focus = true, nil, true, 0
	p.detailErr = ""
	p.impName.SetValue("")
	p.impDir.SetValue("")
	p.setStatus("", "")
	p.rebuild()
	return p.setFocus(0)
}

func (p *appsPage) cancelImport() tea.Cmd {
	p.importing, p.plan, p.inForm = false, nil, false
	p.detailErr = ""
	p.loadedFor = ""
	return tea.Batch(p.loadSelected(true), p.setFocus(0))
}

// analyze le da pasta so os arquivos da analise e pede o plano ao core.
func (p *appsPage) analyze() tea.Cmd {
	name, dir := strings.TrimSpace(p.impName.Value()), strings.TrimSpace(p.impDir.Value())
	if name == "" || dir == "" {
		p.setStatus("danger", "Informe o nome do app e a pasta do repositorio.")
		return nil
	}
	p.busy = "Analisando"
	p.setStatus("warn", "Analisando o repositorio...")
	return func() tea.Msg {
		files := detect.Files{}
		for _, path := range detect.Wanted {
			data, err := os.ReadFile(filepath.Join(dir, path))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return appPlanMsg{err: err}
			}
			files[path] = data
		}
		if len(files) == 0 {
			return appPlanMsg{err: fmt.Errorf("nenhum arquivo do app em %s (Dockerfile, .env.example...)", dir)}
		}
		plan, err := p.api.PlanApp(context.Background(), name, files)
		return appPlanMsg{plan, err}
	}
}

// fillPlan leva o plano pros campos editaveis.
func (p *appsPage) fillPlan(pl apps.Plan) {
	p.plan = &pl
	p.impPort.SetValue("")
	if pl.Spec.Port > 0 {
		p.impPort.SetValue(strconv.Itoa(pl.Spec.Port))
	}
	p.impHost.SetValue("")
	if len(pl.Routes) > 0 {
		p.impHost.SetValue(pl.Routes[0].Host)
	}
	p.impVolumes.SetValue(strings.Join(pl.Spec.Volumes, ", "))
	p.impSecrets.SetValue(strings.Join(pl.Spec.Secrets, ", "))
	p.impEnv = nil
	keys := make([]string, 0, len(pl.Spec.Env))
	for k := range pl.Spec.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p.impEnv = append(p.impEnv, p.newEnvRow(k, pl.Spec.Env[k]))
	}
	p.loadDB(pl.Database)
	if pl.Database == nil {
		p.dbEngine = 0
	}
}

// planValue monta o plano com o que esta nos campos (o usuario decide).
func (p *appsPage) planValue() (apps.Plan, error) {
	pl := *p.plan
	pl.App = strings.TrimSpace(p.impName.Value())
	pl.Spec.Image = strings.TrimSuffix(pl.Spec.Image, "/"+p.plan.App) + "/" + pl.App
	port := 0
	if s := strings.TrimSpace(p.impPort.Value()); s != "" {
		var err error
		if port, err = strconv.Atoi(s); err != nil {
			return pl, errors.New("porta invalida")
		}
	}
	pl.Spec.Port = port
	pl.Spec.Volumes = splitComma(p.impVolumes.Value())
	pl.Spec.Secrets = splitComma(p.impSecrets.Value())
	pl.Spec.Env = map[string]string{}
	for _, r := range p.impEnv {
		if k := strings.TrimSpace(r.name.Value()); k != "" {
			pl.Spec.Env[k] = r.value.Value()
		}
	}
	pl.Routes = nil
	if host := strings.TrimSpace(p.impHost.Value()); host != "" {
		if port == 0 {
			return pl, errors.New("com dominio, informe a porta do app")
		}
		pl.Routes = []apps.Route{{Host: host, Service: apps.WebService, Port: port}}
	}
	db, err := p.dbValue()
	if err != nil {
		return pl, err
	}
	pl.Database = db
	return pl, nil
}

func (p *appsPage) apply() tea.Cmd {
	pl, err := p.planValue()
	if err != nil {
		p.setStatus("danger", err.Error())
		return nil
	}
	return p.do(pl.App, "Configurando "+pl.App, pl.App+" configurado. Informe os secrets e faca o deploy da primeira tag.", true,
		func(ctx context.Context) error { return p.api.ApplyApp(ctx, pl) })
}

func (p *appsPage) update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case appActionMsg:
		p.busy = ""
		if msg.err != nil {
			p.detailErr = msg.err.Error()
			p.setStatus("danger", "Nao deu certo:")
			p.rebuild()
			return p.checkCmd(), true
		}
		p.setStatus("ok", msg.ok)
		cmds := []tea.Cmd{p.checkCmd()}
		if p.importing {
			// o app novo entra na lista e fica selecionado
			p.importing, p.plan, p.inForm = false, nil, false
			p.loadedFor = "" // carrega os editores quando o app chegar no estado
			p.section = secSecrets
			for i, a := range p.list {
				if a.Name == msg.app {
					p.sel = i
				}
			}
			p.pendingSel = msg.app
		}
		if msg.reload {
			// os editores ja tem o que foi salvo: recarregar do estado depois
			// apagaria o que o usuario editou nesse meio tempo
			p.secretValue.SetValue("")
			p.secretName.SetValue("")
		}
		p.rebuild()
		return tea.Batch(cmds...), true
	case appPlanMsg:
		p.busy = ""
		if msg.err != nil {
			p.detailErr = msg.err.Error()
			p.setStatus("danger", "Nao analisou:")
			return nil, true
		}
		p.detailErr = ""
		p.fillPlan(msg.plan)
		p.setStatus("ok", "Revise o plano e aplique.")
		p.rebuild()
		return p.setFocus(1), true
	}
	return nil, false
}

func (p *appsPage) activate(f *field, delta int) tea.Cmd {
	if f.off() {
		return nil
	}
	if f.change != nil {
		if delta == 0 {
			delta = 1
		}
		f.change(delta)
		return nil
	}
	if f.press != nil {
		return f.press()
	}
	return nil
}

// click devolve true quando o clique foi na aba Apps (lista, secao ou campo).
func (p *appsPage) click(msg tea.MouseMsg) tea.Cmd {
	if p.zones.Get("app-new").InBounds(msg) && p.busy == "" {
		return p.startImport()
	}
	for i, a := range p.list {
		if p.zones.Get("app:" + a.Name).InBounds(msg) {
			p.importing, p.inForm, p.sel, p.focus = false, false, i, 0
			return tea.Batch(p.loadSelected(false), p.setFocus(0))
		}
	}
	for i, s := range appSections {
		if p.zones.Get("app-sec:" + s).InBounds(msg) {
			p.section, p.focus = i, 0
			p.rebuild()
			return nil
		}
	}
	for i, f := range p.fields {
		if p.zones.Get(f.id).InBounds(msg) {
			p.inForm = true
			cmd := p.setFocus(i)
			if f.input == nil {
				return tea.Batch(cmd, p.activate(f, 1))
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
			if n := len(p.list); n > 0 && !p.importing {
				d := 1
				if k == "up" {
					d = -1
				}
				p.sel = (p.sel + d + n) % n
				p.focus = 0
				return p.loadSelected(false)
			}
		case "left", "right":
			d := 1
			if k == "left" {
				d = -1
			}
			p.section = (p.section + d + len(appSections)) % len(appSections)
			p.focus = 0
			p.rebuild()
		case "n":
			if p.busy == "" {
				return p.startImport()
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
		if p.importing {
			return p.cancelImport()
		}
		p.inForm = false
		return p.setFocus(p.focus)
	case "tab", "down":
		return p.setFocus(p.focus + 1)
	case "shift+tab", "up":
		return p.setFocus(p.focus - 1)
	case "enter", "space":
		if f.input == nil {
			return p.activate(f, 1)
		}
		if k == "enter" {
			return p.setFocus(p.focus + 1)
		}
	case "left", "right":
		if f.change != nil {
			d := 1
			if k == "left" {
				d = -1
			}
			return p.activate(f, d)
		}
	}
	if f.input != nil && !f.off() {
		ti, cmd := f.input.Update(msg)
		*f.input = ti
		return cmd
	}
	return nil
}

// visao

func (p *appsPage) viewList(w, h int) string {
	t := p.th
	var b strings.Builder
	b.WriteString(t.bold().Render("Apps") + "\n")
	newBtn := t.pill(p.importing)
	if p.busy != "" {
		newBtn = t.disabledPill()
	}
	b.WriteString(p.zones.Mark("app-new", newBtn.Render("+ Novo app")) + "\n\n")
	if len(p.list) == 0 {
		b.WriteString(t.fg(t.muted).Render(lipgloss.NewStyle().Width(w - 4).Render("Nenhum app ainda. Crie com + Novo app a partir da pasta do repositorio.")))
	}
	for i, a := range p.list {
		mark, status, c := appStatus(a, t)
		sel := !p.importing && i == p.sel
		title := t.bold().Render(mark+" "+a.Name) + "  " + t.fg(c).Render(status)
		sub := t.fg(t.muted).Render("sem rota")
		if len(a.Routes) > 0 {
			sub = t.fg(t.muted).Render(a.Routes[0].Host + a.Routes[0].Path)
		}
		card := t.panel(sel && !p.inForm || sel && p.inForm, w-4, 2).Render(ansi.Truncate(title, w-6, "…") + "\n" + ansi.Truncate(sub, w-6, "…"))
		b.WriteString(p.zones.Mark("app:"+a.Name, card) + "\n")
	}
	return lipgloss.NewStyle().Width(w).Height(h).MaxHeight(h).Render(b.String())
}

// appStatus resume o app num simbolo, texto e cor.
func appStatus(a core.AppStatus, t theme) (string, string, color.Color) {
	running, total := 0, len(a.Containers)
	healthy := true
	for _, c := range a.Containers {
		if c.State == "running" {
			running++
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
	case healthy:
		return "✓", fmt.Sprintf("%d/%d no ar", running, total), t.ok
	default:
		return "✗", fmt.Sprintf("%d/%d no ar", running, total), t.danger
	}
}

func (p *appsPage) viewSections(w int) string {
	var parts []string
	for i, s := range appSections {
		parts = append(parts, p.zones.Mark("app-sec:"+s, p.th.pill(i == p.section).Render(s)), " ")
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

// render mostra um campo: texto, select ou botao, com o foco marcado.
func (p *appsPage) render(f *field, i int) string {
	t := p.th
	focused := p.inForm && i == p.focus
	switch {
	case f.input != nil:
		st := lipgloss.NewStyle().Background(t.fieldBg).Foreground(t.fieldFg).Width(f.input.Width() + 1)
		if f.off() {
			return st.Foreground(t.muted).Render(f.input.Value())
		}
		return st.Render(f.input.View())
	case f.change != nil:
		st := lipgloss.NewStyle().Background(t.fieldBg).Foreground(t.fieldFg)
		if focused {
			st = st.Foreground(t.accent).Bold(true)
		}
		return st.Render("‹ " + f.show() + " ›")
	default:
		st := t.pill(focused)
		if f.off() {
			st = t.disabledPill()
		}
		return st.Render(f.label)
	}
}

// row escreve um campo com rotulo, marcado pro clique.
func (p *appsPage) row(b *strings.Builder, id string) {
	for i, f := range p.fields {
		if f.id != id {
			continue
		}
		t := p.th
		marker, label := "  ", t.fg(t.text).Width(20).Render(f.label)
		if p.inForm && i == p.focus {
			marker = t.fg(t.accent).Bold(true).Render("› ")
			label = t.fg(t.accent).Bold(true).Width(20).Render(f.label)
		}
		if p.inForm && i == p.focus {
			p.focusLine = strings.Count(b.String(), "\n")
		}
		b.WriteString(p.zones.Mark(f.id, marker+label+p.render(f, i)) + "\n")
		return
	}
}

// inline escreve varios campos na mesma linha (linhas de tabela).
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
	case p.importing:
		head.WriteString(t.bold().Render("Novo app") + "\n")
	case a == nil:
		head.WriteString(t.bold().Render("Apps") + "\n")
	default:
		mark, status, c := appStatus(*a, t)
		head.WriteString(t.bold().Render(a.Name) + "  " + t.fg(c).Render(mark+" "+status) + "\n")
		head.WriteString(p.viewSections(inner) + "\n")
	}
	// status e erro no topo: nunca saem da tela com a rolagem
	if p.status != "" {
		c := map[string]color.Color{"ok": t.ok, "warn": t.warn, "danger": t.danger}[p.statusC]
		head.WriteString(lipgloss.NewStyle().Bold(true).Foreground(c).Width(inner).Render(p.status) + "\n")
	}
	if p.detailErr != "" {
		for _, l := range strings.Split(p.detailErr, "\n") {
			head.WriteString(wrap.Foreground(t.danger).Render("  "+l) + "\n")
		}
	}

	p.focusLine = -1
	body.WriteString("\n")
	switch {
	case p.importing:
		p.viewImport(&body, inner)
	case a == nil:
		body.WriteString(muted.Width(inner).Render(("Um app precisa so do Dockerfile no repositorio. Clique em + Novo app, informe a pasta do repositorio " +
			"e o cais propoe a configuracao: porta, dominio, volumes, variaveis, secrets e banco. " +
			"Voce revisa tudo antes de aplicar.")))
	default:
		switch p.section {
		case secGeneral:
			p.viewGeneral(&body, *a, inner)
		case secRoutes:
			body.WriteString(muted.Render("  dominio                  caminho  service    porta") + "\n")
			for i := range p.routes {
				p.inline(&body, fmt.Sprintf("route-host:%d", i), fmt.Sprintf("route-path:%d", i),
					fmt.Sprintf("route-service:%d", i), fmt.Sprintf("route-port:%d", i), fmt.Sprintf("route-del:%d", i))
			}
			if len(p.routes) == 0 {
				body.WriteString(muted.Render("  sem rotas: o app nao responde em nenhum dominio") + "\n")
			}
			body.WriteString("\n")
			p.inline(&body, "route-add", "routes-save")
			body.WriteString("\n" + muted.Width(inner).Render(("Um dominio e de um app so. Caminho vazio responde o dominio todo; /api manda so esse prefixo pro service.")) + "\n")
		case secDatabase:
			p.viewDB(&body, "", inner)
			body.WriteString("\n")
			p.inline(&body, "db-save")
		case secSecrets:
			p.viewSecrets(&body, inner)
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

func (p *appsPage) viewGeneral(b *strings.Builder, a core.AppStatus, w int) {
	t := p.th
	muted := t.fg(t.muted)
	if a.Spec != nil {
		b.WriteString(fmt.Sprintf("  %-12s %s:%s\n", "imagem", a.Spec.Image, muted.Render("<tag>")))
		if a.Spec.Port > 0 {
			b.WriteString(fmt.Sprintf("  %-12s %d\n", "porta", a.Spec.Port))
		}
	} else {
		b.WriteString(muted.Render("  app sem spec (modo compose): o deploy vem pelo CLI com o compose pronto") + "\n")
	}
	for _, r := range a.Routes {
		b.WriteString(fmt.Sprintf("  %-12s https://%s%s -> %s:%d\n", "rota", r.Host, r.Path, r.Service, r.Port))
	}
	if a.Database != nil {
		b.WriteString(fmt.Sprintf("  %-12s %s\n", "banco", a.Database.Engine))
	}
	if l := a.Last; l != nil {
		line := l.At.Local().Format("02/01 15:04") + " " + strings.Join(l.Images, ", ")
		c := t.ok
		if l.Error != "" {
			c = t.danger
			line += " (falhou"
			if l.RolledBack {
				line += "; versao anterior no ar"
			}
			line += ")"
		}
		b.WriteString(fmt.Sprintf("  %-12s %s\n", "ult. deploy", t.fg(c).Render(line)))
	}
	if len(a.Containers) > 0 {
		b.WriteString("\n" + t.bold().Render("Containers") + "\n")
		for _, c := range a.Containers {
			state := c.State
			if c.Health != "" {
				state += " (" + c.Health + ")"
			}
			b.WriteString(fmt.Sprintf("  %-28s %s\n", ansi.Truncate(c.Name, 28, "…"), state))
		}
	}
	b.WriteString("\n")
	p.row(b, "tag")
	b.WriteString("\n")
	p.inline(b, "deploy", "restart", "stop")
	if a.Spec != nil {
		b.WriteString("\n" + muted.Width(w).Render(("O CI faz o build e pede o deploy da tag sozinho; aqui e pra subir uma tag na mao ou voltar pra uma anterior. Se o deploy falhar, a versao anterior volta.")) + "\n")
	}
}

func (p *appsPage) viewDB(b *strings.Builder, prefix string, w int) {
	muted := p.th.fg(p.th.muted)
	p.row(b, prefix+"db-engine")
	if p.dbEngine == 0 {
		b.WriteString("\n" + muted.Width(w).Render(("Sem banco. Escolha Postgres ou MySQL (←→): o cais cria database e usuario na infra e o app nao declara banco no compose.")) + "\n")
		return
	}
	p.row(b, prefix+"db-services")
	b.WriteString("\n" + muted.Render("  variavel               recebe") + "\n")
	for i := range p.dbRows {
		p.inline(b, fmt.Sprintf("%sdb-name:%d", prefix, i), fmt.Sprintf("%sdb-kind:%d", prefix, i), fmt.Sprintf("%sdb-del:%d", prefix, i))
	}
	p.inline(b, prefix+"db-add")
	b.WriteString("\n" + muted.Width(w).Render(("A senha nunca vai em variavel: password_file e url_file sao caminhos de arquivo (/run/secrets/...) que o app le. " +
		"Services vazio = todos.")) + "\n")
}

func (p *appsPage) viewSecrets(b *strings.Builder, w int) {
	t := p.th
	muted := t.fg(t.muted)
	var list []apps.Secret
	if a := p.selected(); a != nil {
		list = a.Secrets
	}
	if len(list) == 0 {
		b.WriteString(muted.Render("  nenhum secret") + "\n")
	}
	for _, s := range list {
		state, c := "ok", t.ok
		switch {
		case s.Managed:
			state, c = "do cais", t.muted
		case !s.Set:
			state, c = "falta informar", t.danger
		}
		b.WriteString(fmt.Sprintf("  %-26s %s\n", s.Name, t.fg(c).Render(state)))
	}
	b.WriteString("\n")
	p.row(b, "secret-name")
	p.row(b, "secret-value")
	b.WriteString("\n")
	p.inline(b, "secret-save")
	b.WriteString("\n" + muted.Width(w).Render(("O valor nunca aparece de novo: so o nome. O app le em /run/secrets/<nome>; com o app no ar, os containers sao recriados pra ver o valor novo.")) + "\n")
}

func (p *appsPage) viewImport(b *strings.Builder, w int) {
	t := p.th
	muted := t.fg(t.muted)
	wrap := lipgloss.NewStyle().Width(w)
	p.row(b, "imp-name")
	if p.plan == nil {
		p.row(b, "imp-dir")
		b.WriteString("\n")
		p.inline(b, "imp-analyze", "imp-cancel")
		b.WriteString("\n" + muted.Width(w).Render(("Le so Dockerfile, .env.example e os manifestos (package.json, requirements.txt, composer.json...). O link do GitHub entra aqui quando o GitHub App estiver ligado.")) + "\n")
		return
	}
	a := p.plan.Analysis
	b.WriteString(fmt.Sprintf("  %-20s %s:%s\n", "Imagem", p.plan.Spec.Image, muted.Render("<tag do build>")))
	p.row(b, "imp-port")
	if a.PortSource != "" {
		b.WriteString(muted.Render("                        detectada: "+a.PortSource) + "\n")
	}
	p.row(b, "imp-host")
	p.row(b, "imp-volumes")
	b.WriteString("\n" + t.bold().Render("Variaveis") + muted.Render(" (comuns; segredo vai em Secrets)") + "\n")
	for i := range p.impEnv {
		p.inline(b, fmt.Sprintf("imp-env-name:%d", i), fmt.Sprintf("imp-env-value:%d", i), fmt.Sprintf("imp-env-del:%d", i))
	}
	p.inline(b, "imp-env-add")
	b.WriteString("\n")
	p.row(b, "imp-secrets")
	b.WriteString("\n" + t.bold().Render("Banco") + " ")
	if a.Database != nil && a.Database.Engine != "" {
		b.WriteString(muted.Render("detectado: " + a.Database.Why))
	} else if a.Database != nil {
		b.WriteString(t.fg(t.warn).Render(a.Database.Why))
	} else {
		b.WriteString(muted.Render("nao detectado: escolha se o app usa"))
	}
	b.WriteString("\n")
	p.viewDB(b, "imp-", w)
	if len(a.Notes) > 0 {
		b.WriteString("\n" + t.bold().Render("Avisos") + "\n")
		for _, n := range a.Notes {
			b.WriteString(wrap.Foreground(t.warn).Render("- "+n) + "\n")
		}
	}
	b.WriteString("\n")
	p.inline(b, "imp-apply", "imp-back")
}

// integracao com o modelo (logs usam o visualizador comum com a Infra)

func (m *model) appsClick(msg tea.MouseMsg) tea.Cmd {
	before := m.apps.section
	cmd := m.apps.click(msg)
	return tea.Batch(cmd, m.syncAppLogs(m.apps.section != before))
}

func (m *model) appsKey(msg tea.KeyPressMsg) tea.Cmd {
	k := msg.String()
	if m.apps.section == secLogs && !m.apps.importing {
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
		// leu e nao veio nada: e diferente de ainda estar carregando
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
	b.WriteString(p.viewSections(w-6) + "\n\n")
	p.row(&b, "log-container")
	b.WriteString("\n")
	empty := ""
	if len(a.Containers) == 0 {
		empty = "Nenhum container: o app ainda nao teve deploy ou esta parado."
	}
	m.writeLogs(&b, empty, w, h)
	return t.panel(p.inForm, w-2, h).MaxHeight(h + 2).Render(b.String())
}
