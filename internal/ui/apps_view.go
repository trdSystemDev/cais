package ui

import (
	"fmt"
	"image/color"
	"maps"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/core"
)

func (p *appsPage) viewList(w, h int) string {
	t := p.th
	var b strings.Builder
	b.WriteString(t.bold().Render("Apps") + "\n")
	k := btnNormal
	if p.busy != "" {
		k = btnOff
	}
	b.WriteString(p.zones.Mark("app-new", t.button("+ Novo app", k, p.creating)) + "\n\n")
	if len(p.list) == 0 {
		b.WriteString(t.fg(t.muted).Width(w - 4).Render("Nenhum app ainda. Crie com + Novo app a partir do link do repositorio."))
		return lipgloss.NewStyle().Width(w).Height(h).MaxHeight(h).Render(b.String())
	}
	b.WriteString(p.zones.Mark("app-search", t.input(p.search, max(1, w-4))) + "\n\n")

	var cards []string
	selIdx := -1
	for i, a := range p.list {
		if !p.matches(a) {
			continue
		}
		if !p.creating && i == p.sel {
			selIdx = len(cards)
		}
		cards = append(cards, p.zones.Mark("app:"+a.Name, p.appCard(a, i, w)))
	}
	if len(cards) == 0 {
		b.WriteString(t.fg(t.muted).Width(w - 4).Render("Nenhum app com \"" + strings.TrimSpace(p.search.Value()) + "\" no nome. esc limpa a busca."))
		return lipgloss.NewStyle().Width(w).Height(h).MaxHeight(h).Render(b.String())
	}

	avail := h - strings.Count(b.String(), "\n")
	heights := make([]int, len(cards))
	for i, c := range cards {
		heights[i] = lipgloss.Height(c)
	}
	p.scrollList(heights, selIdx, avail)
	end := listWindow(heights, p.listTop, avail)
	muted := t.fg(t.muted)
	if p.listTop > 0 {
		b.WriteString(muted.Render(fmt.Sprintf("  ↑ %d acima", p.listTop)) + "\n")
	}
	b.WriteString(strings.Join(cards[p.listTop:end], "\n") + "\n")
	if end < len(cards) {
		b.WriteString(muted.Render(fmt.Sprintf("  ↓ %d abaixo", len(cards)-end)))
	}
	return p.zones.Mark("app-list", lipgloss.NewStyle().Width(w).Height(h).MaxHeight(h).Render(b.String()))
}

// appCard e o cartao de um app na lista.
func (p *appsPage) appCard(a core.AppStatus, i, w int) string {
	t := p.th
	mark, status, c := appStatus(a, t)
	sel := !p.creating && i == p.sel
	title := t.bold().Render(mark+" "+a.Name) + "  " + t.fg(c).Render(status)
	sub := t.fg(t.muted).Render(a.Repo)
	cw, indent := w-4, ""
	if prod := p.app(a.PromoteTo); prod != nil && p.matches(*prod) {
		// homologacao: embaixo da producao, recuada
		cw, indent = w-6, "  "
		sub = t.fg(t.muted).Render("homologacao de " + a.PromoteTo)
	}
	if a.AutoDeploy {
		sub += t.fg(t.muted).Render(" · auto")
	}
	// a largura do painel inclui borda e padding: sobram cw-4 colunas
	card := t.panel(sel, cw, 2).Render(ansi.Truncate(title, cw-4, "…") + "\n" + ansi.Truncate(sub, cw-4, "…"))
	if indent != "" {
		card = lipgloss.NewStyle().MarginLeft(len(indent)).Render(card)
	}
	return card
}

// listWindow diz ate onde (exclusivo) cabem os cartoes a partir de top em
// avail linhas, guardando uma linha pro "acima" e outra pro "abaixo" quando
// precisam aparecer. Sempre mostra ao menos um cartao.
func listWindow(heights []int, top, avail int) int {
	room := avail
	if top > 0 {
		room--
	}
	end, used := top, 0
	for end < len(heights) && used+heights[end] <= room {
		used += heights[end]
		end++
	}
	if end < len(heights) {
		for used > room-1 && end > top+1 {
			end--
			used -= heights[end]
		}
	}
	return max(end, min(top+1, len(heights)))
}

