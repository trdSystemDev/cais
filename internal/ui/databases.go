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
	"github.com/trdSystemDev/cais/internal/databases"
)

// dbPanel e o "Novo banco" (Postgres/MySQL) e o "Novo bucket" (Garage) da
// aba Infra: cria o database com usuario e senha nova, ou o bucket com uma
// chave so dele, e lista os ja criados. O app recebe a credencial pelos
// secrets ao ligar (Apps -> Secrets). Fica no lugar dos logs enquanto esta
// aberto.
type dbPanel struct {
	api   core.API
	zones *zone.Manager
	th    theme

	engine  string // postgres, mysql ou garage; aberto quando nao vazio
	name    *textinput.Model
	exts    *textinput.Model
	focus   int
	busy    bool
	err     string
	created *infraRow
	list    []infraRow
}

// infraRow e um database ou bucket na lista: nome, secrets que o app
// recebe, apps ligados e espaco ocupado.
type infraRow struct {
	name    string
	secrets []string
	apps    []string
	size    string
}

type (
	dbCreatedMsg struct {
		row infraRow
		err error
	}
	dbListMsg struct {
		engine string
		list   []infraRow
		err    error
	}
)

func (p *dbPanel) buckets() bool { return p.engine == "garage" }

func newDBPanel(api core.API, zones *zone.Manager, th theme) *dbPanel {
	p := &dbPanel{api: api, zones: zones}
	mk := func(placeholder string) *textinput.Model {
		ti := textinput.New()
		ti.Prompt = ""
		ti.Placeholder = placeholder
		ti.SetWidth(28)
		return &ti
	}
	p.name = mk("")
	p.exts = mk("ex: postgis (opcional)")
	p.setTheme(th)
	return p
}

func (p *dbPanel) setTheme(th theme) {
	p.th = th
	for _, ti := range []*textinput.Model{p.name, p.exts} {
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
		return []string{"db-name", "db-exts", "db-create", "db-close"}
	}
	return []string{"db-name", "db-create", "db-close"}
}

