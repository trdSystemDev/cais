package ui

import (
	"context"
	"image/color"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/stacks"
)

// Secoes do servico selecionado na aba Infra, como as do app na aba Apps.
var infraSections = []string{"Geral", "Logs"}

const (
	infraGeneral = 0
	infraLogs    = 1
)

// serviceCtlMsg e a volta do Iniciar, Parar ou Reiniciar de um servico.
type serviceCtlMsg struct {
	name, action string
	err          error
}

// running diz se algum container do servico esta rodando (o botao vira
// Parar; sem nenhum, Iniciar).
func running(st core.StackStatus) bool {
	for _, c := range st.Containers {
		if c.State == "running" || c.State == "restarting" {
			return true
		}
	}
	return false
}

// stopped: ligado e instalado, com containers, todos parados.
func stopped(st core.StackStatus) bool {
	return st.Enabled && st.Installed && len(st.Containers) > 0 && !running(st)
}

// controlService inicia, para ou reinicia os containers do servico ligado
// (a config nao muda).
func (m *model) controlService(name, action string) tea.Cmd {
	if m.busy() {
		return nil
	}
	m.controlling, m.controlAction = name, action
	m.infraMsgStack = name
	verb := map[string]string{core.ServiceStart: "Iniciando", core.ServiceStop: "Parando", core.ServiceRestart: "Reiniciando"}[action]
	m.infraMsgC, m.infraMsg = "warn", verb+" "+name+"..."
	return func() tea.Msg {
		return serviceCtlMsg{name, action, m.api.ControlService(context.Background(), name, action)}
	}
}

func (m *model) onServiceCtl(msg serviceCtlMsg) tea.Cmd {
	m.controlling, m.controlAction = "", ""
	m.infraMsgStack = msg.name
	switch {
	case msg.err != nil:
		m.infraMsgC, m.infraMsg = "danger", firstLine(msg.err.Error())
	case msg.action == core.ServiceStop:
		m.infraMsgC, m.infraMsg = "ok", msg.name+" parado (continua ligado: Iniciar volta)."
	case msg.action == core.ServiceStart:
		m.infraMsgC, m.infraMsg = "ok", msg.name+" iniciado."
	default:
		m.infraMsgC, m.infraMsg = "ok", msg.name+" reiniciado."
	}
	return tea.Batch(m.stacksCmd(), m.syncInfraLogs(true))
}

// serviceActions sao os botoes do servico selecionado, na ordem do Tab.
func (m *model) serviceActions(st core.StackStatus) []string {
	ids := []string{"toggle:" + st.Name}
	if st.Enabled && m.needsApply(st.Name) {
		ids = append(ids, "apply:"+st.Name)
	}
	if st.Enabled && st.Installed {
		ids = append(ids, "ctl:"+st.Name, "restart:"+st.Name)
	}
	return ids
}

// subIDs sao os campos do painel do servico (Novo banco, Novo bucket ou
// Portas), depois dos botoes.
func (m *model) subIDs() []string {
	switch {
	case m.db.open():
		return m.db.ids()
	case m.ports.shown:
		return portsIDs
	}
	return nil
}

// infraPos e a posicao do foco no formulario do Geral: os botoes do
// servico e depois o painel dele.
func (m *model) infraPos() int {
	if m.infraInSub {
		focus := m.db.focus
		if m.ports.shown {
			focus = m.ports.focus
		}
		return len(m.serviceActions(m.selectedStack())) + focus
	}
	return m.infraFocus
}

func (m *model) setInfraPos(i int) tea.Cmd {
	actions, sub := m.serviceActions(m.selectedStack()), m.subIDs()
	n := len(actions) + len(sub)
	i = ((i % n) + n) % n
	if i < len(actions) {
		m.infraInSub, m.infraFocus = false, i
		m.db.blur()
		m.ports.blur()
		return nil
	}
	m.infraInSub = true
	if m.ports.shown {
		m.ports.active = true
		return m.ports.setFocus(i - len(actions))
	}
	m.db.active = true
	return m.db.setFocus(i - len(actions))
}

// leaveInfraForm devolve o teclado pros cards.
func (m *model) leaveInfraForm() {
	m.infraForm, m.infraInSub, m.infraFocus = false, false, 0
	m.db.blur()
	m.ports.blur()
}

