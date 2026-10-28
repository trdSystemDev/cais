package ui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/trdSystemDev/cais/internal/core"
)

// dbPanel e o "Novo banco" da aba Infra: cria database e usuario com senha
// nova no Postgres ou MySQL e mostra a credencial pra por no app. Fica no
// lugar dos logs enquanto esta aberto.
type dbPanel struct {
	api   core.API
	zones *zone.Manager
	th    theme

	engine  string // aberto quando nao vazio
	name    *textinput.Model
	exts    *textinput.Model
	app     *textinput.Model // opcional: ja liga no app
	env     *textinput.Model
	focus   int
	busy    bool
	err     string
	created *core.DatabaseInfo
	list    []core.DatabaseInfo
}

type (
	dbCreatedMsg struct {
		info core.DatabaseInfo
		err  error
	}
	dbListMsg struct {
		list []core.DatabaseInfo
		err  error
	}
)

func newDBPanel(api core.API, zones *zone.Manager, th theme) *dbPanel {
	p := &dbPanel{api: api, zones: zones}
	mk := func(placeholder string) *textinput.Model {
		ti := textinput.New()
		ti.Prompt = ""
		ti.Placeholder = placeholder
		ti.SetWidth(28)
		return &ti
	}
	p.name = mk("nome (ex: meu_app)")
	p.exts = mk("ex: postgis (opcional)")
	p.app = mk("opcional: ja liga no app")
	p.env = mk("DATABASE_URL")
	p.setTheme(th)
	return p
}

func (p *dbPanel) setTheme(th theme) {
	p.th = th
	for _, ti := range []*textinput.Model{p.name, p.exts, p.app, p.env} {
		s := textinput.DefaultStyles(true)
		for _, st := range []*textinput.StyleState{&s.Focused, &s.Blurred} {
			st.Text = lipgloss.NewStyle().Foreground(th.fieldFg).Background(th.fieldBg)
			st.Placeholder = lipgloss.NewStyle().Foreground(th.muted).Background(th.fieldBg).Italic(true)
		}
		s.Cursor.Color = th.accent
		ti.SetStyles(s)
	}
}

func (p *dbPanel) open() bool { return p.engine != "" }

// ids na ordem do Tab; extensoes so no Postgres
func (p *dbPanel) ids() []string {
	if p.engine == "postgres" {
		return []string{"db-name", "db-exts", "db-app", "db-env", "db-create", "db-close"}
	}
	return []string{"db-name", "db-app", "db-env", "db-create", "db-close"}
}

func (p *dbPanel) input(id string) *textinput.Model {
	switch id {
	case "db-name":
		return p.name
	case "db-exts":
		return p.exts
	case "db-app":
		return p.app
	case "db-env":
		return p.env
	}
	return nil
}

func (p *dbPanel) typing() bool { return p.open() && p.input(p.ids()[p.focus]) != nil }

func (p *dbPanel) setFocus(i int) tea.Cmd {
	ids := p.ids()
	p.focus = ((i % len(ids)) + len(ids)) % len(ids)
	var cmd tea.Cmd
	for j, id := range ids {
		if ti := p.input(id); ti != nil {
			if j == p.focus {
				cmd = ti.Focus()
			} else {
				ti.Blur()
			}
		}
	}
	return cmd
}

func (p *dbPanel) show(engine string) tea.Cmd {
	p.engine, p.err, p.created, p.busy, p.list = engine, "", nil, false, nil
	for _, ti := range []*textinput.Model{p.name, p.exts, p.app, p.env} {
		ti.SetValue("")
	}
	return tea.Batch(p.setFocus(0), p.listCmd())
}

func (p *dbPanel) close() {
	p.engine = ""
	for _, ti := range []*textinput.Model{p.name, p.exts, p.app, p.env} {
		ti.Blur()
	}
}

func (p *dbPanel) listCmd() tea.Cmd {
	return func() tea.Msg {
		list, err := p.api.Databases(context.Background())
		return dbListMsg{list, err}
	}
}

func (p *dbPanel) create() tea.Cmd {
	if p.busy {
		return nil
	}
	req := core.DatabaseRequest{Engine: p.engine, Name: strings.TrimSpace(p.name.Value()),
		App: strings.TrimSpace(p.app.Value()), Env: strings.TrimSpace(p.env.Value())}
	if p.engine == "postgres" {
		req.Extensions = splitComma(p.exts.Value())
	}
	if req.Name == "" {
		p.err = "Informe o nome do database."
		return nil
	}
	p.busy, p.err, p.created = true, "", nil
	return func() tea.Msg {
		info, err := p.api.CreateDatabase(context.Background(), req)
		return dbCreatedMsg{info, err}
	}
}