// scrollList acerta o listTop: vai atras do selecionado quando ele (ou a
// busca) muda -- a roda do mouse rola sem trocar a selecao -- e nao deixa
// sobrar espaco embaixo quando cabem mais cartoes.
func (p *appsPage) scrollList(heights []int, selIdx, avail int) {
	n := len(heights)
	name, query := "", p.search.Value()
	if p.sel < len(p.list) {
		name = p.list[p.sel].Name
	}
	p.listTop = max(0, min(p.listTop, n-1))
	if selIdx >= 0 && (name != p.listSeen || query != p.listQuery) {
		if selIdx < p.listTop {
			p.listTop = selIdx
		}
		for listWindow(heights, p.listTop, avail) <= selIdx {
			p.listTop++
		}
	}
	p.listSeen, p.listQuery = name, query
	for p.listTop > 0 && listWindow(heights, p.listTop-1, avail) == n {
		p.listTop--
	}
}

// listWheel rola a lista com a roda do mouse (o desenho limita).
func (p *appsPage) listWheel(up bool) {
	if up {
		p.listTop = max(0, p.listTop-1)
	} else {
		p.listTop++
	}
}

// appStatus resume o app num simbolo, texto e cor.
func appStatus(a core.AppStatus, t theme) (string, string, color.Color) {
	running, total := 0, len(a.Containers)
	healthy, restarting := true, false
	for _, c := range a.Containers {
		if c.State == "running" {
			running++
		}
		if c.State == "restarting" {
			restarting = true
		}
		if c.State != "running" || c.Health == "unhealthy" {
			healthy = false
		}
	}
	switch {
	case a.BuildQueued:
		return "◌", "na fila de build", t.warn
	case a.Deploying:
		return "◌", "deploy...", t.warn
	case a.Error != "":
		return "✗", "erro", t.danger
	case total == 0 && a.Last == nil:
		return "·", "sem deploy", t.muted
	case total == 0:
		return "✗", "fora do ar", t.danger
	case restarting:
		return "✗", "reiniciando", t.danger
	case running == 0:
		return "■", "parado", t.muted
	case healthy:
		return "✓", fmt.Sprintf("%d/%d no ar", running, total), t.ok
	default:
		return "✗", fmt.Sprintf("%d/%d no ar", running, total), t.danger
	}
}

func (p *appsPage) viewSections() string {
	var parts []string
	for i, s := range appSections {
		parts = append(parts, p.zones.Mark("app-sec:"+s, p.th.pill(i == p.section).Render(s)), " ")
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

// render mostra um campo: texto (fundo cinza) ou botao (borda colorida),
// com o foco marcado.
func (p *appsPage) render(f *field, i int) string {
	t := p.th
	focused := p.inForm && i == p.focus
	if f.input != nil {
		return t.input(f.input, f.input.Width()+1)
	}
	k := f.kind
	if f.off() {
		k = btnOff
		if f.kind == btnDanger {
			k = btnDangerOff // continua dizendo que apaga
		}
	}
	if f.chip {
		return t.chip(f.label, k, focused)
	}
	return t.button(f.label, k, focused)
}

// markFocus guarda a linha (e a altura) do campo com foco, pra rolagem.
func (p *appsPage) markFocus(b *strings.Builder, h int) {
	p.focusLine, p.focusH = strings.Count(b.String(), "\n"), h
}

// picker escreve um seletor (clique ou enter troca a opcao) com o rotulo
// do campo.
func (p *appsPage) picker(b *strings.Builder, id, text string) {
	t := p.th
	for i, f := range p.fields {
		if f.id != id {
			continue
		}
		marker, ls := "  ", t.fg(t.text).Width(18)
		if p.inForm && i == p.focus {
			marker, ls = t.fg(t.accent).Bold(true).Render("› "), t.fg(t.accent).Bold(true).Width(18)
			p.markFocus(b, 1)
		}
		b.WriteString(p.zones.Mark(f.id, marker+ls.Render(f.label)+t.selector(text, p.inForm && i == p.focus)) + "\n")
	}
}

// row escreve um campo com rotulo, marcado pro clique.
func (p *appsPage) row(b *strings.Builder, id string) {
	for i, f := range p.fields {
		if f.id != id {
			continue
		}
		t := p.th
		marker, label := "  ", t.fg(t.text).Width(18).Render(f.label)
		if p.inForm && i == p.focus {
			marker = t.fg(t.accent).Bold(true).Render("› ")
			label = t.fg(t.accent).Bold(true).Width(18).Render(f.label)
			p.markFocus(b, 1)
		}
		b.WriteString(p.zones.Mark(f.id, marker+label+p.render(f, i)) + "\n")
		return
	}
}

// inline escreve varios botoes lado a lado.
func (p *appsPage) inline(b *strings.Builder, ids ...string) {
	parts := []string{"  "}
	for _, id := range ids {
		for i, f := range p.fields {
			if f.id == id {
				r := p.render(f, i)
				if p.inForm && i == p.focus {
					p.markFocus(b, lipgloss.Height(r))
				}
				if len(parts) > 1 {
					parts = append(parts, " ")
				}
				parts = append(parts, p.zones.Mark(f.id, r))
			}
		}
	}
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, parts...) + "\n")
}

