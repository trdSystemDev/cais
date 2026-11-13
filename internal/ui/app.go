// Package ui e a TUI do cais (Bubble Tea + Lip Gloss), com mouse. E so uma
// visao do core: le o estado via Subscribe e dispara acoes nele, sem saber
// se o core esta no mesmo processo ou no daemon, do outro lado do socket.
package ui

import (
	"context"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/trdSystemDev/cais/internal/core"
)

// secrets ficam em cada app (aba Apps) e os bancos na aba Infra
var tabs = []string{"Dashboard", "Apps", "Infra", "Setup"}

const (
	tabDashboard = 0
	tabApps      = 1
	tabInfra     = 2
	tabSetup     = 3
)

// helps sao os atalhos de cada aba, no rodape.
var helps = map[int]string{
	tabDashboard: "enter/r verifica · ",
	tabApps:      "↑↓ app · ←→ secao · enter/tab entra no formulario · esc volta · n novo app · ",
	tabInfra:     "↑↓ card · ←→ container · espaco liga/desliga · r atualiza · ",
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
	apps         *appsPage
	db           *dbPanel // Novo banco, na aba Infra

	selectionGeneration uint64
	logSelection        logSelection
	logArea             logArea
	copiedNotice        bool
	copiedGeneration    uint64

	// aba Infra
	infraContainer                  int
	logsGeneration                  uint64
	logsTarget, logsText, logsError string
	logsOffset                      int
	infraSel                        int    // servico opcional selecionado (teclado)
	toggling                        string // servico sendo ligado/desligado daqui
	infraMsg                        string
	infraMsgC                       string
	infraMsgStack                   string
	togglingOn                      bool
}

type (
	stateMsg            core.State
	closedMsg           struct{}
	copiedNoticeExpired uint64
)

func newModel(api core.API, states <-chan core.State) *model {
	m := &model{api: api, zones: zone.New(), th: newTheme(true), states: states}
	m.setup = newSetupPage(api, m.zones, m.th)
	m.apps = newAppsPage(api, m.zones, m.th)
	m.db = newDBPanel(api, m.zones, m.th)
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
	case logSelectionScrollTick:
		return m, m.scrollLogSelection(uint64(msg))
	case copiedNoticeExpired:
		if uint64(msg) == m.copiedGeneration {
			m.copiedNotice = false
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.logSelection = logSelection{}
		m.w, m.h = msg.Width, msg.Height
		return m, nil
	case tea.BackgroundColorMsg:
		m.th = newTheme(msg.IsDark())
		m.setup.setTheme(m.th)
		m.apps.setTheme(m.th)
		m.db.setTheme(m.th)
		return m, nil
	case stateMsg:
		return m, tea.Batch(m.waitState(), m.onState(core.State(msg)))
	case closedMsg:
		m.disconnected = true
		return m, nil
	case tea.MouseClickMsg:
		if m.startLogSelection(msg) {
			return m, nil
		}
		m.logSelection = logSelection{}
		return m, nil
	case tea.MouseMotionMsg:
		if m.logSelection.dragging {
			if msg.Button != tea.MouseLeft {
				return m, m.finishLogSelection(msg.X, msg.Y)
			}
			m.moveLogSelection(msg.X, msg.Y)
			return m, m.selectionScrollCmd()
		}
		return m, nil
	case tea.MouseWheelMsg:
		m.logSelection = logSelection{}

		if m.tab == tabApps && !m.disconnected && m.zones.Get("app-logs").InBounds(msg) {
			m.scrollLogs(msg.Button == tea.MouseWheelUp)
			return m, nil
		}
		if m.tab == tabApps && !m.disconnected && m.zones.Get("app-detail").InBounds(msg) {
			m.apps.wheel(msg.Button == tea.MouseWheelUp)
			return m, nil
		}
		if m.tab != tabInfra || m.disconnected {
			return m, nil
		}
		key := "down"
		if msg.Button == tea.MouseWheelUp {
			key = "up"
		}
		if m.zones.Get("infra-logs").InBounds(msg) {
			key = "pgdown"
			if msg.Button == tea.MouseWheelUp {
				key = "pgup"
			}
		}
		return m, m.infraKey(key)
	case tea.MouseReleaseMsg:
		if m.logSelection.dragging && (msg.Button == tea.MouseLeft || msg.Button == tea.MouseNone) {
			return m, m.finishLogSelection(msg.X, msg.Y)
		}
		if msg.Button != tea.MouseLeft {
			return m, nil
		}
		return m, m.click(msg)
	case tea.KeyPressMsg:
		return m, m.key(msg)
	case tea.PasteMsg:
		// colar chega como mensagem propria, nao como teclas: vai pro campo
		// com foco (chave do GitHub, link do repositorio, secrets)
		if m.disconnected {
			return m, nil
		}
		switch m.tab {
		case tabSetup:
			return m, m.setup.paste(msg)
		case tabApps:
			return m, m.apps.paste(msg)
		case tabInfra:
			return m, m.db.paste(msg)
		}
		return m, nil
	case infraLogsMsg:
		if msg.generation != m.logsGeneration || m.tab != tabInfra || m.disconnected {
			return m, nil
		}
		m.logsText = msg.text
		m.logsError = ""
		if msg.err != nil {
			m.logsError = msg.err.Error()
		}
		generation := m.logsGeneration
		return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return infraLogsTick(generation) })
	case infraLogsTick:
		if uint64(msg) != m.logsGeneration || m.tab != tabInfra || m.disconnected {
			return m, nil
		}
		if len(m.logSelection.lines) > 0 {
			generation := uint64(msg)
			return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return infraLogsTick(generation) })
		}
		if !m.state.Infra.Checking {
			return m, tea.Batch(m.fetchInfraLogs(), m.stacksCmd())
		}
		return m, m.fetchInfraLogs()
	case serviceDoneMsg:
		return m, m.onServiceDone(msg)
	case appLogsMsg:
		return m, m.onAppLogs(msg)
	case appLogsTick:
		return m, m.onAppLogsTick(msg)
	}
	if cmd, ok := m.db.update(msg); ok {
		return m, cmd
	}
	if cmd, ok := m.apps.update(msg); ok {
		return m, tea.Batch(cmd, m.syncAppLogs(false))
	}
	// respostas das acoes da aba Setup
	cmd := m.setup.update(msg)
	if _, ok := msg.(runDoneMsg); ok {
		// o Setup mexeu nas stacks: a aba Infra ja abre atualizada
		cmd = tea.Batch(cmd, m.stacksCmd())
	}
	return m, cmd
}