func (p *dbPanel) input(id string) *textinput.Model {
	switch id {
	case "db-name":
		return p.name
	case "db-exts":
		return p.exts
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
	for _, ti := range []*textinput.Model{p.name, p.exts} {
		ti.SetValue("")
	}
	p.name.Placeholder = "nome (ex: meu_app)"
	if p.buckets() {
		p.name.Placeholder = "nome (ex: meu-app-arquivos)"
	}
	return tea.Batch(p.setFocus(0), p.listCmd())
}

func (p *dbPanel) close() {
	p.engine = ""
	for _, ti := range []*textinput.Model{p.name, p.exts} {
		ti.Blur()
	}
}

func (p *dbPanel) listCmd() tea.Cmd {
	engine := p.engine
	return func() tea.Msg {
		ctx := context.Background()
		var rows []infraRow
		if engine == "garage" {
			list, err := p.api.Buckets(ctx)
			for _, b := range list {
				rows = append(rows, infraRow{b.Name, b.Secrets, b.Apps, cmpOrStr(b.Size, "?")})
			}
			return dbListMsg{engine, rows, err}
		}
		list, err := p.api.Databases(ctx)
		for _, d := range list {
			if d.Engine == engine {
				size := "?"
				if d.Size != nil {
					size = databases.HumanSize(*d.Size)
				}
				rows = append(rows, infraRow{d.Name, d.Secrets, d.Apps, size})
			}
		}
		return dbListMsg{engine, rows, err}
	}
}

func cmpOrStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (p *dbPanel) create() tea.Cmd {
	if p.busy {
		return nil
	}
	name := strings.TrimSpace(p.name.Value())
	if name == "" {
		p.err = "Informe o nome."
		return nil
	}
	p.busy, p.err, p.created = true, "", nil
	if p.buckets() {
		return func() tea.Msg {
			info, err := p.api.CreateBucket(context.Background(), name)
			return dbCreatedMsg{infraRow{name: info.Name, secrets: info.Secrets}, err}
		}
	}
	req := core.DatabaseRequest{Engine: p.engine, Name: name}
	if p.engine == "postgres" {
		req.Extensions = splitComma(p.exts.Value())
	}
	return func() tea.Msg {
		info, err := p.api.CreateDatabase(context.Background(), req)
		return dbCreatedMsg{infraRow{name: info.Name, secrets: info.Secrets}, err}
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
		p.created = &msg.row
		for _, ti := range []*textinput.Model{p.name, p.exts} {
			ti.SetValue("")
		}
		return p.listCmd(), true
	case dbListMsg:
		if msg.err == nil && msg.engine == p.engine {
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
	what, nameLabel, link := "banco", "Database", "Ligar banco"
	desc := "Cria o database e um usuario com senha nova, que so acessa esse database. " +
		"O app chega pela rede " + p.engine + "_network."
	if p.buckets() {
		what, nameLabel, link = "bucket", "Bucket", "Ligar bucket"
		desc = "Cria o bucket e uma chave de acesso so dele (leitura, escrita e dono). O app chega pela rede " +
			"garage_network em http://garage:3900 (regiao garage, path-style)."
	}
	var b strings.Builder
	b.WriteString(t.bold().Render("Novo "+what+" no "+p.engine) + "\n")
	b.WriteString(wrap.Inherit(muted).Render(desc) + "\n\n")
	field := lipgloss.NewStyle().Background(t.fieldBg).Foreground(t.fieldFg).Width(29)
	var buttons []string
	for i, id := range p.ids() {
		focused := i == p.focus
		if ti := p.input(id); ti != nil {
			label := map[string]string{"db-name": nameLabel, "db-exts": "Extensoes"}[id]
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
		b.WriteString("\n" + t.fg(t.ok).Bold(true).Render(nameLabel+" "+c.name+" criado.") + "\n")
		b.WriteString(wrap.Inherit(muted).Render("Ligue ao app em Apps -> Secrets -> "+link+": o app recebe os secrets "+
			strings.Join(c.secrets, " e ")+".") + "\n")
	}
	if len(p.list) > 0 {
		b.WriteString("\n" + t.bold().Render("Ja criados") + "\n")
		writeInfraRows(&b, nameLabel, p.list, t, w-6)
	}
	return t.panel(true, w-2, h).MaxHeight(h + 2).Render(b.String())
}

// writeInfraRows e a tabela dos databases ou buckets: nome, secrets que o
// app recebe (e o app ligado) e espaco ocupado. Se nao cabe em w, os
// secrets vao numa segunda linha de cada item.
func writeInfraRows(b *strings.Builder, nameLabel string, list []infraRow, t theme, w int) {
	nameW, secW := len(nameLabel), len("Secrets")
	secs := make([]string, len(list))
	for i, d := range list {
		app := "sem app"
		if len(d.apps) > 0 {
			app = strings.Join(d.apps, ", ")
		}
		secs[i] = strings.Join(d.secrets, ", ") + " (" + app + ")"
		nameW, secW = max(nameW, len(d.name)), max(secW, len(secs[i]))
	}
	head := t.fg(t.muted).Bold(true)
	if 2+nameW+2+secW+2+9 > w {
		row := func(name, size string) string { return fmt.Sprintf("  %-*s  %9s", nameW, name, size) }
		b.WriteString(head.Render(row(nameLabel, "Espaco")) + "\n")
		for i, d := range list {
			b.WriteString(row(d.name, d.size) + "\n")
			b.WriteString(t.fg(t.muted).Width(w).Render("    "+secs[i]) + "\n")
		}
		return
	}
	row := func(name, secrets, size string) string {
		return fmt.Sprintf("  %-*s  %-*s  %9s", nameW, name, secW, secrets, size)
	}
	b.WriteString(head.Render(row(nameLabel, "Secrets", "Espaco")) + "\n")
	for i, d := range list {
		b.WriteString(row(d.name, secs[i], d.size) + "\n")
	}
}