func (p *appsPage) viewDetail(w, h int) string {
	t := p.th
	inner := w - 6
	wrap := lipgloss.NewStyle().Width(inner)
	muted := t.fg(t.muted)
	var head, body strings.Builder

	a := p.selected()
	switch {
	case p.creating:
		head.WriteString(t.bold().Render("Novo app") + "\n")
	case a == nil:
		head.WriteString(t.bold().Render("Apps") + "\n")
	default:
		mark, status, c := appStatus(*a, t)
		head.WriteString(t.bold().Render(a.Name) + "  " + t.fg(c).Render(mark+" "+status) + "\n")
		head.WriteString(p.viewSections() + "\n")
	}
	if p.status != "" {
		c := map[string]color.Color{"ok": t.ok, "warn": t.warn, "danger": t.danger}[p.statusC]
		head.WriteString(lipgloss.NewStyle().Bold(true).Foreground(c).Width(inner).Render(p.status) + "\n")
	}
	if p.detailErr != "" {
		c := t.danger
		if p.statusC == "ok" {
			c = t.warn
		}
		for _, l := range strings.Split(p.detailErr, "\n") {
			head.WriteString(wrap.Foreground(c).Render("  "+l) + "\n")
		}
	}

	p.focusLine, p.focusH = -1, 1
	body.WriteString("\n")
	switch {
	case p.creating:
		p.viewCreate(&body, inner)
	case a == nil:
		body.WriteString(muted.Width(inner).Render(
			"Um app e um repositorio do GitHub com o compose de producao pronto. Clique em + Novo app e " +
				"informe o link do repositorio. No deploy o cais baixa o commit, grava o .env com as " +
				"variaveis do app, faz o build e sobe. A Ajuda de cada app mostra como escrever o compose."))
	default:
		switch p.section {
		case secGeneral:
			p.viewGeneral(&body, *a, inner)
		case secEnv:
			p.viewEnv(&body, inner)
		case secLinks:
			p.viewLinks(&body, *a, inner)
		case secGuide:
			if p.guideFor == a.Name {
				body.WriteString(p.guide)
			} else {
				body.WriteString(muted.Render("carregando..."))
			}
		}
	}

	headStr := strings.TrimSuffix(head.String(), "\n")
	lines := strings.Split(strings.TrimSuffix(body.String(), "\n"), "\n")
	avail := max(1, h-lipgloss.Height(headStr))
	if p.focusLine >= 0 && p.follow {
		p.follow = false
		if p.focusLine < p.scroll {
			p.scroll = p.focusLine
		}
		if end := p.focusLine + p.focusH - 1; end >= p.scroll+avail {
			p.scroll = min(p.focusLine, end-avail+1)
		}
	}
	p.scroll = max(0, min(p.scroll, len(lines)-avail))
	visible := lines[p.scroll:min(len(lines), p.scroll+avail)]
	if p.scroll > 0 {
		visible[0] = muted.Render("  ↑ mais acima")
	}
	if p.scroll+avail < len(lines) {
		visible[len(visible)-1] = muted.Render("  ↓ mais abaixo")
	}
	return p.zones.Mark("app-detail", t.panel(p.inForm, w-2, h).MaxHeight(h+2).Render(headStr+"\n"+strings.Join(visible, "\n")))
}

