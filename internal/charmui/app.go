// Package charmui e um prototipo da TUI com Bubble Tea + Lip Gloss, so com
// a aba Setup, pra comparar com a versao tview (internal/ui). Usa o mesmo
// core.API: abre pelo daemon, como a outra.
package charmui

import (
	"context"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/trdSystemDev/cais/internal/bootstrap"
	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/stacks"
)

var tabs = []string{"Dashboard", "Apps", "Secrets", "Bancos", "Infra", "Setup"}

const setupTab = 5

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

type model struct {
	api   core.API
	zones *zone.Manager
	th    theme
	w, h  int

	tab   int
	items []*item
	focus int

	tls    int // 0 Let's Encrypt, 1 CA local
	stacks map[string]bool

	cfg     config.Config
	plan    bootstrap.Plan
	state   core.State
	states  <-chan core.State
	status  string
	statusC string // ok, warn, danger
	saveErr string
	showRun bool
	running bool
}

// Mensagens que voltam das chamadas ao core (feitas fora do Update).
type (
	stateMsg  core.State
	closedMsg struct{}
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

// Run abre o prototipo e bloqueia ate o usuario sair.
func Run(api core.API) error {
	states, unsubscribe, err := api.Subscribe(context.Background())
	if err != nil {
		return err
	}
	defer unsubscribe()

	m := newModel(api, states)
	_, err = tea.NewProgram(m).Run()
	m.zones.Close()
	return err
}

func newModel(api core.API, states <-chan core.State) *model {
	m := &model{
		api:    api,
		zones:  zone.New(),
		th:     newTheme(true),
		tab:    setupTab,
		stacks: map[string]bool{},
		states: states,
	}
	input := func(id, label, placeholder string) {
		ti := textinput.New()
		ti.Prompt = ""
		ti.Placeholder = placeholder
		ti.SetWidth(28)
		m.items = append(m.items, &item{id: id, label: label, kind: kindInput, input: &ti})
	}
	input("domain", "Dominio", "exemplo.com.br")
	input("email", "E-mail", "voce@exemplo.com.br")
	m.items = append(m.items, &item{id: "tls", label: "Certificados", kind: kindSelect})
	for _, st := range stacks.All {
		m.items = append(m.items, &item{id: "stack:" + st.Name, label: st.Name, kind: kindCheck})
	}
	input("ghClient", "GitHub client ID", "so com woodpecker")
	input("ghAdmin", "GitHub admin", "seu usuario")
	input("ghSecret", "GitHub secret", "so pra gravar ou trocar")
	m.get("ghSecret").input.EchoMode = textinput.EchoPassword
	input("hostCI", "Host do CI", "padrao ci.<dominio>")
	input("hostReg", "Host do registry", "padrao registry.<dominio>")
	m.items = append(m.items,
		&item{id: "save", label: "Salvar", kind: kindButton},
		&item{id: "run", label: "Executar setup", kind: kindButton})
	m.applyTheme()
	m.setFocus(0)
	return m
}

func (m *model) get(id string) *item {
	for _, it := range m.items {
		if it.id == id {
			return it
		}
	}
	return nil
}

func (m *model) applyTheme() {
	for _, it := range m.items {
		if it.input == nil {
			continue
		}
		s := textinput.DefaultStyles(true)
		for _, st := range []*textinput.StyleState{&s.Focused, &s.Blurred} {
			st.Text = lipgloss.NewStyle().Foreground(m.th.fieldFg).Background(m.th.fieldBg)
			st.Placeholder = lipgloss.NewStyle().Foreground(m.th.muted).Background(m.th.fieldBg).Italic(true)
		}
		s.Cursor.Color = m.th.accent
		it.input.SetStyles(s)
	}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(tea.RequestBackgroundColor, m.waitState(), m.loadCmd())
}

func (m *model) waitState() tea.Cmd {
	return func() tea.Msg {
		s, ok := <-m.states
		if !ok {
			return closedMsg{}
		}
		return stateMsg(s)
	}
}

func (m *model) loadCmd() tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		info, err := m.api.LoadConfig(ctx)
		if err != nil {
			return loadedMsg{err: err}
		}
		plan, err := m.api.PlanSetup(ctx)
		return loadedMsg{info: info, plan: plan, err: err}
	}
}

func (m *model) planCmd() tea.Cmd {
	return func() tea.Msg {
		plan, err := m.api.PlanSetup(context.Background())
		return planMsg{plan, err}
	}
}

func (m *model) fill(c config.Config) {
	m.cfg = c
	m.get("domain").input.SetValue(c.Domain)
	m.get("email").input.SetValue(c.Email)
	m.tls = 0
	if !c.LetsEncrypt() {
		m.tls = 1
	}
	for _, st := range stacks.All {
		m.stacks[st.Name] = c.HasStack(st.Name)
	}
	m.get("ghClient").input.SetValue(c.Woodpecker.GitHubClient)
	m.get("ghAdmin").input.SetValue(c.Woodpecker.Admin)
	m.get("hostCI").input.SetValue(c.Hosts.CI)
	m.get("hostReg").input.SetValue(c.Hosts.Registry)
}