// onState aplica uma foto nova do estado e devolve o que ela dispara
// (logs, variaveis e ajuda do app selecionado).
func (m *model) onState(s core.State) tea.Cmd {
	m.state = s
	m.setup.onState(m.state)
	appsCmd := m.apps.onState(m.state)
	return tea.Batch(m.syncInfraLogs(false), appsCmd, m.syncAppLogs(false))
}

// setTab troca de aba; a Infra pergunta ao docker toda vez que abre.
func (m *model) setTab(i int) tea.Cmd {
	if m.tab != i {
		m.logsGeneration++
		m.logsTarget = ""
	}
	m.logSelection = logSelection{}
	m.tab = i
	if i == tabInfra && !m.state.Infra.Checking {
		return tea.Batch(m.stacksCmd(), m.syncInfraLogs(true))
	}
	if i == tabApps {
		// bancos e buckets criados na Infra (ou pelo CLI) ja aparecem pra ligar
		return tea.Batch(m.apps.checkCmd(), m.apps.dbsCmd(), m.syncAppLogs(true))
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
	case tabApps:
		return m.appsClick(msg)
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
	typing := (m.tab == tabSetup && m.setup.typing()) || (m.tab == tabApps && m.apps.typing()) ||
		(m.tab == tabInfra && m.db.typing())
	if !typing && len(k) == 1 && k[0] >= '1' && int(k[0]-'1') < len(tabs) {
		return m.setTab(int(k[0] - '1'))
	}
	switch m.tab {
	case tabDashboard:
		if (k == "enter" || k == "r") && !m.state.Environment.Checking {
			return m.checkCmd()
		}
	case tabApps:
		return m.appsKey(msg)
	case tabInfra:
		if m.db.open() {
			return m.db.key(msg)
		}
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
	help := helps[m.tab]
	if m.tab == tabApps && m.apps.inForm {
		help = "tab/↑↓ campo · ←→ escolhe · enter/espaco ativa · esc volta pra lista · roda rola · "
	}
	footer := m.th.fg(m.th.muted).Render(" clique ou 1-" + strconv.Itoa(len(tabs)) + " troca de aba · " + help + "ctrl+q sai")
	bodyH := m.h - lipgloss.Height(header) - lipgloss.Height(footer)

	var body string
	switch m.tab {
	case tabDashboard:
		body = m.viewDashboard(bodyH)
	case tabApps:
		body = m.viewApps(bodyH)
	case tabInfra:
		body = m.viewInfra(bodyH)
	case tabSetup:
		body = m.setup.view(m.w, bodyH)
	}
	content := lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
	if m.tab == tabApps && m.apps.info != nil {
		// sem zona no Fechar: o compositor perde as marcas, e qualquer clique fecha
		box := m.th.modal(*m.apps.info, m.w, m.th.button("Fechar", btnNormal, true))
		content = lipgloss.NewCompositor(lipgloss.NewLayer(content), lipgloss.NewLayer(box).
			X(max(0, (m.w-lipgloss.Width(box))/2)).Y(max(0, (m.h-lipgloss.Height(box))/2)).Z(1)).Render()
	}
	content = m.zones.Scan(content)
	if m.copiedNotice {
		notice := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(m.th.ok).
			Background(m.th.fieldBg).Foreground(m.th.text).Padding(0, 2).Bold(true).Render("Texto copiado")
		content = lipgloss.NewCompositor(lipgloss.NewLayer(content), lipgloss.NewLayer(notice).
			X(max(0, (m.w-lipgloss.Width(notice))/2)).Y(max(0, m.h-lipgloss.Height(notice)-1)).Z(1)).Render()
	}
	v.SetContent(content)
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