// pressAction e o clique ou enter num botao do servico.
func (m *model) pressAction(id string) tea.Cmd {
	kind, name, _ := strings.Cut(id, ":")
	switch kind {
	case "toggle":
		return m.toggle(name)
	case "apply":
		return m.apply(name)
	case "ctl":
		if running(m.selectedStack()) {
			return m.controlService(name, core.ServiceStop)
		}
		return m.controlService(name, core.ServiceStart)
	case "restart":
		return m.controlService(name, core.ServiceRestart)
	}
	return nil
}

// syncInfraPanels abre no Geral o painel do servico selecionado: Novo
// banco ou Novo bucket (servico ligado e no ar) ou as Portas (proxy).
func (m *model) syncInfraPanels() tea.Cmd {
	st := m.selectedStack()
	wantDB := ""
	if (st.Name == "postgres" || st.Name == "mysql" || st.Name == "garage") && st.Enabled && st.Installed {
		wantDB = st.Name
	}
	var cmd tea.Cmd
	if m.db.engine != wantDB {
		if wantDB == "" {
			m.db.close()
		} else {
			cmd = m.db.show(wantDB)
		}
		m.infraInSub = false
	}
	if wantPorts := st.Name == "proxy"; m.ports.shown != wantPorts {
		if wantPorts {
			m.ports.show(m.setup.plan.Config.Proxy)
		} else {
			m.ports.close()
		}
		m.infraInSub = false
	}
	if n := len(m.serviceActions(st)); !m.infraInSub && m.infraFocus >= n {
		m.infraFocus = max(0, n-1)
	}
	return cmd
}

// selectInfra seleciona o card i (a secao aberta continua).
func (m *model) selectInfra(i int) tea.Cmd {
	if i != m.infraSel {
		m.leaveInfraForm()
		m.infraSel, m.infraContainer = i, 0
	}
	return tea.Batch(m.syncInfraPanels(), m.syncInfraLogs(true))
}

