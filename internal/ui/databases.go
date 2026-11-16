package ui

import (
	"context"
	"fmt"
	"image/color"
	"slices"
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
	src     *textinput.Model // string de conexao pra importar (tem senha)
	focus   int
	busy    bool
	err     string
	created *infraRow
	list    []infraRow
	done    string // aviso de exclusao feita

	// importing e a importacao em andamento, pelo estado do core
	importing *core.DBImport

	// excluir: o nome do item aguardando confirmacao (digitar o nome)
	delName  string
	delInput *textinput.Model
}

// infraRow e um database ou bucket na lista: nome, secrets que o app
// recebe, apps ligados e espaco ocupado.
type infraRow struct {
	name    string
	secrets []string
	apps    []string
	size    string
	// imported e a origem quando o database acabou de ser importado
	imported string
	dumpSize string
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
	dbDeletedMsg struct {
		name string
		err  error
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
	p.src = mk("opcional: postgres://...")
	p.src.EchoMode = textinput.EchoPassword
	p.src.CharLimit = 2048
	p.delInput = mk("digite o nome")
	p.setTheme(th)
	return p
}

func (p *dbPanel) setTheme(th theme) {
	p.th = th
	for _, ti := range []*textinput.Model{p.name, p.exts, p.src, p.delInput} {
		th.styleInput(ti)
	}
}

func (p *dbPanel) open() bool { return p.engine != "" }

// ids na ordem do Tab: o formulario (extensoes so no Postgres), o excluir
// de cada item da lista e a confirmacao aberta
func (p *dbPanel) ids() []string {
	ids := []string{"db-name", "db-create", "db-close"}
	switch p.engine {
	case "postgres":
		ids = []string{"db-name", "db-exts", "db-src", "db-create", "db-close"}
	case "mysql":
		ids = []string{"db-name", "db-src", "db-create", "db-close"}
	}
	for _, d := range p.list {
		ids = append(ids, "db-del:"+d.name)
	}
	if p.delName != "" {
		ids = append(ids, "db-del-confirm", "db-del-ok", "db-del-cancel")
	}
	return ids
}

// focusID e o campo em foco (a lista pode ter mudado desde o foco)
func (p *dbPanel) focusID() string {
	ids := p.ids()
	return ids[min(p.focus, len(ids)-1)]
}

func (p *dbPanel) input(id string) *textinput.Model {
	switch id {
	case "db-name":
		return p.name
	case "db-exts":
		return p.exts
	case "db-src":
		return p.src
	case "db-del-confirm":
		return p.delInput
	}
	return nil
}

func (p *dbPanel) typing() bool { return p.open() && p.input(p.focusID()) != nil }

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
	p.engine, p.err, p.created, p.busy, p.list, p.done, p.delName = engine, "", nil, false, nil, "", ""
	for _, ti := range []*textinput.Model{p.name, p.exts, p.src, p.delInput} {
		ti.SetValue("")
	}
	p.name.Placeholder = "nome (ex: meu_app)"
	p.src.Placeholder = "opcional: " + engine + "://usuario:senha@host/banco"
	if p.buckets() {
		p.name.Placeholder = "nome (ex: meu-app-arquivos)"
	}
	return tea.Batch(p.setFocus(0), p.listCmd())
}

