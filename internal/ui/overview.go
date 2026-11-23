package ui

import (
	"context"
	"fmt"
	"image/color"
	"math"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/databases"
)

// gravidade de um aviso ou de um estado, da pior pra melhor
type severity int

const (
	sevDanger severity = iota
	sevWarn
	sevInfo
	sevOK
	sevMuted
)

func (m *model) sevColor(s severity) color.Color {
	switch s {
	case sevDanger:
		return m.th.danger
	case sevWarn:
		return m.th.warn
	case sevInfo:
		return m.th.info
	case sevOK:
		return m.th.ok
	}
	return m.th.muted
}

// dashAlert e um aviso do dashboard: o clique leva aonde resolve (um app,
// ou uma aba).
type dashAlert struct {
	sev  severity
	text string
	app  string // abre o app na aba Apps
	tab  int    // senao, abre a aba (-1: nenhuma)
}

// limites dos avisos
const (
	diskWarn  = 85.0
	diskAlarm = 95.0
	memWarn   = 90.0
	certWarn  = 15 * 24 * time.Hour
	certAlarm = 3 * 24 * time.Hour
	// backupLate: o backup automatico passou do horario e nao rodou
	backupLate = time.Hour
)

// appHealth resume um app: a gravidade e o texto curto do estado dele.
func appHealth(a core.AppStatus) (severity, string) {
	switch {
	case a.Deploying:
		return sevInfo, "deploy em andamento"
	case a.Error != "":
		return sevDanger, firstLine(a.Error)
	case len(a.Containers) == 0 && a.Commit == "":
		return sevMuted, "sem deploy"
	case len(a.Containers) == 0:
		return sevDanger, "parado"
	}
	for _, c := range a.Containers {
		switch {
		case c.State == "restarting":
			return sevDanger, c.Service + " reiniciando sem parar"
		case c.State != "running":
			return sevDanger, fmt.Sprintf("%s parado (%s, saida %d)", c.Service, c.State, c.ExitCode)
		case c.Health == "unhealthy":
			return sevDanger, c.Service + " com healthcheck falhando"
		case c.Health == "starting":
			return sevWarn, c.Service + " subindo"
		}
	}
	return sevOK, "no ar"
}

