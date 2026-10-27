// Package ui e a TUI do cais (Bubble Tea + Lip Gloss), com mouse. E so uma
// visao do core: le o estado via Subscribe e dispara acoes nele, sem saber
// se o core esta no mesmo processo ou no daemon, do outro lado do socket.
package ui

import (
	"context"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/trdSystemDev/cais/internal/core"
)

var tabs = []string{"Dashboard", "Apps", "Secrets", "Bancos", "Infra", "Setup"}

const (
	tabDashboard = 0
	tabInfra     = 4
	tabSetup     = 5
)

// placeholders das abas que ainda nao existem: fase do roadmap e o que vem.
var placeholders = map[int]struct {
	phase int
	desc  string
}{
	1: {3, "Criar app pelo link do GitHub, deploy, logs ao vivo, restart."},
	2: {3, "Secrets centrais e por app: gerar, copiar, propagar."},
	3: {4, "Databases e usuarios no Postgres/MySQL, instancias dedicadas."},
}

// helps sao os atalhos de cada aba, no rodape.
var helps = map[int]string{
	tabDashboard: "enter/r verifica · ",
	tabInfra:     "↑↓ servico · enter liga/desliga · r atualiza · ",
	tabSetup:     "tab/↑↓ campo · espaco marca · ctrl+s salva · ctrl+r executa · ",
}

// App e a TUI inteira. Nada e desenhado ate Run ser chamado.
type App struct {
	api core.API
}

// New monta a interface sobre qualquer core.API (local ou rpc.Client).
func New(api core.API) *App { return &App{api: api} }

// Run se inscreve no core, dispara a checagem inicial do ambiente e
// bloqueia ate o usuario sair.
func (a *App) Run() error {
	states, unsubscribe, err := a.api.Subscribe(context.Background())
	if err != nil {
		return err
	}
	defer unsubscribe()

	m := newModel(a.api, states)
	defer m.zones.Close()
	_, err = tea.NewProgram(m).Run()
	return err
}

type model struct {
	api    core.API
	zones  *zone.Manager
	th     theme
	w, h   int
	tab    int
	state  core.State
	states <-chan core.State
	// a conexao com o daemon caiu: nada na tela e confiavel, so sair
	disconnected bool
	setup        *setupPage

	// aba Infra
	infraSel  int    // servico opcional selecionado (teclado)
	toggling  string // servico sendo ligado/desligado daqui
	infraMsg  string
	infraMsgC string
}

type (
	stateMsg  core.State
	closedMsg struct{}
)

func newModel(api core.API, states <-chan core.State) *model {
	m := &model{api: api, zones: zone.New(), th: newTheme(true), states: states}
	m.setup = newSetupPage(api, m.zones, m.th)
	return m
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(tea.RequestBackgroundColor, m.waitState(), m.checkCmd(), m.setup.load())
}

// waitState entrega a proxima foto do core; o Update pede a seguinte.
func (m *model) waitState() tea.Cmd {
	return func() tea.Msg {
		s, ok := <-m.states
		if !ok {
			return closedMsg{}
		}
		return stateMsg(s)
	}
}

// checkCmd dispara a checagem do ambiente; o resultado chega pelo estado.
func (m *model) checkCmd() tea.Cmd {
	return func() tea.Msg {
		m.api.CheckEnvironment(context.Background())
		return nil
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil
	case tea.BackgroundColorMsg:
		m.th = newTheme(msg.IsDark())
		m.setup.setTheme(m.th)
		return m, nil
	case stateMsg:
		m.state = core.State(msg)
		m.setup.onState(m.state)
		return m, m.waitState()
	case closedMsg:
		m.disconnected = true
		return m, nil
	case tea.MouseReleaseMsg:
		if msg.Button != tea.MouseLeft {
			return m, nil
		}
		return m, m.click(msg)
	case tea.KeyPressMsg:
		return m, m.key(msg)
	case serviceDoneMsg:
		return m, m.onServiceDone(msg)
	}
	// respostas das acoes da aba Setup
	cmd := m.setup.update(msg)
	if _, ok := msg.(runDoneMsg); ok {
		// o Setup mexeu nas stacks: a aba Infra ja abre atualizada
		cmd = tea.Batch(cmd, m.stacksCmd())
	}
	return m, cmd
}