func (p *dbPanel) close() {
	p.engine, p.delName = "", ""
	p.src.SetValue("") // tem senha: nao fica no campo depois de fechar
	for _, ti := range []*textinput.Model{p.name, p.exts, p.src, p.delInput} {
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
				rows = append(rows, infraRow{name: b.Name, secrets: b.Secrets, apps: b.Apps, size: cmpOrStr(b.Size, "?")})
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
				rows = append(rows, infraRow{name: d.Name, secrets: d.Secrets, apps: d.Apps, size: size})
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
	p.busy, p.err, p.created, p.done = true, "", nil, ""
	if p.buckets() {
		return func() tea.Msg {
			info, err := p.api.CreateBucket(context.Background(), name)
			return dbCreatedMsg{infraRow{name: info.Name, secrets: info.Secrets}, err}
		}
	}
	req := core.DatabaseRequest{Engine: p.engine, Name: name, Source: strings.TrimSpace(p.src.Value())}
	if p.engine == "postgres" {
		req.Extensions = splitComma(p.exts.Value())
	}
	return func() tea.Msg {
		info, err := p.api.CreateDatabase(context.Background(), req)
		row := infraRow{name: info.Name, secrets: info.Secrets, imported: info.Imported}
		if info.Imported != "" {
			row.dumpSize = databases.HumanSize(info.ImportedBytes)
		}
		return dbCreatedMsg{row, err}
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
	if name, ok := strings.CutPrefix(id, "db-del:"); ok {
		p.delName, p.err, p.done = name, "", ""
		p.delInput.SetValue("")
		return p.setFocus(slices.Index(p.ids(), "db-del-confirm"))
	}
	switch id {
	case "db-create":
		return p.create()
	case "db-close":
		p.close()
	case "db-del-ok":
		return p.remove()
	case "db-del-cancel":
		p.delName = ""
		return p.setFocus(0)
	}
	return nil
}

// remove exclui o item confirmado (o nome digitado tem que bater)
func (p *dbPanel) remove() tea.Cmd {
	name := p.delName
	if p.busy || name == "" {
		return nil
	}
	if strings.TrimSpace(p.delInput.Value()) != name {
		p.err = "Digite " + name + " pra confirmar."
		p.delInput.SetValue("")
		return nil
	}
	p.busy, p.err = true, ""
	engine := p.engine
	return func() tea.Msg {
		ctx := context.Background()
		if engine == "garage" {
			return dbDeletedMsg{name, p.api.DeleteBucket(ctx, name)}
		}
		return dbDeletedMsg{name, p.api.DeleteDatabase(ctx, engine, name)}
	}
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
		for _, ti := range []*textinput.Model{p.name, p.exts, p.src} {
			ti.SetValue("")
		}
		return p.listCmd(), true
	case dbDeletedMsg:
		p.busy = false
		if msg.err != nil {
			p.err = msg.err.Error()
			return nil, true
		}
		p.delName, p.created = "", nil
		p.done = msg.name + " excluido."
		return tea.Batch(p.setFocus(0), p.listCmd()), true
	case dbListMsg:
		if msg.err == nil && msg.engine == p.engine {
			p.list = msg.list
		}
		return nil, true
	}
	return nil, false
}

func (p *dbPanel) key(msg tea.KeyPressMsg) tea.Cmd {
	id := p.focusID()
	switch k := msg.String(); k {
	case "esc":
		if p.delName != "" {
			p.delName = ""
			return p.setFocus(0)
		}
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
	if ti := p.input(p.focusID()); ti != nil && p.open() {
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
	focusID := p.focusID()
	var buttons []string
	for _, id := range p.ids() {
		if id != "db-name" && id != "db-exts" && id != "db-src" && id != "db-create" && id != "db-close" {
			continue // excluir: na lista e na confirmacao
		}
		focused := id == focusID
		if ti := p.input(id); ti != nil {
			label := map[string]string{"db-name": nameLabel, "db-exts": "Extensoes", "db-src": "Importar de"}[id]
			marker, ls := "  ", t.fg(t.text).Width(12)
			if focused {
				marker, ls = t.fg(t.accent).Bold(true).Render("› "), t.fg(t.accent).Bold(true).Width(12)
			}
			b.WriteString(p.zones.Mark(id, marker+ls.Render(label)+t.input(ti, 29)) + "\n")
			if id == "db-src" {
				b.WriteString(p.viewSource(w-6) + "\n")
			}
			continue
		}
		label := map[string]string{"db-create": "Criar", "db-close": "Fechar"}[id]
		if id == "db-create" && strings.TrimSpace(p.src.Value()) != "" {
			label = "Criar e importar"
		}
		k := btnNormal
		if id == "db-close" {
			k = btnNeutral
		}
		if p.busy && id == "db-create" {
			k, label = btnOff, "Criando..."
			if strings.TrimSpace(p.src.Value()) != "" {
				label = "Importando..."
			}
		}
		buttons = append(buttons, " ", p.zones.Mark(id, t.button(label, k, focused)))
	}
	b.WriteString("\n" + lipgloss.JoinHorizontal(lipgloss.Top, buttons...) + "\n")
	if im := p.importing; im != nil && p.busy && im.Engine == p.engine {
		b.WriteString("\n" + wrap.Foreground(t.warn).Render(fmt.Sprintf("Importando de %s: %s copiados. Pode sair da aba: a copia continua no daemon.",
			im.Source, databases.HumanSize(im.Bytes))) + "\n")
	}
	if p.err != "" && p.delName == "" {
		b.WriteString("\n" + wrap.Foreground(t.danger).Render(p.err) + "\n")
	}
	if p.done != "" {
		b.WriteString("\n" + t.fg(t.ok).Bold(true).Render(p.done) + "\n")
	}
	if c := p.created; c != nil {
		msg := nameLabel + " " + c.name + " criado."
		if c.imported != "" {
			msg = nameLabel + " " + c.name + " criado com o conteudo de " + c.imported + " (dump de " + c.dumpSize + ")."
		}
		b.WriteString("\n" + wrap.Foreground(t.ok).Bold(true).Render(msg) + "\n")
		b.WriteString(wrap.Inherit(muted).Render("Ligue ao app em Apps -> Secrets -> "+link+": o app recebe os secrets "+
			strings.Join(c.secrets, " e ")+".") + "\n")
	}
	if len(p.list) > 0 {
		b.WriteString("\n" + t.bold().Render("Ja criados") + "\n")
		del := func(name string) string {
			return p.zones.Mark("db-del:"+name, t.chip("✕ excluir", btnDanger, focusID == "db-del:"+name || p.delName == name))
		}
		writeInfraRows(&b, nameLabel, p.list, t, w-6, del)
	}
	if p.delName != "" {
		warn := "Excluir o database " + p.delName + " apaga todos os dados dele e o usuario. Nao tem volta."
		if p.buckets() {
			warn = "Excluir o bucket " + p.delName + " apaga o bucket e a chave dele (o Garage so exclui bucket vazio). Nao tem volta."
		}
		b.WriteString("\n" + t.fg(t.danger).Bold(true).Render("Excluir "+p.delName) + "\n")
		b.WriteString(wrap.Inherit(muted).Render(warn+" Digite "+p.delName+" pra confirmar.") + "\n")
		marker, ls := "  ", t.fg(t.text).Width(12)
		if focusID == "db-del-confirm" {
			marker, ls = t.fg(t.accent).Bold(true).Render("› "), t.fg(t.accent).Bold(true).Width(12)
		}
		b.WriteString(p.zones.Mark("db-del-confirm", marker+ls.Render("Confirmar")+t.input(p.delInput, 29)) + "\n")
		k, label := btnDanger, "Excluir"
		if p.busy {
			k, label = btnOff, "Excluindo..."
		}
		b.WriteString("\n" + lipgloss.JoinHorizontal(lipgloss.Top, "  ",
			p.zones.Mark("db-del-ok", t.button(label, k, focusID == "db-del-ok")), " ",
			p.zones.Mark("db-del-cancel", t.button("Cancelar", btnNeutral, focusID == "db-del-cancel"))) + "\n")
		if p.err != "" {
			b.WriteString("\n" + wrap.Foreground(t.danger).Render(p.err) + "\n")
		}
	}
	return t.panel(true, w-2, h).MaxHeight(h + 2).Render(b.String())
}

// viewSource mostra, embaixo do campo Importar de, o que o cais entendeu da
// string de conexao (sem a senha), ou por que nao entendeu.
func (p *dbPanel) viewSource(w int) string {
	t := p.th
	raw := strings.TrimSpace(p.src.Value())
	// alinhado com o campo, inclusive nas linhas quebradas
	line := func(c color.Color, text string) string {
		return t.fg(c).PaddingLeft(14).Width(w).Render(text)
	}
	if raw == "" {
		return line(t.muted, "vazio cria o banco vazio")
	}
	s, err := databases.ParseSource(p.engine, raw)
	if err != nil {
		return line(t.danger, err.Error())
	}
	return line(t.ok, "copia de "+s.String())
}

// writeInfraRows e a tabela dos databases ou buckets: nome, secrets que o
// app recebe (e o app ligado) e espaco ocupado. Se nao cabe em w, os
// secrets vao numa segunda linha de cada item.
func writeInfraRows(b *strings.Builder, nameLabel string, list []infraRow, t theme, w int, del func(name string) string) {
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
	if 2+nameW+2+secW+2+9+11 > w {
		row := func(name, size string) string { return fmt.Sprintf("  %-*s  %9s", nameW, name, size) }
		b.WriteString(head.Render(row(nameLabel, "Espaco")) + "\n")
		for i, d := range list {
			b.WriteString(row(d.name, d.size) + "  " + del(d.name) + "\n")
			b.WriteString(t.fg(t.muted).Width(w).Render("    "+secs[i]) + "\n")
		}
		return
	}
	row := func(name, secrets, size string) string {
		return fmt.Sprintf("  %-*s  %-*s  %9s", nameW, name, secW, secrets, size)
	}
	b.WriteString(head.Render(row(nameLabel, "Secrets", "Espaco")) + "\n")
	for i, d := range list {
		b.WriteString(row(d.name, secs[i], d.size) + "  " + del(d.name) + "\n")
	}
}