// wheel rola o formulario com a roda do mouse.
func (p *appsPage) wheel(up bool) {
	if up {
		p.scroll = max(0, p.scroll-3)
	} else {
		p.scroll += 3 // o desenho limita ao fim
	}
}

func (p *appsPage) viewCreate(b *strings.Builder, w int) {
	p.row(b, "new-name")
	p.row(b, "new-repo")
	p.row(b, "new-branch")
	p.row(b, "new-compose")
	p.picker(b, "new-auto", autoText(p.newAuto, p.newBranch.Value()))
	if p.newPromoteTo != "" {
		b.WriteString("  " + p.th.fg(p.th.text).Width(18).Render("Homologacao de") + p.th.bold().Render(p.newPromoteTo) + "\n")
		b.WriteString(p.th.fg(p.th.muted).Width(w).Render("  Use um compose proprio (ex: docker-compose.homolog.yaml) com rotas do Traefik com o nome deste app "+
			"e outro dominio; variaveis e banco sao separados dos da producao.") + "\n")
	}
	b.WriteString("\n")
	p.inline(b, "new-create", "new-cancel")
	b.WriteString("\n" + p.th.fg(p.th.muted).Width(w).Render(
		"Nome vazio = o nome do repositorio. O repositorio precisa do compose de producao pronto (labels do "+
			"Traefik, redes, cais run): depois de criar, a Ajuda mostra o modelo. O codigo e baixado pelo GitHub "+
			"App do cais (aba Setup).") + "\n")
}

// autoText e o texto do seletor de deploy automatico.
func autoText(on bool, branch string) string {
	if on {
		return "‹ automatico: cada commit na " + branchName(strings.TrimSpace(branch)) + " ›"
	}
	return "‹ manual ›"
}

// viewPromotion e a parte de homologacao da aba Geral: na homologacao, o
// que promover faria; na producao, as homologacoes dela (ou o botao de
// criar uma).
func (p *appsPage) viewPromotion(b *strings.Builder, a core.AppStatus, w int) {
	t := p.th
	muted := t.fg(t.muted)
	wrap := lipgloss.NewStyle().Width(w).PaddingLeft(2) // recuo em todas as linhas
	hs := p.homologs(a)
	b.WriteString("\n" + t.rule(w) + "\n")
	if len(hs) == 0 {
		b.WriteString(t.bold().Render("Homologacao") + "\n")
		b.WriteString(muted.Width(w).Render("  Nenhuma homologacao. Ela e um app separado, com o mesmo repositorio e um compose proprio: "+
			"sobe uma versao de teste sem tocar na producao, e o commit validado nela e promovido pra ca.") + "\n\n")
		p.inline(b, "homolog-new")
		return
	}
	title := "Homologacao"
	if a.PromoteTo != "" {
		title = "Promover pra producao (" + a.PromoteTo + ")"
	}
	b.WriteString(t.bold().Render(title) + " ")
	p.mark(b, "info-promote")
	b.WriteString(" ")
	p.mark(b, "promo-refresh")
	b.WriteString("\n")
	for _, h := range hs {
		r, ok := p.promos[h.Name]
		if a.PromoteTo == "" {
			fmt.Fprintf(b, "  %-16s no ar %s\n", h.Name, cmpOr(shortCommit(h.Commit), "-"))
		} else if prod := p.app(a.PromoteTo); prod != nil {
			fmt.Fprintf(b, "  %-16s %s\n", "validado aqui", cmpOr(shortCommit(h.Commit), "-"))
			fmt.Fprintf(b, "  %-16s %s\n", "na producao", cmpOr(shortCommit(prod.Commit), "-"))
		}
		var line string
		c := t.muted
		switch {
		case !ok:
			line = "consultando o GitHub..."
		case r.err != "":
			line, c = r.err, t.danger
		case r.p.State == apps.PromoteReady:
			line, c = "pode promover "+shortCommit(r.p.Deploy), t.ok
			if r.p.Ahead > 0 {
				line += fmt.Sprintf(": a producao ganha %d commit(s)", r.p.Ahead)
			}
			if r.p.Deploy != r.p.Commit {
				line += " (a ponta da " + branchName(r.p.Branch) + " tem o mesmo codigo: squash merge)"
			}
		case r.p.State == apps.PromoteSame:
			line = r.p.Reason
		case r.p.State == apps.PromoteOlder:
			line, c = r.p.Reason, t.warn
		default:
			line, c = r.p.Reason, t.danger
		}
		b.WriteString(wrap.Foreground(c).Render(line) + "\n")
		if ok && r.err == "" && r.p.Unvalidated > 0 {
			b.WriteString(wrap.Foreground(t.warn).Render(fmt.Sprintf("a %s tem %d commit(s) depois do validado: o Deploy direto da producao subiria codigo que nao passou pela homologacao",
				branchName(r.p.Branch), r.p.Unvalidated)) + "\n")
		}
		b.WriteString("\n")
		p.inline(b, "promote:"+h.Name)
	}
}