// setTab troca de aba; a Infra pergunta ao docker toda vez que abre.
func (m *model) setTab(i int) tea.Cmd {
	m.tab = i
	if i == tabInfra && !m.state.Infra.Checking {
		return m.stacksCmd()
	}
	return nil
}

func (m *model) click(msg tea.MouseMsg) tea.Cmd {
	if m.zones.Get("quit").InBounds(msg) {
		return tea.Quit
	}
	if m.disconnected {
		return nil
	}
	for i, t := range tabs {
		if m.zones.Get("tab:" + t).InBounds(msg) {
			return m.setTab(i)
		}
	}
	switch m.tab {
	case tabDashboard:
		if m.zones.Get("check").InBounds(msg) && !m.state.Environment.Checking {
			return m.checkCmd()
		}
	case tabInfra:
		return m.infraClick(msg)
	case tabSetup:
		return m.setup.click(msg)
	}
	return nil
}

func (m *model) key(msg tea.KeyPressMsg) tea.Cmd {
	k := msg.String()
	if k == "ctrl+q" || k == "ctrl+c" {
		return tea.Quit
	}
	if m.disconnected {
		if k == "enter" || k == "q" {
			return tea.Quit
		}
		return nil
	}
	// numeros trocam de aba, menos quando o foco esta num campo de texto
	typing := m.tab == tabSetup && m.setup.typing()
	if !typing && len(k) == 1 && k[0] >= '1' && int(k[0]-'1') < len(tabs) {
		return m.setTab(int(k[0] - '1'))
	}
	switch m.tab {
	case tabDashboard:
		if (k == "enter" || k == "r") && !m.state.Environment.Checking {
			return m.checkCmd()
		}
	case tabInfra:
		return m.infraKey(k)
	case tabSetup:
		return m.setup.key(msg)
	}
	return nil
}

func (m *model) View() tea.View {
	v := tea.NewView("")
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = "cais"
	if m.w == 0 {
		return v
	}
	if m.disconnected {
		v.SetContent(m.zones.Scan(m.viewDisconnected()))
		return v
	}

	header := m.viewTabs()
	footer := m.th.fg(m.th.muted).Render(" clique ou 1-6 troca de aba · " + helps[m.tab] + "ctrl+q sai")
	bodyH := m.h - lipgloss.Height(header) - lipgloss.Height(footer)

	var body string
	switch m.tab {
	case tabDashboard:
		body = m.viewDashboard(bodyH)
	case tabInfra:
		body = m.viewInfra(bodyH)
	case tabSetup:
		body = m.setup.view(m.w, bodyH)
	default:
		p := placeholders[m.tab]
		body = m.th.panel(false, m.w-2, bodyH-2).Render("\n" + m.th.bold().Render(tabs[m.tab]) + "\n\n" +
			m.th.fg(m.th.muted).Render(p.desc) + "\n\n" +
			m.th.fg(m.th.warn).Render("Em construcao -- chega na fase "+strconv.Itoa(p.phase)))
	}
	v.SetContent(m.zones.Scan(lipgloss.JoinVertical(lipgloss.Left, header, body, footer)))
	return v
}

func (m *model) viewTabs() string {
	var parts []string
	for i, t := range tabs {
		parts = append(parts, m.zones.Mark("tab:"+t, m.th.pill(i == m.tab).Render(t)), " ")
	}
	left := lipgloss.JoinHorizontal(lipgloss.Top, parts...)
	quit := m.zones.Mark("quit", m.th.dangerPill().Render("Sair"))
	gap := max(m.w-lipgloss.Width(left)-lipgloss.Width(quit), 1)
	return left + strings.Repeat(" ", gap) + quit
}

// viewDisconnected troca a tela inteira por um aviso: sem o daemon nada do
// que estava na tela e confiavel, entao nao deixa o usuario seguir.
func (m *model) viewDisconnected() string {
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(m.th.danger).
		Padding(1, 4).Align(lipgloss.Center).Render(
		m.th.fg(m.th.danger).Bold(true).Render("A conexao com o daemon do cais caiu.") + "\n\n" +
			"Veja: systemctl status cais\ne abra o cais de novo.\n\n" +
			m.zones.Mark("quit", m.th.dangerPill().Render("Sair")))
	return lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center, box)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