func (m *model) viewSectionPills() string {
	var parts []string
	for i, s := range infraSections {
		parts = append(parts, m.zones.Mark("infra-sec:"+s, m.th.pill(i == m.infraSection).Render(s)), " ")
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

// viewInfraHead e o titulo do servico com o estado e as secoes.
func (m *model) viewInfraHead(st core.StackStatus, w int) string {
	t := m.th
	mark, status, c := m.stackStatus(st)
	return ansi.Truncate(t.bold().Render(st.Name)+"  "+t.fg(c).Render(mark+" "+status), w, "…") + "\n" + m.viewSectionPills() + "\n"
}

// viewInfraGeneral e a secao Geral: os botoes do servico e o painel dele.
func (m *model) viewInfraGeneral(w, h int) string {
	t := m.th
	inner := w - 6
	wrap := lipgloss.NewStyle().Width(inner)
	muted := t.fg(t.muted)
	st := m.selectedStack()
	def, _ := stacks.Get(st.Name)

	top := m.viewInfraHead(st, inner) + "\n"
	var b strings.Builder
	b.WriteString(wrap.Inherit(muted).Render(def.Desc) + "\n\n")

	focusID := ""
	if m.infraForm && !m.infraInSub {
		actions := m.serviceActions(st)
		focusID = actions[min(m.infraFocus, len(actions)-1)]
	}
	btn := func(id, label string, k btnKind) string {
		if m.busy() {
			k = btnOff
		}
		return m.zones.Mark(id, t.button(label, k, id == focusID))
	}
	parts := []string{"  "}
	for _, id := range m.serviceActions(st) {
		var label string
		var k btnKind
		switch kind, _, _ := strings.Cut(id, ":"); kind {
		case "toggle":
			label, k = "Ligar", btnNormal
			if st.Enabled {
				label, k = "Desligar", btnDanger
			}
		case "apply":
			label, k = "Aplicar", btnNormal
		case "ctl":
			label, k = "Iniciar", btnNormal
			if running(st) {
				label, k = "Parar", btnWarn
			}
		case "restart":
			label, k = "Reiniciar", btnInfo
		}
		if len(parts) > 1 {
			parts = append(parts, " ")
		}
		parts = append(parts, btn(id, label, k))
	}
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, parts...) + "\n")
	if _, detail, changing := m.serviceOperation(st.Name); changing && detail != "" {
		b.WriteString(wrap.Foreground(t.warn).Render("◌ "+detail) + "\n")
	}
	if m.infraMsg != "" && m.infraMsgStack == st.Name {
		c := map[string]lipgloss.Style{"ok": t.fg(t.ok), "danger": t.fg(t.danger)}[m.infraMsgC]
		if m.infraMsgC != "ok" && m.infraMsgC != "danger" {
			c = t.fg(t.warn)
		}
		b.WriteString(wrap.Inherit(c).Render(m.infraMsg) + "\n")
	}
	switch {
	case !st.Enabled:
		b.WriteString(wrap.Inherit(muted).Render("Desligado: Ligar grava na config e sobe o servico.") + "\n")
	case m.needsApply(st.Name):
		b.WriteString(wrap.Inherit(muted).Render("A config salva muda este servico (ou ele ainda nao subiu): Aplicar sobe de novo com ela.") + "\n")
	}
	if st.Enabled && st.Installed {
		b.WriteString(wrap.Inherit(muted).Render("Parar e Reiniciar: so os containers, continua ligado. "+
			"Desligar: tira da config e derruba os containers (os volumes ficam).") + "\n")
		if st.Name == "postgres" || st.Name == "mysql" {
			b.WriteString(wrap.Foreground(t.warn).Render("Parado, os apps que usam o banco caem e o backup falha.") + "\n")
		}
	}

	if st.Name == "garage" && st.Enabled {
		b.WriteString("\n" + muted.Render(ansi.Truncate("S3: https://"+m.setup.plan.Config.S3Host()+" · regiao garage", inner, "…")) + "\n")
		b.WriteString(wrap.Foreground(t.warn).Render("Os arquivos dos buckets nao entram no backup do cais.") + "\n")
	}
	var sub string
	switch {
	case m.db.open():
		sub = m.db.view(inner)
	case m.ports.shown:
		m.ports.fill(m.setup.plan.Config.Proxy)
		sub = m.ports.view(m.setup.plan.Config, st.Enabled, inner)
	case (st.Name == "postgres" || st.Name == "mysql" || st.Name == "garage") && st.Enabled:
		what := map[string]string{"postgres": "Novo banco", "mysql": "Novo banco", "garage": "Novo bucket"}[st.Name]
		b.WriteString("\n" + wrap.Inherit(muted).Render(what+" aparece aqui quando o servico estiver no ar.") + "\n")
	}
	if !m.infraForm && sub == "" {
		b.WriteString("\n" + muted.Render("enter ou tab entra nos botoes · ←→ secao") + "\n")
	}
	if sub != "" {
		sub = t.rule(inner) + "\n" + sub
	}
	return t.panel(m.infraForm, w-2, h).MaxHeight(h + 2).Render(fitGeneral(top, b.String(), sub, m.infraInSub, h))
}

// fitGeneral junta o Geral na altura h: com o foco no painel do servico
// (Novo banco, Portas) e sem espaco, os botoes do servico saem da tela; se
// ainda nao cabe, fica o fim do painel (a confirmacao de excluir, o
// resultado), com o titulo e as secoes sempre no topo.
func fitGeneral(top, mid, sub string, inSub bool, h int) string {
	lines := func(s string) []string {
		if s == "" {
			return nil
		}
		return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	}
	head, body := lines(top), append(lines(mid), append([]string{""}, lines(sub)...)...)
	if sub == "" {
		body = lines(mid)
	}
	if len(head)+len(body) > h && inSub {
		body = lines(sub)
	}
	if avail := max(1, h-len(head)); len(body) > avail {
		body = body[len(body)-avail:]
	}
	return strings.Join(append(head, body...), "\n")
}

// stackStatus e o estado curto do servico (card e titulo).
func (m *model) stackStatus(st core.StackStatus) (string, string, color.Color) {
	t := m.th
	if m.controlling == st.Name {
		return "◌", map[string]string{core.ServiceStart: "iniciando...", core.ServiceStop: "parando...", core.ServiceRestart: "reiniciando..."}[m.controlAction], t.warn
	}
	if on, _, changing := m.serviceOperation(st.Name); changing {
		if on {
			return "◌", "ligando...", t.warn
		}
		return "◌", "desligando...", t.warn
	}
	switch {
	case st.Error != "":
		return "✗", "erro", t.danger
	case !st.Enabled:
		return "·", "desligado", t.muted
	case !st.Installed:
		return "○", "ainda nao subiu", t.muted
	case stopped(st):
		return "■", "parado", t.muted
	case st.Healthy():
		return "✓", "no ar", t.ok
	}
	return "✗", "com problema", t.danger
}