func cmpOr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func (p *appsPage) viewGeneral(b *strings.Builder, a core.AppStatus, w int) {
	t := p.th
	fmt.Fprintf(b, "  %-14s github.com/%s\n", "repositorio", a.Repo)
	fmt.Fprintf(b, "  %-14s %s\n", "compose", a.ComposeFile())
	for _, l := range a.Databases {
		fmt.Fprintf(b, "  %-14s %s\n", "banco", l.ID())
	}
	if a.Commit != "" {
		fmt.Fprintf(b, "  %-14s %s\n", "no ar", shortCommit(a.Commit))
	}
	if l := a.Last; l != nil {
		line := l.At.Local().Format("02/01 15:04") + " " + shortCommit(l.Commit)
		c := t.ok
		if l.Error != "" {
			c = t.danger
			line += " (falhou"
			if l.RolledBack {
				line += "; versao anterior no ar"
			}
			if at, ok := a.AutoRetryAt(); ok && a.AutoDeploy {
				line += "; tenta de novo as " + at.Local().Format("15:04")
			}
			line += ")"
		}
		switch {
		case l.PromotedFrom != "":
			line += " · promovido de " + l.PromotedFrom
		case l.Auto:
			line += " · automatico"
		}
		fmt.Fprintf(b, "  %-14s %s\n", "ult. deploy", t.fg(c).Render(line))
		if len(l.Problems) > 0 {
			b.WriteString(lipgloss.NewStyle().Width(w).Foreground(t.danger).Render("    compose recusado (corrija no repositorio; modelo na Ajuda):") + "\n")
		}
		for _, pr := range l.Problems {
			b.WriteString(lipgloss.NewStyle().Width(w).Foreground(t.danger).Render("    - "+pr) + "\n")
		}
		for _, wn := range l.Warnings {
			b.WriteString(lipgloss.NewStyle().Width(w).Foreground(t.warn).Render("    aviso: "+wn) + "\n")
		}
	}
	if len(a.Containers) > 0 {
		b.WriteString("\n" + t.rule(w) + "\n" + t.bold().Render("Containers") + "\n")
		for _, c := range a.Containers {
			state := c.State
			if c.Health != "" {
				state += " (" + c.Health + ")"
			}
			fmt.Fprintf(b, "  %-30s %s\n", ansi.Truncate(c.Name, 30, "…"), state)
		}
	}
	b.WriteString("\n" + t.rule(w) + "\n")
	p.title(b, "Deploy", "info-deploy")
	p.row(b, "ref")
	p.picker(b, "auto", autoText(a.AutoDeploy, a.Branch))
	b.WriteString("\n")
	p.inline(b, "deploy", "restart", "stop")
	p.viewHistory(b, a, w)
	p.viewPromotion(b, a, w)

	b.WriteString("\n" + t.rule(w) + "\n")
	p.title(b, "Repositorio", "info-repo")
	p.row(b, "repo")
	p.row(b, "branch")
	p.row(b, "compose")
	b.WriteString("\n")
	p.inline(b, "app-save")

	b.WriteString("\n" + t.rule(w) + "\n")
	p.title(b, "Excluir app", "info-delete")
	p.delConfirm.Placeholder = "digite " + a.Name + " pra confirmar"
	p.row(b, "delete-confirm")
	b.WriteString("\n")
	for _, f := range p.fields {
		if f.id == "delete-volumes" {
			f.label, f.kind = "Volumes (dados): manter", btnNeutral
			if p.delVolumes {
				f.label, f.kind = "Volumes (dados): APAGAR", btnDanger
			}
		}
	}
	p.inline(b, "delete-volumes", "delete")
}