// alerts sao os avisos do que precisa de atencao, do mais grave pro menos.
// So entra o que ja foi checado: nada de alarme falso antes da primeira
// leitura.
func (m *model) alerts(now time.Time) []dashAlert {
	st := m.state
	var out []dashAlert
	add := func(sev severity, app string, tab int, format string, args ...any) {
		out = append(out, dashAlert{sev: sev, text: fmt.Sprintf(format, args...), app: app, tab: tab})
	}

	if e := st.Environment; e.Checked() && !e.Checking && !e.Docker.OK() {
		add(sevDanger, "", -1, "Docker com problema: veja o Ambiente, embaixo")
	}
	if !st.Apps.CheckedAt.IsZero() {
		for _, a := range st.Apps.List {
			if sev, text := appHealth(a); sev == sevDanger {
				add(sev, a.Name, tabApps, "App %s: %s", a.Name, text)
			}
			if l := a.Last; l != nil && !l.OK() && !a.Deploying {
				what := "falhou"
				if l.RolledBack {
					what = "falhou (a versao anterior continua no ar)"
				}
				add(sevWarn, a.Name, tabApps, "Ultimo deploy do %s %s", a.Name, what)
			}
		}
	}
	if !st.Infra.CheckedAt.IsZero() && !st.Infra.Checking && !st.Setup.Running {
		for _, s := range st.Infra.Stacks {
			if s.Enabled && !s.Healthy() {
				add(sevDanger, "", tabInfra, "Servico %s fora do ar", s.Name)
			}
		}
	}

	b := st.Backup
	switch {
	case b.Last != nil && !b.Last.OK() && !b.Running:
		add(sevDanger, "", tabSetup, "Ultimo backup falhou (%s)", b.Last.At.Local().Format("02/01 15:04"))
	case !b.Ready():
		add(sevWarn, "", tabSetup, "Backups nao configurados: os bancos nao saem da VPS (Setup, Backups)")
	case b.Time == "":
		add(sevWarn, "", tabSetup, "Backup automatico desligado: so o manual (Setup, Backups)")
	case !b.Running && !b.Next.IsZero() && now.Sub(b.Next) > backupLate:
		add(sevDanger, "", tabSetup, "Backup atrasado: era pra ter rodado %s", b.Next.Local().Format("02/01 15:04"))
	}

	if s := m.sys; !s.At.IsZero() && s.Error == "" {
		for _, d := range s.Now.Disks {
			if p := d.Percent(); p >= diskWarn {
				sev := sevWarn
				if p >= diskAlarm {
					sev = sevDanger
				}
				add(sev, "", -1, "Disco %s com %.0f%% usado (%s livres)", d.Path, p, size(d.Total-d.Used))
			}
		}
		if p := s.Now.MemPercent(); p >= memWarn {
			add(sevWarn, "", -1, "Memoria com %.0f%% usada", p)
		}
	}

	h := st.Health
	if h.CertsError != "" {
		add(sevWarn, "", tabInfra, "Nao deu pra ler os certificados do proxy: %s", h.CertsError)
	}
	for _, c := range h.Certs {
		left := c.NotAfter.Sub(now)
		switch {
		case left <= 0:
			add(sevDanger, "", tabInfra, "Certificado de %s venceu em %s", c.Domain, c.NotAfter.Local().Format("02/01"))
		case left < certAlarm:
			add(sevDanger, "", tabInfra, "Certificado de %s vence %s", c.Domain, until(left))
		case left < certWarn:
			add(sevWarn, "", tabInfra, "Certificado de %s vence %s (o Traefik renova sozinho; se nao renovar, confira o DNS)", c.Domain, until(left))
		}
	}
	if h.UpdateAvailable() {
		add(sevInfo, "", -1, "Versao nova do cais: %s (rodando %s). Atualize rodando o install.sh de novo na VPS", h.Latest, h.Version)
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].sev < out[j].sev })
	return out
}

// maxAlerts e quantos avisos aparecem; o resto vira "+N".
const maxAlerts = 4