func (m *model) formConfig() config.Config {
	c := m.cfg
	val := func(id string) string { return strings.TrimSpace(m.get(id).input.Value()) }
	c.Domain, c.Email = val("domain"), val("email")
	c.TLS = ""
	if m.tls == 1 {
		c.TLS = config.TLSSelfSigned
	}
	c.Stacks = nil
	for _, st := range stacks.All {
		if m.stacks[st.Name] {
			c.Stacks = append(c.Stacks, st.Name)
		}
	}
	c.Woodpecker.GitHubClient, c.Woodpecker.Admin = val("ghClient"), val("ghAdmin")
	c.Hosts.CI, c.Hosts.Registry = val("hostCI"), val("hostReg")
	return c
}

func (m *model) save() tea.Cmd {
	c := m.formConfig()
	secret := m.get("ghSecret").input.Value()
	m.setStatus("warn", "Salvando...")
	return func() tea.Msg {
		ctx := context.Background()
		err := m.api.SaveConfig(ctx, c)
		if err == nil && secret != "" {
			err = m.api.SetSecret(ctx, "github_client_secret", secret)
		}
		return savedMsg{c, err}
	}
}

func (m *model) run() tea.Cmd {
	if m.running || !m.plan.Ready() {
		return nil
	}
	m.showRun, m.running = true, true
	m.setStatus("warn", "Executando o Setup...")
	return func() tea.Msg {
		_, err := m.api.RunSetup(context.Background())
		return runDoneMsg{err}
	}
}

func (m *model) setStatus(kind, msg string) { m.statusC, m.status = kind, msg }

func (m *model) setFocus(i int) tea.Cmd {
	n := len(m.items)
	m.focus = ((i % n) + n) % n
	var cmd tea.Cmd
	for j, it := range m.items {
		if it.input == nil {
			continue
		}
		if j == m.focus {
			cmd = it.input.Focus()
		} else {
			it.input.Blur()
		}
	}
	return cmd
}

// activate e o Enter/espaco/clique num item.
func (m *model) activate(it *item) tea.Cmd {
	switch {
	case it.kind == kindSelect:
		m.tls = 1 - m.tls
	case it.kind == kindCheck:
		name := strings.TrimPrefix(it.id, "stack:")
		m.stacks[name] = !m.stacks[name]
	case it.id == "save":
		return m.save()
	case it.id == "run":
		return m.run()
	}
	return nil
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case tea.BackgroundColorMsg:
		m.th = newTheme(msg.IsDark())
		m.applyTheme()

	case stateMsg:
		m.state = core.State(msg)
		if m.state.Setup.Running {
			m.showRun = true
		}
		return m, m.waitState()
	case closedMsg:
		m.setStatus("danger", "A conexao com o daemon caiu. Saia (ctrl+q) e veja: systemctl status cais")

	case loadedMsg:
		if msg.err != nil {
			m.setStatus("danger", "Erro lendo a config: "+msg.err.Error())
			return m, nil
		}
		m.fill(msg.info.Config)
		m.plan = msg.plan
	case planMsg:
		if msg.err == nil {
			m.plan = msg.plan
		}
	case savedMsg:
		m.showRun = false
		if msg.err != nil {
			m.saveErr = msg.err.Error()
			m.setStatus("danger", "Nao salvou: veja o motivo ao lado.")
			return m, nil
		}
		m.saveErr = ""
		m.cfg = msg.cfg
		m.get("ghSecret").input.SetValue("")
		m.setStatus("ok", "Config salva.")
		return m, m.planCmd()
	case runDoneMsg:
		m.running = false
		if msg.err != nil {
			m.setStatus("danger", "Setup parou: "+firstLine(msg.err.Error()))
		} else {
			m.setStatus("ok", "Setup concluido.")
		}
		return m, m.planCmd()

	case tea.MouseReleaseMsg:
		if msg.Button != tea.MouseLeft {
			return m, nil
		}
		return m, m.click(msg)

	case tea.KeyPressMsg:
		return m, m.key(msg)
	}
	return m, nil
}

func (m *model) click(msg tea.MouseMsg) tea.Cmd {
	if m.zones.Get("quit").InBounds(msg) {
		return tea.Quit
	}
	for i := range tabs {
		if m.zones.Get("tab:" + tabs[i]).InBounds(msg) {
			m.tab = i
			return nil
		}
	}
	if m.tab != setupTab {
		return nil
	}
	for i, it := range m.items {
		if m.zones.Get(it.id).InBounds(msg) {
			cmd := m.setFocus(i)
			if it.kind != kindInput {
				return tea.Batch(cmd, m.activate(it))
			}
			return cmd
		}
	}
	return nil
}

func (m *model) key(msg tea.KeyPressMsg) tea.Cmd {
	k := msg.String()
	switch k {
	case "ctrl+q", "ctrl+c":
		return tea.Quit
	case "ctrl+s":
		return m.save()
	case "ctrl+r":
		return m.run()
	}
	it := m.items[m.focus]
	typing := m.tab == setupTab && it.kind == kindInput
	if !typing && len(k) == 1 && k[0] >= '1' && k[0] <= '6' {
		m.tab = int(k[0] - '1')
		return nil
	}
	if m.tab != setupTab {
		return nil
	}
	switch k {
	case "tab", "down":
		return m.setFocus(m.focus + 1)
	case "shift+tab", "up":
		return m.setFocus(m.focus - 1)
	case "enter", "space":
		if it.kind != kindInput {
			return m.activate(it)
		}
		if k == "enter" {
			return m.setFocus(m.focus + 1)
		}
	case "left", "right":
		if it.kind == kindSelect {
			return m.activate(it)
		}
	}
	if it.kind == kindInput {
		ti, cmd := it.input.Update(msg)
		*it.input = ti
		return cmd
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