// viewHistory e o historico de deploys da aba Geral, com o "voltar" nos
// que deram certo.
func (p *appsPage) viewHistory(b *strings.Builder, a core.AppStatus, w int) {
	if len(a.History) == 0 {
		return
	}
	t := p.th
	b.WriteString("\n" + t.bold().Render("Historico") + "  " + t.fg(t.muted).Render("ultimos deploys; voltar usa as imagens guardadas (sem build)") + "\n")
	if a.AutoHold != "" && a.AutoDeploy {
		b.WriteString(lipgloss.NewStyle().Width(w).Foreground(t.warn).Render("  Deploy automatico esperando um commit novo: depois do rollback, "+
			shortCommit(a.AutoHold)+" nao sobe sozinho.") + "\n")
	}
	shown, withBtn := 0, map[string]bool{}
	for _, d := range a.History {
		if shown == 8 {
			break
		}
		shown++
		origin := "manual"
		switch {
		case d.Rollback:
			origin = "rollback"
		case d.Auto:
			origin = "automatico"
		case d.PromotedFrom != "":
			origin = "de " + d.PromotedFrom
		}
		line := fmt.Sprintf("  %s  %s  %-10s ", d.At.Local().Format("02/01 15:04"), shortCommit(d.Commit), origin)
		c, result := t.ok, "ok"
		if !d.OK() {
			c, result = t.danger, "falhou"
		}
		if d.OK() && d.Commit == a.Commit {
			result = "no ar"
		}
		row := line + t.fg(c).Render(fmt.Sprintf("%-7s", result))
		// o botao fica na linha mais nova do commit (um por commit)
		if !withBtn[d.Commit] {
			for i, f := range p.fields {
				if f.id == "rollback:"+d.Commit {
					withBtn[d.Commit] = true
					if p.inForm && i == p.focus {
						p.markFocus(b, 1)
					}
					row += " " + p.zones.Mark(f.id, p.render(f, i))
				}
			}
		}
		b.WriteString(ansi.Truncate(row, w, "…") + "\n")
	}
}

func (p *appsPage) viewEnv(b *strings.Builder, w int) {
	t := p.th
	muted := t.fg(t.muted)
	// o valor ocupa o que sobra da linha: marcador (2), nome (22+1), os
	// espacos e o [✕ remover] (11)
	valueW := max(20, w-2-23-1-1-1-11)
	for _, r := range p.envRows {
		if r.value.Width() != valueW {
			r.value.SetWidth(valueW)
			if !r.value.Focused() {
				// o textinput so recalcula a janela visivel andando o cursor
				r.value.CursorEnd()
				r.value.CursorStart()
			}
		}
	}
	if len(p.envRows) == 0 {
		b.WriteString(muted.Render("  nenhuma variavel: adicione uma ou importe um .env") + "\n")
	} else {
		head := t.fg(t.muted).Bold(true)
		b.WriteString("  " + head.Width(24).Render("Nome") + head.Render("Valor") + "\n")
	}
	for i := range p.envRows {
		p.envRow(b, i)
	}
	b.WriteString("\n")
	p.inline(b, "env-add", "env-import", "env-save")
	if p.envDirty() {
		b.WriteString(t.fg(t.warn).Render("  alteracoes nao salvas: valem so depois do Salvar") + "\n")
	}

	if p.importing {
		b.WriteString("\n" + t.bold().Render("Importar .env") + "\n")
		b.WriteString(muted.Width(w).Render("Cole aqui o conteudo do .env (Ctrl+Shift+V ou o colar do terminal). "+
			"Variavel que ja existe recebe o valor novo; nada e gravado antes do Salvar.") + "\n\n")
		box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(t.muted).Padding(0, 1).Width(w - 2)
		if p.importText == "" {
			b.WriteString(box.Render(muted.Italic(true).Render("aguardando o texto colado...")) + "\n")
		} else if env, err := apps.ParseEnv([]byte(p.importText)); err == nil {
			now := p.envNow()
			var lines []string
			for _, k := range slices.Sorted(maps.Keys(env)) {
				tag := t.fg(t.ok).Render("nova")
				if v, ok := now[k]; ok {
					tag = muted.Render("igual")
					if v != env[k] {
						tag = t.fg(t.warn).Render("troca o valor")
					}
				}
				lines = append(lines, fmt.Sprintf("%-24s %s", k, tag))
			}
			if len(lines) == 0 {
				lines = []string{muted.Render("nenhuma variavel no texto colado")}
			}
			b.WriteString(box.Render(strings.Join(lines, "\n")) + "\n")
		}
		if p.importErr != "" {
			b.WriteString(t.fg(t.danger).Width(w).Render("  "+p.importErr) + "\n")
		}
		b.WriteString("\n")
		p.inline(b, "env-import-apply", "env-import-cancel")
	}

	if a := p.selected(); a != nil {
		if infra := apps.InfraVars(a.App); len(infra) > 0 {
			b.WriteString("\n" + muted.Width(w).Render("Dos bancos e buckets ligados (no deploy): "+strings.Join(infra, ", ")) + "\n")
		}
	}
	b.WriteString("\n" + muted.Width(w).Render(
		"Todas, senhas e chaves inclusive: o cais run injeta no processo do app (o entrypoint do compose, "+
			"veja a Ajuda) e nada vai pro docker inspect. Valem no proximo deploy. Valor com varias linhas "+
			"(chave PEM, JSON): cole no campo do valor.") + "\n")
}

