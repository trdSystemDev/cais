package ui

import (
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/trdSystemDev/cais/internal/apps"
)

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
		case msg.deployed != nil && msg.rollback:
			how := "com as imagens guardadas"
			if !msg.deployed.Images {
				how = "com build (as imagens nao estavam guardadas)"
			}
			p.setStatus("ok", msg.app+" voltou pro commit "+shortCommit(msg.deployed.Commit)+", "+how+".")
		case msg.deployed != nil:
			p.setStatus("ok", msg.app+" no ar no commit "+shortCommit(msg.deployed.Commit)+".")
			if len(msg.deployed.Warnings) > 0 {
				p.detailErr = "Avisos sobre o compose (nao impediram o deploy):\n- " + strings.Join(msg.deployed.Warnings, "\n- ")
			}
		default:
			p.setStatus("ok", msg.ok)
			if p.envPending != nil {
				p.envSaved = p.envPending
			}
		}
		p.envPending = nil
		p.rebuild()
		return tea.Batch(p.checkCmd(), p.guideCmd(msg.app), p.dbsCmd(), p.promoRefresh(true)), true
	case appPromoMsg:
		if p.promoSig[msg.from] != msg.sig {
			return nil, true // resposta de uma consulta ja substituida
		}
		r := promoResult{p: msg.p}
		if msg.err != nil {
			r.err = msg.err.Error()
		}
		p.promos[msg.from] = r
		p.rebuild()
		return nil, true
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
		p.setStatus("ok", msg.app.Name+" criado. A Ajuda mostra como escrever o compose de producao; depois Variaveis, Bancos e buckets e Deploy.")
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
		p.envLoaded, p.envSaved = msg.app, msg.env
		p.rebuild()
		return nil, true
	case appDBsMsg:
		if msg.err == nil {
			p.dbs, p.buckets = msg.list, msg.buckets
			p.dbPick, p.bucketPick = 0, 0
		}
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
		p.endSearch()
		return p.startCreate()
	}
	if p.zones.Get("app-search").InBounds(msg) {
		return p.startSearch()
	}
	for i, a := range p.list {
		if p.matches(a) && p.zones.Get("app:"+a.Name).InBounds(msg) {
			p.endSearch()
			p.creating, p.inForm, p.sel, p.focus = false, false, i, 0
			return tea.Batch(p.loadSelected(false), p.setFocus(0))
		}
	}
	if !p.creating {
		for i, s := range appSections {
			if p.zones.Get("app-sec:" + s).InBounds(msg) {
				p.section, p.focus, p.scroll = i, 0, 0
				p.rebuild()
				return p.sectionCmd()
			}
		}
	}
	for i, f := range p.fields {
		if p.zones.Get(f.id).InBounds(msg) {
			p.endSearch()
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

// startSearch poe o foco na busca da lista (pelo / ou clicando nela).
func (p *appsPage) startSearch() tea.Cmd {
	if p.creating {
		return nil
	}
	p.searching, p.inForm = true, false
	p.setFocus(p.focus) // tira o foco do campo do formulario
	return p.search.Focus()
}

func (p *appsPage) endSearch() {
	p.searching = false
	p.search.Blur()
}

// searchKey e a tecla com o foco na busca: o texto filtra a lista, ↑↓
// andam nos apps que aparecem, enter volta pra lista mantendo o filtro e
// esc limpa a busca.
func (p *appsPage) searchKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "up":
		return p.moveSel(-1)
	case "down":
		return p.moveSel(1)
	case "enter", "tab":
		p.endSearch()
		return nil
	case "esc":
		p.endSearch()
		p.search.SetValue("")
		return nil
	}
	ti, cmd := p.search.Update(msg)
	*p.search = ti
	return tea.Batch(cmd, p.searched())
}

func (p *appsPage) key(msg tea.KeyPressMsg) tea.Cmd {
	k := msg.String()
	if p.searching {
		return p.searchKey(msg)
	}
	if !p.inForm {
		switch k {
		case "up", "down":
			if len(p.list) > 0 && !p.creating {
				d := 1
				if k == "up" {
					d = -1
				}
				return p.moveSel(d)
			}
		case "/":
			return p.startSearch()
		case "esc":
			if p.search.Value() != "" {
				p.search.SetValue("") // tira o filtro
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
			return p.sectionCmd()
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
	// Importar .env aberto: o texto colado inteiro (com as quebras de linha)
	if p.importing && p.section == secEnv && !p.creating && !p.typing() {
		p.importText, p.importErr = msg.Content, ""
		if _, err := apps.ParseEnv([]byte(p.importText)); err != nil {
			p.importErr = err.Error()
		}
		return nil
	}
	if p.searching {
		ti, cmd := p.search.Update(msg)
		*p.search = ti
		return tea.Batch(cmd, p.searched())
	}
	if !p.typing() {
		return nil
	}
	f := p.fields[p.focus]
	if f.off() {
		return nil
	}
	// valor com varias linhas (chave PEM, JSON) colado numa variavel: fica
	// inteiro, o campo de uma linha so mostra quantas linhas tem
	if r := p.envRowOf(f.input); r != nil && f.input == r.value && strings.ContainsAny(msg.Content, "\n\r") {
		v := strings.ReplaceAll(strings.ReplaceAll(msg.Content, "\r\n", "\n"), "\r", "\n")
		r.setMulti(strings.TrimRight(v, "\n"))
		return nil
	}
	ti, cmd := f.input.Update(msg)
	*f.input = ti
	return cmd
}

// visao