func splitComma(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func (p *dbPanel) press(id string) tea.Cmd {
	switch id {
	case "db-create":
		return p.create()
	case "db-close":
		p.close()
	}
	return nil
}

func (p *dbPanel) update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case dbCreatedMsg:
		p.busy = false
		if msg.err != nil {
			p.err = msg.err.Error()
			return nil, true
		}
		p.created = &msg.info
		for _, ti := range []*textinput.Model{p.name, p.exts, p.app, p.env} {
			ti.SetValue("")
		}
		return p.listCmd(), true
	case dbListMsg:
		if msg.err == nil {
			p.list = msg.list
		}
		return nil, true
	}
	return nil, false
}

func (p *dbPanel) key(msg tea.KeyPressMsg) tea.Cmd {
	id := p.ids()[p.focus]
	switch k := msg.String(); k {
	case "esc":
		p.close()
		return nil
	case "tab", "down":
		return p.setFocus(p.focus + 1)
	case "shift+tab", "up":
		return p.setFocus(p.focus - 1)
	case "enter":
		if p.input(id) != nil {
			return p.setFocus(p.focus + 1)
		}
		return p.press(id)
	case "space":
		if p.input(id) == nil {
			return p.press(id)
		}
	}
	if ti := p.input(id); ti != nil {
		v, cmd := ti.Update(msg)
		*ti = v
		return cmd
	}
	return nil
}

func (p *dbPanel) paste(msg tea.PasteMsg) tea.Cmd {
	if ti := p.input(p.ids()[p.focus]); ti != nil && p.open() {
		v, cmd := ti.Update(msg)
		*ti = v
		return cmd
	}
	return nil
}

func (p *dbPanel) click(msg tea.MouseMsg) tea.Cmd {
	for i, id := range p.ids() {
		if p.zones.Get(id).InBounds(msg) {
			cmd := p.setFocus(i)
			if p.input(id) == nil {
				return tea.Batch(cmd, p.press(id))
			}
			return cmd
		}
	}
	return nil
}

func (p *dbPanel) view(w, h int) string {
	t := p.th
	muted := t.fg(t.muted)
	wrap := lipgloss.NewStyle().Width(w - 6)
	var b strings.Builder
	b.WriteString(t.bold().Render("Novo banco no "+p.engine) + "\n")
	b.WriteString(wrap.Inherit(muted).Render("Cria o database e um usuario com senha nova, que so acessa esse database. "+
		"O app chega pela rede "+p.engine+"_network.") + "\n\n")
	field := lipgloss.NewStyle().Background(t.fieldBg).Foreground(t.fieldFg).Width(29)
	var buttons []string
	for i, id := range p.ids() {
		focused := i == p.focus
		if ti := p.input(id); ti != nil {
			label := map[string]string{"db-name": "Database", "db-exts": "Extensoes", "db-app": "App", "db-env": "Variavel"}[id]
			marker, ls := "  ", t.fg(t.text).Width(12)
			if focused {
				marker, ls = t.fg(t.accent).Bold(true).Render("› "), t.fg(t.accent).Bold(true).Width(12)
			}
			b.WriteString(p.zones.Mark(id, marker+ls.Render(label)+field.Render(ti.View())) + "\n")
			continue
		}
		label := map[string]string{"db-create": "Criar", "db-close": "Fechar"}[id]
		st := t.pill(focused)
		if p.busy && id == "db-create" {
			st, label = t.disabledPill(), "Criando..."
		}
		buttons = append(buttons, p.zones.Mark(id, st.Render(label)))
	}
	b.WriteString("\n  " + strings.Join(buttons, " ") + "\n")
	if p.err != "" {
		b.WriteString("\n" + wrap.Foreground(t.danger).Render(p.err) + "\n")
	}
	if c := p.created; c != nil {
		msg := "Database " + c.Name + " criado. A senha fica guardada; copie pro app:"
		for _, u := range c.Apps {
			msg = "Database " + c.Name + " criado e ligado ao app " + u.App + " (" + u.Env + " no proximo deploy):"
		}
		b.WriteString("\n" + t.fg(t.ok).Bold(true).Render(msg) + "\n")
		writeDB(&b, *c, t, w)
	}
	var mine []core.DatabaseInfo
	for _, d := range p.list {
		if d.Engine == p.engine && (p.created == nil || d.Name != p.created.Name) {
			mine = append(mine, d)
		}
	}
	if len(mine) > 0 {
		b.WriteString("\n" + t.bold().Render("Ja criados") + "\n")
		for _, d := range mine {
			writeDB(&b, d, t, w)
		}
	}
	return t.panel(true, w-2, h).MaxHeight(h + 2).Render(b.String())
}

func writeDB(b *strings.Builder, d core.DatabaseInfo, t theme, w int) {
	wrap := lipgloss.NewStyle().Width(w - 6)
	fmt.Fprintf(b, "  %-9s %s  usuario %s  senha %s\n", d.Name, t.fg(t.muted).Render(fmt.Sprintf("%s:%d", d.Host, d.Port)), d.User, d.Password)
	for _, u := range d.Apps {
		fmt.Fprintf(b, "  %-9s app %s (%s)\n", "", u.App, u.Env)
	}
	b.WriteString(wrap.Foreground(t.accent).Render("  "+d.URL) + "\n")
}