func (m *model) renderAlerts(w int) string {
	t := m.th
	list := m.alerts(time.Now())
	if len(list) == 0 {
		if m.state.Apps.CheckedAt.IsZero() {
			return t.fg(t.muted).Render("Conferindo apps e servicos...") + "\n"
		}
		return t.fg(t.ok).Bold(true).Render("✓ Tudo certo") + t.fg(t.muted).Render(": apps e servicos no ar, backup em dia, nada pedindo atencao.") + "\n"
	}
	icons := map[severity]string{sevDanger: "✗", sevWarn: "!", sevInfo: "i"}
	var b strings.Builder
	for i, a := range list {
		if i == maxAlerts {
			b.WriteString(t.fg(t.muted).Render(fmt.Sprintf("  +%d avisos", len(list)-maxAlerts)) + "\n")
			break
		}
		c := m.sevColor(a.sev)
		line := t.fg(c).Bold(true).Render(icons[a.sev]+" ") + t.fg(c).Render(a.text)
		if lipgloss.Width(line) > w {
			line = t.fg(c).Bold(true).Render(icons[a.sev]+" ") + t.fg(c).Render(truncate(a.text, w-3))
		}
		if a.app != "" || a.tab >= 0 {
			line = m.zones.Mark(fmt.Sprintf("alert:%d", i), line)
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// maxDashApps e quantos apps a visao geral lista.
const maxDashApps = 6

// renderOverview: os apps na esquerda; servicos, backup e ultimos deploys
// na direita.
func (m *model) renderOverview(w int) string {
	t := m.th
	muted := t.fg(t.muted)
	st := m.state
	now := time.Now()
	colW := (w - 4) / 2

	// apps
	var left strings.Builder
	up, bad := 0, 0
	for _, a := range st.Apps.List {
		switch sev, _ := appHealth(a); sev {
		case sevOK:
			up++
		case sevDanger:
			bad++
		}
	}
	summary := fmt.Sprintf("%d no ar", up)
	if bad > 0 {
		summary += fmt.Sprintf(" · %d com problema", bad)
	}
	if len(st.Apps.List) == 0 {
		summary = "nenhum (crie na aba Apps)"
	}
	left.WriteString(t.bold().Render("Apps") + "  " + muted.Render(summary))
	nameW := 4
	for _, a := range st.Apps.List {
		nameW = max(nameW, min(20, len(a.Name)))
	}
	// os com problema primeiro
	list := append([]core.AppStatus(nil), st.Apps.List...)
	sort.SliceStable(list, func(i, j int) bool {
		si, _ := appHealth(list[i])
		sj, _ := appHealth(list[j])
		return si < sj
	})
	for i, a := range list {
		if i == maxDashApps {
			left.WriteString("\n" + muted.Render(fmt.Sprintf("  +%d apps", len(list)-maxDashApps)))
			break
		}
		sev, text := appHealth(a)
		line := "  " + t.fg(m.sevColor(sev)).Render("●") + " " + fmt.Sprintf("%-*s", nameW, truncate(a.Name, nameW)) + "  " +
			t.fg(m.sevColor(sev)).Render(text)
		if a.Commit != "" && sev != sevDanger {
			line += muted.Render("  " + shortCommit(a.Commit))
			if a.Last != nil && a.Last.OK() {
				line += muted.Render(" · " + ago(now, a.Last.At))
			}
		}
		left.WriteString("\n" + m.zones.Mark("dash-app:"+a.Name, truncate(line, colW)))
	}

	// servicos
	var right strings.Builder
	right.WriteString(t.bold().Render("Servicos") + "  ")
	var svcs []string
	for _, s := range st.Infra.Stacks {
		if !s.Enabled {
			continue
		}
		mark := t.fg(t.ok).Render("✓")
		if st.Infra.CheckedAt.IsZero() || st.Infra.Checking {
			mark = muted.Render("…")
		} else if !s.Healthy() {
			mark = t.fg(t.danger).Render("✗")
		}
		svcs = append(svcs, mark+" "+s.Name)
	}
	if len(svcs) == 0 {
		right.WriteString(muted.Render("nenhum ligado (aba Infra)"))
	}
	right.WriteString(strings.Join(svcs, "  ") + "\n")

	// backup
	b := st.Backup
	right.WriteString(t.bold().Render("Backup") + "    ")
	switch {
	case b.Running:
		right.WriteString(t.fg(t.info).Render("fazendo agora: " + cmpOrStr(b.Step, "...")))
	case !b.Ready():
		right.WriteString(t.fg(t.warn).Render("nao configurado"))
	case b.Last == nil:
		right.WriteString(muted.Render("nenhum feito ainda"))
	case b.Last.OK():
		right.WriteString(t.fg(t.ok).Render("✓ "+ago(now, b.Last.At)) + muted.Render(" · "+databases.HumanSize(b.Last.Size)))
	default:
		right.WriteString(t.fg(t.danger).Render("✗ falhou " + ago(now, b.Last.At)))
	}
	if !b.Next.IsZero() && !b.Running {
		right.WriteString(muted.Render(" · proximo " + b.Next.Local().Format("02/01 15:04")))
	}
	right.WriteString("\n")

	// ultimos deploys de todos os apps
	type dep struct {
		app string
		at  time.Time
		ok  bool
		why string
		sha string
	}
	var deps []dep
	for _, a := range st.Apps.List {
		for _, d := range a.History {
			why := ""
			switch {
			case !d.OK() && d.RolledBack:
				why = "falhou, voltou"
			case !d.OK():
				why = "falhou"
			case d.Rollback:
				why = "rollback"
			case d.PromotedFrom != "":
				why = "promovido"
			case d.Auto:
				why = "automatico"
			}
			deps = append(deps, dep{app: a.Name, at: d.At, ok: d.OK(), why: why, sha: shortCommit(d.Commit)})
		}
	}
	sort.Slice(deps, func(i, j int) bool { return deps[i].at.After(deps[j].at) })
	right.WriteString(t.bold().Render("Ultimos deploys"))
	if len(deps) == 0 {
		right.WriteString("  " + muted.Render("nenhum"))
	}
	for i, d := range deps {
		if i == 3 {
			break
		}
		mark := t.fg(t.ok).Render("✓")
		if !d.ok {
			mark = t.fg(t.danger).Render("✗")
		}
		line := "  " + mark + " " + fmt.Sprintf("%-*s", nameW, truncate(d.app, nameW)) + "  " + d.sha + muted.Render("  "+ago(now, d.at))
		if d.why != "" {
			line += muted.Render(" · " + d.why)
		}
		right.WriteString("\n" + m.zones.Mark("dash-app:"+d.app, truncate(line, colW)))
	}

	return lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(colW+4).Render(left.String()), right.String()) + "\n"
}

// dashClick: avisos e apps levam aonde resolve.
func (m *model) dashClick(msg tea.MouseMsg) tea.Cmd {
	if m.zones.Get("check").InBounds(msg) && !m.state.Environment.Checking {
		return tea.Batch(m.checkCmd(), m.dashRefresh(true))
	}
	for i, a := range m.alerts(time.Now()) {
		if i == maxAlerts {
			break
		}
		if m.zones.Get(fmt.Sprintf("alert:%d", i)).InBounds(msg) {
			switch {
			case a.app != "":
				return m.openApp(a.app)
			case a.tab >= 0:
				return m.setTab(a.tab)
			}
		}
	}
	for _, a := range m.state.Apps.List {
		if m.zones.Get("dash-app:" + a.Name).InBounds(msg) {
			return m.openApp(a.Name)
		}
	}
	return nil
}

// openApp abre o app na aba Apps.
func (m *model) openApp(name string) tea.Cmd {
	p := m.apps
	p.creating, p.inForm, p.focus = false, false, 0
	p.pendingSel = name
	for i, a := range p.list {
		if a.Name == name {
			p.sel = i
		}
	}
	return tea.Batch(m.setTab(tabApps), p.loadSelected(false))
}

// dashRefresh pede ao core o que a visao geral mostra: apps, servicos,
// backup e (com intervalo minimo no core) certificados e versao nova.
func (m *model) dashRefresh(all bool) tea.Cmd {
	cmds := []tea.Cmd{m.apps.checkCmd(), m.backupRefreshCmd()}
	if !m.state.Infra.Checking && !m.state.Setup.Running {
		cmds = append(cmds, m.stacksCmd())
	}
	if all {
		cmds = append(cmds, m.healthCmd())
	}
	return tea.Batch(cmds...)
}

func (m *model) healthCmd() tea.Cmd {
	return func() tea.Msg {
		m.api.CheckHealth(context.Background())
		return nil
	}
}

// ago e quanto tempo faz, curto: agora, ha 5 min, ha 3h, ha 2 dias.
func ago(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "agora"
	case d < time.Hour:
		return fmt.Sprintf("ha %d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("ha %dh", int(d.Hours()))
	}
	return fmt.Sprintf("ha %d dias", int(d.Hours())/24)
}

// until e quanto falta, curto: em 5h, em 3 dias.
func until(d time.Duration) string {
	if d < 48*time.Hour {
		return fmt.Sprintf("em %dh", int(math.Ceil(d.Hours())))
	}
	return fmt.Sprintf("em %d dias", int(math.Ceil(d.Hours()/24)))
}

// truncate corta s (com estilo) em w colunas.
func truncate(s string, w int) string { return ansi.Truncate(s, max(1, w), "…") }