// envRow escreve uma variavel numa linha: nome, valor e [✕ remover], com o
// marcador na linha que tem o foco.
func (p *appsPage) envRow(b *strings.Builder, i int) {
	t := p.th
	marker := "  "
	var parts []string
	for _, id := range []string{fmt.Sprintf("env-name:%d", i), fmt.Sprintf("env-value:%d", i), fmt.Sprintf("env-del:%d", i)} {
		for j, f := range p.fields {
			if f.id != id {
				continue
			}
			if p.inForm && j == p.focus {
				marker = t.fg(t.accent).Bold(true).Render("› ")
				p.markFocus(b, 1)
			}
			parts = append(parts, p.zones.Mark(f.id, p.render(f, j)))
		}
	}
	b.WriteString(marker + strings.Join(parts, " ") + "\n")
}

// textos de ajuda, abertos pelo [i] de cada parte
var (
	infoDeploy = infoBox{"Deploy", "Baixa o commit (vazio = o mais novo da branch), faz o build com a versao atual " +
		"ainda no ar e sobe. Se o up falhar ou um container nao ficar de pe em 15 segundos, o commit anterior volta.\n\n" +
		"Reiniciar reinicia os containers sem baixar nada; Parar derruba o app (os dados ficam)."}
	infoPromote = infoBox{"Homologacao e promocao", "A homologacao e um app separado (mesmo repositorio, compose proprio, " +
		"rotas com o nome dela): sobe uma versao de teste sem tocar na producao. Promover sobe na producao o commit que esta " +
		"no ar e de pe na homologacao, pelo deploy normal (build com a versao atual no ar; se falhar, a anterior volta).\n\n" +
		"O commit validado tem que estar na branch da producao: na mesma branch isso ja vale; com branch propria, faca o merge " +
		"(PR) antes, inclusive squash. Assim um deploy futuro da branch nao desfaz a promocao. Voltar a producao pra um " +
		"commit mais velho pede confirmacao.\n\n" +
		"Deploy automatico (em cada app): homolog automatica com promocao manual, ou homolog e producao automaticas (a " +
		"producao sobe no merge na branch dela)."}
	infoRepo = infoBox{"Repositorio", "De onde o cais baixa o codigo: o repositorio do GitHub, a branch e o arquivo " +
		"compose de producao. Vale no proximo deploy.\n\n" +
		"Branch vazia usa a principal do repositorio. A Ajuda mostra como escrever o compose."}
	infoDatabases = infoBox{"Bancos da infra", "No deploy o app recebe, pelo cais run, as variaveis " +
		"<ENGINE>_<NOME>_USER, _PASS e _URL de cada banco ligado (URL pronta; o database tem o nome do usuario).\n\n" +
		"O app chega no banco por postgres:5432 ou mysql:3306. Desligar nao apaga o database. Crie bancos na aba Infra."}
	infoBuckets = infoBox{"Buckets do Garage", "No deploy o app recebe, pelo cais run, as variaveis " +
		"S3_<BUCKET>_ACCESS_KEY, _SECRET_KEY e _ENDPOINT de cada bucket ligado (- do nome vira _).\n\n" +
		"Endpoint http://garage:3900, regiao garage, path-style (rede garage_network). " +
		"Desligar nao apaga o bucket. Crie buckets na aba Infra -> Garage."}
)

