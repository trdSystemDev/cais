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
			p.secretName.SetValue("")
			p.secretValue.SetValue("")
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
		p.setStatus("ok", msg.app.Name+" criado. A Ajuda mostra como escrever o compose de producao; depois Variaveis, Secrets e Deploy.")
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
		return p.startCreate()
	}
	for i, a := range p.list {
		if p.zones.Get("app:" + a.Name).InBounds(msg) {
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

func (p *appsPage) key(msg tea.KeyPressMsg) tea.Cmd {
	k := msg.String()
	if !p.inForm {
		switch k {
		case "up", "down":
			if n := len(p.list); n > 0 && !p.creating {
				d := 1
				if k == "up" {
					d = -1
				}
				p.sel = (p.sel + d + n) % n
				p.focus = 0
				return p.loadSelected(false)
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
	if !p.typing() {
		return nil
	}
	f := p.fields[p.focus]
	if f.off() {
		return nil
	}
	ti, cmd := f.input.Update(msg)
	*f.input = ti
	return cmd
}

// visao