// mark escreve o campo id na linha atual (um so, de uma linha).
func (p *appsPage) mark(b *strings.Builder, id string) {
	for i, f := range p.fields {
		if f.id == id {
			if p.inForm && i == p.focus {
				p.markFocus(b, 1)
			}
			b.WriteString(p.zones.Mark(f.id, p.render(f, i)))
		}
	}
}

// title escreve o titulo de uma parte com o [i] que abre a ajuda dela.
func (p *appsPage) title(b *strings.Builder, text, infoID string) {
	b.WriteString(p.th.bold().Render(text) + " ")
	p.mark(b, infoID)
	b.WriteString("\n")
}

// pickLink escreve o seletor e o botao de ligar lado a lado.
func (p *appsPage) pickLink(b *strings.Builder, pickID, text, linkID string) {
	t := p.th
	var left, right string
	for i, f := range p.fields {
		focused := p.inForm && i == p.focus
		switch f.id {
		case pickID:
			marker, ls := "  ", t.fg(t.text).Width(18)
			if focused {
				marker, ls = t.fg(t.accent).Bold(true).Render("› "), t.fg(t.accent).Bold(true).Width(18)
			}
			left = p.zones.Mark(f.id, marker+ls.Render(f.label)+t.selector(text, focused))
		case linkID:
			right = p.zones.Mark(f.id, p.render(f, i))
		default:
			continue
		}
		if focused {
			p.markFocus(b, lipgloss.Height(left))
		}
	}
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Center, left, "  ", right) + "\n")
}

// viewLinks e a secao Bancos e buckets: os da infra ligados ao app.
func (p *appsPage) viewLinks(b *strings.Builder, a core.AppStatus, w int) {
	t := p.th
	muted := t.fg(t.muted)

	p.title(b, "Bancos da infra", "info-dbs")
	if len(a.Databases) == 0 {
		b.WriteString(muted.Render("  nenhum banco ligado") + "\n")
	}
	for _, l := range a.Databases {
		fmt.Fprintf(b, "  %-22s %s ", l.ID(), muted.Render(strings.Join(l.Vars(), ", ")))
		p.mark(b, "db-unlink:"+l.ID())
		b.WriteString("\n")
	}
	list := p.linkable()
	pick := "nenhum banco criado (aba Infra)"
	if len(list) > 0 {
		d := list[min(p.dbPick, len(list)-1)]
		pick = "‹ " + d.Engine + "/" + d.Name + " ›"
	}
	p.pickLink(b, "db-pick", pick, "db-link")

	b.WriteString("\n" + t.rule(w) + "\n")
	p.title(b, "Buckets do Garage", "info-buckets")
	if len(a.Buckets) == 0 {
		b.WriteString(muted.Render("  nenhum bucket ligado") + "\n")
	}
	for _, l := range a.Buckets {
		fmt.Fprintf(b, "  %-22s %s ", l.Name, muted.Render(strings.Join(l.Vars(), ", ")))
		p.mark(b, "bucket-unlink:"+l.Name)
		b.WriteString("\n")
	}
	bl := p.linkableBuckets()
	pick = "nenhum bucket criado (aba Infra -> Garage)"
	if len(bl) > 0 {
		pick = "‹ " + bl[min(p.bucketPick, len(bl)-1)].Name + " ›"
	}
	p.pickLink(b, "bucket-pick", pick, "bucket-link")
}

// integracao com o modelo (logs usam o visualizador comum com a Infra)
