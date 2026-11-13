package ui

import (
	"context"
	"fmt"
	"image/color"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/github"
)

// secoes do detalhe de um app
var appSections = []string{"Geral", "Variaveis", "Secrets", "Ajuda", "Logs"}

const (
	secGeneral = iota
	secEnv
	secSecrets
	secGuide
	secLogs
)

// field e cada coisa focavel de um formulario da aba Apps, na ordem do Tab:
// campo de texto ou botao.
type field struct {
	id       string
	label    string
	input    *textinput.Model
	press    func() tea.Cmd
	disabled func() bool
	kind     btnKind // cor do botao (verde, vermelho...)
	chip     bool    // botao dentro de uma linha de tabela: [texto], sem fundo
}

func (f *field) off() bool { return f.disabled != nil && f.disabled() }

type envRow struct{ name, value *textinput.Model }

// appsPage e a aba Apps: a lista a esquerda e, a direita, o detalhe do app
// selecionado (deploy, variaveis, secrets, ajuda, logs) ou o cadastro de um
// app novo. So chama acoes do core; o estado dos apps chega pelo estado.
type appsPage struct {
	api   core.API
	zones *zone.Manager
	th    theme

	list     []core.AppStatus
	sel      int
	inForm   bool // o foco esta no formulario (senao, na lista)
	section  int
	creating bool
	fields   []*field
	focus    int

	// editores do app selecionado: so recarregam quando troca de app ou
	// depois de salvar, pra uma foto nova do estado nao apagar a edicao
	loadedFor   string
	repo        *textinput.Model
	branch      *textinput.Model
	compose     *textinput.Model
	ref         *textinput.Model
	envRows     []*envRow
	envLoaded   string            // app cujas variaveis estao nos editores
	envSaved    map[string]string // como estao gravadas (pra avisar o que falta salvar)
	envPending  map[string]string // enviadas no Salvar, gravadas quando der certo
	importing   bool              // "Importar .env" aberto: o colar vai pra ele
	importText  string            // .env colado, ainda nao aplicado
	importErr   string
	secretName  *textinput.Model
	secretValue *textinput.Model
	guide       string
	guideFor    string
	dbs         []core.DatabaseInfo // bancos da infra (pra ligar no app)
	dbPick      int
	buckets     []core.BucketInfo // buckets do Garage (pra ligar no app)
	bucketPick  int
	delConfirm  *textinput.Model // nome do app digitado pra excluir
	delVolumes  bool             // excluir apaga tambem os volumes

	// app novo
	newName, newRepo, newBranch, newCompose *textinput.Model
	newPromoteTo                            string // o app novo e homologacao deste
	newAuto                                 bool   // deploy automatico no app novo

	// promocao: o que cada homologacao ligada ao app selecionado faria
	// agora (consulta o GitHub; refeita quando os commits no ar mudam)
	promos   map[string]promoResult // homologacao -> resultado
	promoSig map[string]string      // homologacao -> commits no ar quando consultou

	container  int    // logs: container do app selecionado
	pendingSel string // app recem-criado: seleciona quando chegar no estado
	scroll     int
	focusLine  int
	focusH     int // altura do campo com foco (linhas)
	follow     bool
	busy       string
	status     string
	statusC    string
	detailErr  string
	info       *infoBox // ajuda aberta pelo [i] (modal)
}

// respostas das chamadas ao core
type (
	appActionMsg struct {
		app      string
		ok       string
		err      error
		deployed *apps.DeployResult
	}
	appCreatedMsg struct {
		app apps.App
		err error
	}
	appEnvMsg struct {
		app string
		env map[string]string
		err error
	}
	appDBsMsg struct {
		list    []core.DatabaseInfo
		buckets []core.BucketInfo
		err     error
	}
	appGuideMsg struct {
		app  string
		text string
		err  error
	}
	appPromoMsg struct {
		from string
		sig  string
		p    apps.Promotion
		err  error
	}
	appLogsMsg struct {
		generation uint64
		text       string
		err        error
	}
)

// promoResult e a resposta do CheckPromotion de uma homologacao.
type promoResult struct {
	p   apps.Promotion
	err string
}

func newAppsPage(api core.API, zones *zone.Manager, th theme) *appsPage {
	p := &appsPage{api: api, zones: zones, th: th, promos: map[string]promoResult{}, promoSig: map[string]string{}}
	p.repo = p.input("dono/repositorio", 36)
	p.branch = p.input("a principal do repositorio", 28)
	p.compose = p.input(apps.DefaultCompose, 30)
	p.ref = p.input("vazio = mais novo da branch", 30)
	p.secretName = p.input("nome (ex: jwt_secret)", 26)
	p.secretValue = p.input("valor", 30)
	p.delConfirm = p.input("digite o nome do app", 34)
	p.newName = p.input("nome do app (minusculas)", 30)
	p.newRepo = p.input("https://github.com/dono/repo", 40)
	p.newBranch = p.input("a principal do repositorio", 28)
	p.newCompose = p.input(apps.DefaultCompose, 30)
	p.rebuild()
	return p
}

func (p *appsPage) input(placeholder string, width int) *textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Placeholder = placeholder
	ti.SetWidth(width)
	p.style(&ti)
	return &ti
}

func (p *appsPage) style(ti *textinput.Model) { p.th.styleInput(ti) }

func (p *appsPage) setTheme(th theme) {
	p.th = th
	for _, ti := range []*textinput.Model{p.repo, p.branch, p.compose, p.ref, p.secretName, p.secretValue,
		p.newName, p.newRepo, p.newBranch, p.newCompose, p.delConfirm} {
		p.style(ti)
	}
	for _, r := range p.envRows {
		p.style(r.name)
		p.style(r.value)
	}
}

// typing diz se o foco esta num campo de texto (numeros nao trocam de aba).
func (p *appsPage) typing() bool {
	return p.inForm && p.focus < len(p.fields) && p.fields[p.focus].input != nil
}

func (p *appsPage) selected() *core.AppStatus {
	if p.creating || p.sel >= len(p.list) {
		return nil
	}
	return &p.list[p.sel]
}

// onState recebe a foto nova: a lista sempre acompanha; os editores so
// recarregam quando o app selecionado muda.
func (p *appsPage) onState(s core.State) tea.Cmd {
	name := ""
	if a := p.selected(); a != nil {
		name = a.Name
	}
	p.list = nestHomologs(s.Apps.List)
	if p.pendingSel != "" {
		name = p.pendingSel
	}
	for i, a := range p.list {
		if a.Name == name {
			p.sel = i
			if name == p.pendingSel {
				p.pendingSel = ""
			}
		}
	}
	if p.sel >= len(p.list) {
		p.sel = max(0, len(p.list)-1)
	}
	if !p.creating {
		p.rebuild() // os secrets (botoes de apagar) vem do estado
	}
	return tea.Batch(p.loadSelected(false), p.promoRefresh(false))
}

// nestHomologs poe cada homologacao logo depois da producao dela.
func nestHomologs(list []core.AppStatus) []core.AppStatus {
	has := map[string]bool{}
	for _, a := range list {
		has[a.Name] = true
	}
	out := make([]core.AppStatus, 0, len(list))
	for _, a := range list {
		if a.PromoteTo != "" && has[a.PromoteTo] {
			continue
		}
		out = append(out, a)
		for _, h := range list {
			if h.PromoteTo == a.Name {
				out = append(out, h)
			}
		}
	}
	return out
}

// homologs sao as homologacoes ligadas ao app: ele mesmo, se for uma, ou
// as que promovem pra ele.
func (p *appsPage) homologs(a core.AppStatus) []core.AppStatus {
	var out []core.AppStatus
	for _, h := range p.list {
		if (a.PromoteTo != "" && h.Name == a.Name) || h.PromoteTo == a.Name {
			out = append(out, h)
		}
	}
	return out
}

func (p *appsPage) app(name string) *core.AppStatus {
	for i := range p.list {
		if p.list[i].Name == name {
			return &p.list[i]
		}
	}
	return nil
}

// promoRefresh consulta de novo a promocao das homologacoes ligadas ao app
// selecionado quando os commits no ar mudaram (ou sempre, com force).
func (p *appsPage) promoRefresh(force bool) tea.Cmd {
	a := p.selected()
	if a == nil || p.section != secGeneral {
		return nil
	}
	var cmds []tea.Cmd
	for _, h := range p.homologs(*a) {
		sig := h.Commit
		if h.Last != nil {
			sig += "|" + h.Last.Commit + h.Last.At.String()
		}
		if prod := p.app(h.PromoteTo); prod != nil {
			sig += "|" + prod.Commit
		}
		if !force && p.promoSig[h.Name] == sig {
			continue
		}
		p.promoSig[h.Name] = sig
		from := h.Name
		cmds = append(cmds, func() tea.Msg {
			pr, err := p.api.CheckPromotion(context.Background(), from)
			return appPromoMsg{from: from, sig: sig, p: pr, err: err}
		})
	}
	return tea.Batch(cmds...)
}

// loadSelected preenche os editores com o app selecionado e busca as
// variaveis (ficam fora do estado: podem ter segredo) e a ajuda.
func (p *appsPage) loadSelected(force bool) tea.Cmd {
	a := p.selected()
	if a == nil {
		p.loadedFor = ""
		p.rebuild()
		return nil
	}
	if !force && a.Name == p.loadedFor {
		return nil
	}
	p.loadedFor = a.Name
	p.container = 0
	p.detailErr = ""
	p.repo.SetValue(a.Repo)
	p.branch.SetValue(a.Branch)
	p.compose.SetValue(a.Compose)
	p.ref.SetValue("")
	p.secretName.SetValue("")
	p.secretValue.SetValue("")
	p.delConfirm.SetValue("")
	p.delVolumes = false
	p.dbPick, p.bucketPick = 0, 0
	p.envRows, p.envLoaded, p.envSaved, p.guide, p.guideFor = nil, "", nil, "", ""
	p.closeImport()
	p.rebuild()
	return tea.Batch(p.envCmd(a.Name), p.guideCmd(a.Name), p.dbsCmd())
}

// sectionCmd atualiza o que a secao aberta mostra: em Secrets, os bancos e
// buckets que podem ser ligados (criados depois que o app foi aberto).
func (p *appsPage) sectionCmd() tea.Cmd {
	if p.section == secSecrets {
		return p.dbsCmd()
	}
	return nil
}

func (p *appsPage) dbsCmd() tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		list, err := p.api.Databases(ctx)
		if err != nil {
			return appDBsMsg{err: err}
		}
		buckets, err := p.api.Buckets(ctx)
		return appDBsMsg{list, buckets, err}
	}
}

// buckets que ainda nao estao ligados no app selecionado
func (p *appsPage) linkableBuckets() []core.BucketInfo {
	a := p.selected()
	var out []core.BucketInfo
	for _, bk := range p.buckets {
		if a != nil && slices.ContainsFunc(a.Buckets, func(l apps.BucketLink) bool { return l.Name == bk.Name }) {
			continue
		}
		out = append(out, bk)
	}
	return out
}

// bancos que ainda nao estao ligados no app selecionado
func (p *appsPage) linkable() []core.DatabaseInfo {
	a := p.selected()
	var out []core.DatabaseInfo
	for _, d := range p.dbs {
		if a != nil && slices.ContainsFunc(a.Databases, func(l apps.DBLink) bool { return l.Engine == d.Engine && l.Name == d.Name }) {
			continue
		}
		out = append(out, d)
	}
	return out
}

func (p *appsPage) envCmd(name string) tea.Cmd {
	return func() tea.Msg {
		env, err := p.api.AppEnv(context.Background(), name)
		return appEnvMsg{name, env, err}
	}
}

func (p *appsPage) guideCmd(name string) tea.Cmd {
	return func() tea.Msg {
		text, err := p.api.AppGuide(context.Background(), name)
		return appGuideMsg{name, text, err}
	}
}

func (p *appsPage) newEnvRow(name, value string) *envRow {
	r := &envRow{name: p.input("VARIAVEL", 22), value: p.input("valor", 40)}
	r.name.SetValue(name)
	r.value.SetValue(value)
	r.value.CursorStart() // valor comprido mostra o comeco, nao o fim
	return r
}

func del[T any](list []T, i int) []T {
	if i < 0 || i >= len(list) {
		return list
	}
	return append(list[:i:i], list[i+1:]...)
}

// rebuild monta os campos do formulario atual (secao do app ou app novo).
func (p *appsPage) rebuild() {
	p.fields = nil
	add := func(f *field) { p.fields = append(p.fields, f) }
	btn := func(id, label string, press func() tea.Cmd) *field {
		return &field{id: id, label: label, press: press, disabled: func() bool { return p.busy != "" }}
	}

	if p.creating {
		add(&field{id: "new-name", label: "Nome do app", input: p.newName})
		add(&field{id: "new-repo", label: "Repositorio", input: p.newRepo})
		add(&field{id: "new-branch", label: "Branch", input: p.newBranch})
		add(&field{id: "new-compose", label: "Compose", input: p.newCompose})
		add(&field{id: "new-auto", label: "Deploy", press: func() tea.Cmd { p.newAuto = !p.newAuto; return nil }})
		add(btn("new-create", "Criar", p.create))
		cancel := btn("new-cancel", "Cancelar", p.cancelCreate)
		cancel.kind = btnNeutral
		add(cancel)
		p.setFocus(p.focus)
		return
	}

	a := p.selected()
	if a == nil {
		p.setFocus(0)
		return
	}
	// info e o [i] ao lado do titulo de uma parte: abre a ajuda dela
	info := func(id string, box infoBox) {
		add(&field{id: id, label: "i", chip: true, press: func() tea.Cmd { p.info = &box; return nil }})
	}
	switch p.section {
	case secGeneral:
		info("info-deploy", infoDeploy)
		add(&field{id: "ref", label: "Commit ou branch", input: p.ref})
		add(&field{id: "auto", label: "Deploy", press: p.toggleAuto, disabled: func() bool { return p.busy != "" }})
		add(btn("deploy", "Deploy", p.deploy))
		restart, stop := btn("restart", "Reiniciar", p.restart), btn("stop", "Parar", p.stop)
		restart.kind, stop.kind = btnInfo, btnWarn
		add(restart)
		add(stop)

		// promocao: na homologacao e na producao que tem homologacao
		hs := p.homologs(*a)
		if len(hs) > 0 {
			info("info-promote", infoPromote)
			add(btn("promo-refresh", "atualizar", func() tea.Cmd { return p.promoRefresh(true) }))
			p.fields[len(p.fields)-1].chip = true
		}
		for _, h := range hs {
			from := h.Name
			r, ok := p.promos[from]
			f := btn("promote:"+from, "Promover", func() tea.Cmd { return p.promote(from, r.p.State == apps.PromoteOlder) })
			if ok && r.p.Deploy != "" {
				f.label = "Promover " + shortCommit(r.p.Deploy)
			}
			if ok && r.p.State == apps.PromoteOlder {
				f.label, f.kind = "Promover "+shortCommit(r.p.Deploy)+" mesmo assim", btnDanger
			}
			f.disabled = func() bool {
				r, ok := p.promos[from]
				return p.busy != "" || !ok || r.err != "" || (r.p.State != apps.PromoteReady && r.p.State != apps.PromoteOlder)
			}
			add(f)
		}
		if len(hs) == 0 && a.PromoteTo == "" {
			add(btn("homolog-new", "Criar homologacao", p.startHomolog))
		}
		info("info-repo", infoRepo)
		add(&field{id: "repo", label: "Repositorio", input: p.repo})
		add(&field{id: "branch", label: "Branch", input: p.branch})
		add(&field{id: "compose", label: "Compose", input: p.compose})
		add(btn("app-save", "Salvar", p.saveApp))
		info("info-delete", infoBox{"Excluir app", "Apaga containers, imagens, codigo, variaveis e secrets do app. " +
			"O database e os buckets da infra ficam (aba Infra).\n\n" +
			"Digite " + a.Name + " no Confirmar pra liberar o botao. Os volumes (dados) so sao apagados se voce trocar " +
			"Volumes pra APAGAR; nao tem volta."})
		add(&field{id: "delete-confirm", label: "Confirmar", input: p.delConfirm})
		add(btn("delete-volumes", "", func() tea.Cmd { p.delVolumes = !p.delVolumes; return nil }))
		name := a.Name
		add(&field{id: "delete", label: "Excluir app", kind: btnDanger, press: p.deleteApp, disabled: func() bool {
			return p.busy != "" || strings.TrimSpace(p.delConfirm.Value()) != name
		}})
	case secEnv:
		for i, r := range p.envRows {
			add(&field{id: fmt.Sprintf("env-name:%d", i), input: r.name})
			add(&field{id: fmt.Sprintf("env-value:%d", i), input: r.value})
			rm := btn(fmt.Sprintf("env-del:%d", i), "✕ remover", func() tea.Cmd { p.envRows = del(p.envRows, i); p.rebuild(); return nil })
			rm.kind, rm.chip = btnDanger, true
			add(rm)
		}
		add(btn("env-add", "+ Nova variavel", func() tea.Cmd {
			p.envRows = append(p.envRows, p.newEnvRow("", ""))
			p.rebuild()
			return p.setFocus(len(p.envRows)*3 - 3) // o nome da nova
		}))
		add(btn("env-import", "Importar .env", func() tea.Cmd { p.importing = true; p.importText, p.importErr = "", ""; p.rebuild(); return nil }))
		add(btn("env-save", "Salvar", p.saveEnv))
		if p.importing {
			add(&field{id: "env-import-apply", label: "Aplicar", press: p.applyImport, disabled: func() bool { return p.importText == "" }})
			cancel := btn("env-import-cancel", "Cancelar", func() tea.Cmd { p.closeImport(); p.rebuild(); return nil })
			cancel.kind = btnNeutral
			add(cancel)
		}
	case secSecrets:
		info("info-secrets", infoSecrets)
		for _, s := range a.Secrets {
			if s.Infra != "" {
				continue // credencial de banco ou bucket: desliga por ele
			}
			name := s.Name
			f := btn("secret-del:"+name, "✕ apagar", func() tea.Cmd { return p.deleteSecret(name) })
			f.kind, f.chip = btnDanger, true
			add(f)
		}
		add(&field{id: "secret-name", label: "Secret", input: p.secretName})
		add(&field{id: "secret-value", label: "Valor", input: p.secretValue})
		add(btn("secret-save", "Gravar", p.saveSecret))

		info("info-dbs", infoDatabases)
		for _, l := range a.Databases {
			l := l
			f := btn("db-unlink:"+l.ID(), "desligar", func() tea.Cmd { return p.unlinkDB(l) })
			f.kind, f.chip = btnDanger, true
			add(f)
		}
		add(&field{id: "db-pick", label: "Banco", press: func() tea.Cmd {
			if n := len(p.linkable()); n > 0 {
				p.dbPick = (p.dbPick + 1) % n
			}
			return nil
		}})
		add(btn("db-link", "Ligar banco", p.linkDB))

		info("info-buckets", infoBuckets)
		for _, l := range a.Buckets {
			l := l
			f := btn("bucket-unlink:"+l.Name, "desligar", func() tea.Cmd { return p.unlinkBucket(l) })
			f.kind, f.chip = btnDanger, true
			add(f)
		}
		add(&field{id: "bucket-pick", label: "Bucket", press: func() tea.Cmd {
			if n := len(p.linkableBuckets()); n > 0 {
				p.bucketPick = (p.bucketPick + 1) % n
			}
			return nil
		}})
		add(btn("bucket-link", "Ligar bucket", p.linkBucket))
	case secLogs:
		add(&field{id: "log-container", label: "Container", press: func() tea.Cmd {
			if n := len(a.Containers); n > 0 {
				p.container = (p.container + 1) % n
			}
			return nil
		}})
	}
	p.setFocus(p.focus)
}

func (p *appsPage) setFocus(i int) tea.Cmd {
	n := len(p.fields)
	if n == 0 {
		p.focus = 0
		return nil
	}
	p.focus = ((i % n) + n) % n
	p.follow = true
	var cmd tea.Cmd
	for j, f := range p.fields {
		if f.input == nil {
			continue
		}
		if p.inForm && j == p.focus {
			cmd = f.input.Focus()
		} else {
			f.input.Blur()
		}
	}
	return cmd
}

func (p *appsPage) setStatus(kind, msg string) { p.statusC, p.status = kind, msg }

// acoes

func (p *appsPage) checkCmd() tea.Cmd {
	return func() tea.Msg {
		p.api.CheckApps(context.Background())
		return nil
	}
}

// do roda uma acao do core fora do Update e devolve o resultado.
func (p *appsPage) do(app, label, ok string, fn func(ctx context.Context) error) tea.Cmd {
	if p.busy != "" {
		return nil
	}
	p.busy = label
	p.detailErr = ""
	p.setStatus("warn", label+"...")
	return func() tea.Msg {
		return appActionMsg{app: app, ok: ok, err: fn(context.Background())}
	}
}

func (p *appsPage) deploy() tea.Cmd {
	a := p.selected()
	if a == nil || p.busy != "" {
		return nil
	}
	name, ref := a.Name, strings.TrimSpace(p.ref.Value())
	p.busy = "deploy"
	p.detailErr = ""
	p.setStatus("warn", "Deploy de "+name+": baixando, build (a versao atual continua no ar) e up...")
	return func() tea.Msg {
		res, err := p.api.DeployApp(context.Background(), name, ref)
		return appActionMsg{app: name, err: err, deployed: &res}
	}
}

func (p *appsPage) restart() tea.Cmd {
	a := p.selected()
	if a == nil {
		return nil
	}
	name := a.Name
	return p.do(name, "Reiniciando "+name, name+" reiniciado.", func(ctx context.Context) error {
		return p.api.RestartApp(ctx, name, "")
	})
}

func (p *appsPage) stop() tea.Cmd {
	a := p.selected()
	if a == nil {
		return nil
	}
	name := a.Name
	return p.do(name, "Parando "+name, name+" parado (Reiniciar volta).", func(ctx context.Context) error {
		return p.api.StopApp(ctx, name)
	})
}

func (p *appsPage) deleteApp() tea.Cmd {
	a := p.selected()
	if a == nil || strings.TrimSpace(p.delConfirm.Value()) != a.Name {
		return nil
	}
	name, volumes := a.Name, p.delVolumes
	msg := name + " excluido (o database da infra fica)."
	if volumes {
		msg = name + " excluido, com os volumes (o database da infra fica)."
	}
	return p.do(name, "Excluindo "+name, msg, func(ctx context.Context) error {
		return p.api.DeleteApp(ctx, name, volumes)
	})
}

func (p *appsPage) saveApp() tea.Cmd {
	a := p.selected()
	if a == nil {
		return nil
	}
	repo, err := github.ParseRepo(p.repo.Value())
	if err != nil {
		p.setStatus("danger", err.Error())
		return nil
	}
	upd := a.App // mantem a producao (homologacao) e o deploy automatico
	upd.Repo, upd.Branch, upd.Compose = repo.String(), strings.TrimSpace(p.branch.Value()), strings.TrimSpace(p.compose.Value())
	return p.do(a.Name, "Salvando", a.Name+" salvo; vale no proximo deploy.", func(ctx context.Context) error {
		return p.api.UpdateApp(ctx, upd)
	})
}

// toggleAuto liga ou desliga o deploy automatico do app (grava na hora).
func (p *appsPage) toggleAuto() tea.Cmd {
	a := p.selected()
	if a == nil {
		return nil
	}
	upd := a.App
	upd.AutoDeploy = !upd.AutoDeploy
	ok := "Deploy de " + a.Name + " manual: so pelo botao (ou CLI/MCP)."
	if upd.AutoDeploy {
		ok = "Deploy automatico em " + a.Name + ": cada commit novo na " + branchName(a.Branch) + " sobe sozinho (consulta a cada minuto)."
	}
	return p.do(a.Name, "Salvando", ok, func(ctx context.Context) error { return p.api.UpdateApp(ctx, upd) })
}

func branchName(b string) string {
	if b == "" {
		return "branch principal"
	}
	return b
}

// promote sobe na producao o commit validado na homologacao from.
func (p *appsPage) promote(from string, confirm bool) tea.Cmd {
	r, ok := p.promos[from]
	if !ok || p.busy != "" {
		return nil
	}
	to := r.p.To
	p.busy = "deploy"
	p.detailErr = ""
	p.setStatus("warn", "Promovendo "+shortCommit(r.p.Deploy)+" pra "+to+": build (a versao atual continua no ar) e up...")
	return func() tea.Msg {
		res, err := p.api.PromoteApp(context.Background(), from, confirm)
		return appActionMsg{app: to, err: err, deployed: &res}
	}
}

// startHomolog abre o cadastro de uma homologacao do app: mesmo
// repositorio, ligada a ele.
func (p *appsPage) startHomolog() tea.Cmd {
	a := p.selected()
	if a == nil {
		return nil
	}
	prod := *a
	cmd := p.startCreate()
	p.newName.SetValue(prod.Name + "-homolog")
	p.newRepo.SetValue(prod.Repo)
	p.newPromoteTo = prod.Name
	p.rebuild()
	return tea.Batch(cmd, p.setFocus(2)) // a branch
}

func (p *appsPage) saveEnv() tea.Cmd {
	a := p.selected()
	if a == nil {
		return nil
	}
	env := map[string]string{}
	for _, r := range p.envRows {
		k := strings.TrimSpace(r.name.Value())
		if k == "" {
			continue
		}
		if _, dup := env[k]; dup {
			p.setStatus("danger", "variavel "+k+" repetida")
			return nil
		}
		env[k] = r.value.Value()
	}
	name := a.Name
	p.envPending = env
	return p.do(name, "Salvando variaveis", "Variaveis de "+name+" salvas; valem no proximo deploy.", func(ctx context.Context) error {
		return p.api.SetAppEnv(ctx, name, env)
	})
}

// envNow sao as variaveis como estao nos editores (linhas sem nome ficam fora).
func (p *appsPage) envNow() map[string]string {
	env := map[string]string{}
	for _, r := range p.envRows {
		if k := strings.TrimSpace(r.name.Value()); k != "" {
			env[k] = r.value.Value()
		}
	}
	return env
}

func (p *appsPage) envDirty() bool {
	return p.envSaved != nil && !maps.Equal(p.envNow(), p.envSaved)
}

func (p *appsPage) closeImport() { p.importing, p.importText, p.importErr = false, "", "" }

// applyImport junta o .env colado nos editores: variavel que ja existe
// recebe o valor novo, as outras entram no fim. So grava no Salvar.
func (p *appsPage) applyImport() tea.Cmd {
	env, err := apps.ParseEnv([]byte(p.importText))
	if err != nil {
		p.importErr = err.Error()
		return nil
	}
	keys := slices.Sorted(maps.Keys(env))
	changed, added := 0, 0
	for _, k := range keys {
		i := slices.IndexFunc(p.envRows, func(r *envRow) bool { return strings.TrimSpace(r.name.Value()) == k })
		if i >= 0 {
			if p.envRows[i].value.Value() != env[k] {
				p.envRows[i].value.SetValue(env[k])
				p.envRows[i].value.CursorStart()
				changed++
			}
			continue
		}
		p.envRows = append(p.envRows, p.newEnvRow(k, env[k]))
		added++
	}
	p.closeImport()
	p.rebuild()
	p.setStatus("warn", fmt.Sprintf("Importadas: %d novas, %d com valor trocado. Confira e clique Salvar.", added, changed))
	return p.setFocus(slices.IndexFunc(p.fields, func(f *field) bool { return f.id == "env-save" }))
}

func (p *appsPage) saveSecret() tea.Cmd {
	a := p.selected()
	if a == nil {
		return nil
	}
	secret, value := strings.TrimSpace(p.secretName.Value()), p.secretValue.Value()
	if secret == "" || value == "" {
		p.setStatus("danger", "Informe o nome e o valor do secret.")
		return nil
	}
	name := a.Name
	return p.do(name, "Gravando "+secret, secret+" gravado em ${CAIS_SECRETS}/"+secret+" (vale no proximo deploy).", func(ctx context.Context) error {
		return p.api.SetAppSecret(ctx, name, secret, value)
	})
}

func (p *appsPage) linkDB() tea.Cmd {
	a := p.selected()
	list := p.linkable()
	if a == nil || len(list) == 0 {
		return nil
	}
	d := list[min(p.dbPick, len(list)-1)]
	name := a.Name
	return p.do(name, "Ligando "+d.Engine+"/"+d.Name, name+" ligado a "+d.Engine+"/"+d.Name+": os secrets valem no proximo deploy.", func(ctx context.Context) error {
		return p.api.LinkAppDatabase(ctx, name, d.Engine, d.Name)
	})
}

func (p *appsPage) linkBucket() tea.Cmd {
	a := p.selected()
	list := p.linkableBuckets()
	if a == nil || len(list) == 0 {
		return nil
	}
	bk := list[min(p.bucketPick, len(list)-1)]
	name := a.Name
	return p.do(name, "Ligando o bucket "+bk.Name, name+" ligado ao bucket "+bk.Name+": os secrets valem no proximo deploy.", func(ctx context.Context) error {
		return p.api.LinkAppBucket(ctx, name, bk.Name)
	})
}

func (p *appsPage) unlinkBucket(l apps.BucketLink) tea.Cmd {
	a := p.selected()
	if a == nil {
		return nil
	}
	name := a.Name
	return p.do(name, "Desligando o bucket "+l.Name, name+" desligado do bucket "+l.Name+" (o bucket e os arquivos ficam).", func(ctx context.Context) error {
		return p.api.UnlinkAppBucket(ctx, name, l.Name)
	})
}

func (p *appsPage) unlinkDB(l apps.DBLink) tea.Cmd {
	a := p.selected()
	if a == nil {
		return nil
	}
	name := a.Name
	return p.do(name, "Desligando "+l.ID(), name+" desligado de "+l.ID()+" (o database e os dados ficam).", func(ctx context.Context) error {
		return p.api.UnlinkAppDatabase(ctx, name, l.Engine, l.Name)
	})
}

func (p *appsPage) deleteSecret(secret string) tea.Cmd {
	a := p.selected()
	if a == nil {
		return nil
	}
	name := a.Name
	return p.do(name, "Apagando "+secret, secret+" apagado.", func(ctx context.Context) error {
		return p.api.DeleteAppSecret(ctx, name, secret)
	})
}

func (p *appsPage) startCreate() tea.Cmd {
	p.creating, p.inForm, p.focus = true, true, 0
	p.detailErr = ""
	for _, ti := range []*textinput.Model{p.newName, p.newRepo, p.newBranch, p.newCompose} {
		ti.SetValue("")
	}
	p.newPromoteTo, p.newAuto = "", false
	p.setStatus("", "")
	p.rebuild()
	return p.setFocus(0)
}

func (p *appsPage) cancelCreate() tea.Cmd {
	p.creating, p.inForm = false, false
	p.detailErr = ""
	p.setStatus("", "")
	return tea.Batch(p.loadSelected(true), p.setFocus(0))
}

// create cadastra o app. O nome vazio vira o nome do repositorio.
func (p *appsPage) create() tea.Cmd {
	repo, err := github.ParseRepo(p.newRepo.Value())
	if err != nil {
		p.setStatus("danger", err.Error())
		return nil
	}
	a := apps.App{Name: strings.TrimSpace(p.newName.Value()), Repo: repo.String(),
		Branch: strings.TrimSpace(p.newBranch.Value()), Compose: strings.TrimSpace(p.newCompose.Value()),
		PromoteTo: p.newPromoteTo, AutoDeploy: p.newAuto}
	if a.Name == "" {
		a.Name = strings.ToLower(repo.Name)
	}
	p.busy = "Criando"
	p.detailErr = ""
	p.setStatus("warn", "Criando "+a.Name+"...")
	return func() tea.Msg {
		out, err := p.api.CreateApp(context.Background(), a)
		return appCreatedMsg{out, err}
	}
}

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
	}
	for i, a := range p.list {
		mark, status, c := appStatus(a, t)
		sel := !p.creating && i == p.sel
		title := t.bold().Render(mark+" "+a.Name) + "  " + t.fg(c).Render(status)
		sub := t.fg(t.muted).Render(a.Repo)
		cw, indent := w-4, ""
		if a.PromoteTo != "" && p.app(a.PromoteTo) != nil {
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
		b.WriteString(p.zones.Mark("app:"+a.Name, card) + "\n")
	}
	return lipgloss.NewStyle().Width(w).Height(h).MaxHeight(h).Render(b.String())
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
		case secSecrets:
			p.viewSecrets(&body, *a, inner)
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
			"e outro dominio; variaveis, secrets e banco sao separados dos da producao.") + "\n")
	}
	b.WriteString("\n")
	p.inline(b, "new-create", "new-cancel")
	b.WriteString("\n" + p.th.fg(p.th.muted).Width(w).Render(
		"Nome vazio = o nome do repositorio. O repositorio precisa do compose de producao pronto (labels do "+
			"Traefik, redes, secrets): depois de criar, a Ajuda mostra o modelo. O codigo e baixado pelo GitHub "+
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

	b.WriteString("\n" + muted.Width(w).Render(
		"Viram o .env na pasta do projeto no deploy: o compose usa com env_file: .env ou ${VAR}. "+
			"O cais acrescenta CAIS_APP, CAIS_COMMIT e CAIS_SECRETS. Senhas e chaves vao em Secrets, nao aqui.") + "\n")
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
	infoSecrets = infoBox{"Secrets", "Cada secret e um arquivo em ${CAIS_SECRETS}/<nome>. No compose: " +
		"secrets: {x: {file: ${CAIS_SECRETS}/x}} e o service le /run/secrets/x.\n\n" +
		"O valor so aparece enquanto voce digita e vale no proximo deploy. As credenciais de banco e bucket " +
		"ligados aparecem aqui, mas sao desligadas na parte deles."}
	infoDatabases = infoBox{"Bancos da infra", "No deploy o cais grava usuario e senha de cada banco ligado nos secrets " +
		"<engine>_<nome>_user e <engine>_<nome>_pass (o database tem o nome do usuario); montar a URL e com o app.\n\n" +
		"O app chega no banco por postgres:5432 ou mysql:3306. Desligar nao apaga o database. Crie bancos na aba Infra."}
	infoBuckets = infoBox{"Buckets do Garage", "No deploy o cais grava a chave de cada bucket ligado nos secrets " +
		"s3_<bucket>_access_key e s3_<bucket>_secret_key.\n\n" +
		"O app usa o endpoint http://garage:3900, regiao garage, path-style (rede garage_network). " +
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

func (p *appsPage) viewSecrets(b *strings.Builder, a core.AppStatus, w int) {
	t := p.th
	muted := t.fg(t.muted)

	p.title(b, "Secrets", "info-secrets")
	if len(a.Secrets) == 0 {
		b.WriteString(muted.Render("  nenhum secret") + "\n")
	}
	nameW := 0
	for _, s := range a.Secrets {
		nameW = max(nameW, len(s.Name))
	}
	for _, s := range a.Secrets {
		fmt.Fprintf(b, "  %-*s  ", nameW, s.Name)
		if s.Infra != "" {
			b.WriteString(muted.Render("credencial de " + s.Infra))
		} else {
			p.mark(b, "secret-del:"+s.Name)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
	p.row(b, "secret-name")
	p.row(b, "secret-value")
	b.WriteString("\n")
	p.inline(b, "secret-save")

	b.WriteString("\n" + t.rule(w) + "\n")
	p.title(b, "Bancos da infra", "info-dbs")
	if len(a.Databases) == 0 {
		b.WriteString(muted.Render("  nenhum banco ligado") + "\n")
	}
	for _, l := range a.Databases {
		fmt.Fprintf(b, "  %-22s %s ", l.ID(), muted.Render(strings.Join(l.Secrets(), " e ")))
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
		fmt.Fprintf(b, "  %-22s %s ", l.Name, muted.Render(strings.Join(l.Secrets(), " e ")))
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

func (m *model) appsClick(msg tea.MouseMsg) tea.Cmd {
	if m.apps.info != nil {
		m.apps.info = nil // qualquer clique fecha a ajuda
		return nil
	}
	sel, before, cont := m.apps.sel, m.apps.section, m.apps.container
	cmd := m.apps.click(msg)
	force := sel != m.apps.sel || m.apps.section != before || m.apps.container != cont
	if force {
		m.logSelection = logSelection{} // a selecao era dos logs de antes
	}
	return tea.Batch(cmd, m.syncAppLogs(force), m.apps.promoRefresh(false))
}

func (m *model) appsKey(msg tea.KeyPressMsg) tea.Cmd {
	k := msg.String()
	if m.apps.info != nil {
		switch k {
		case "esc", "enter", "space", "q":
			m.apps.info = nil
		}
		return nil
	}
	if m.apps.section == secLogs && !m.apps.creating {
		switch k {
		case "pgup", "pgdown":
			m.scrollLogs(k == "pgup")
			return nil
		case "end":
			m.logsOffset = 0
			return nil
		}
	}
	sel, sec, cont := m.apps.sel, m.apps.section, m.apps.container
	cmd := m.apps.key(msg)
	force := sel != m.apps.sel || sec != m.apps.section || cont != m.apps.container
	if force {
		m.logSelection = logSelection{} // a selecao era dos logs de antes
	}
	return tea.Batch(cmd, m.syncAppLogs(force), m.apps.promoRefresh(false))
}

// scrollLogs rola os logs 10 linhas pra cima (antigos) ou pra baixo.
func (m *model) scrollLogs(up bool) {
	m.logSelection = logSelection{}
	if up {
		m.logsOffset = min(m.logsOffset+10, max(0, len(m.logArea.allLines)-m.logArea.height))
	} else {
		m.logsOffset = max(0, m.logsOffset-10)
	}
}

// syncAppLogs troca o container dos logs quando a secao Logs esta aberta.
func (m *model) syncAppLogs(force bool) tea.Cmd {
	if m.tab != tabApps || m.disconnected {
		return nil
	}
	target := ""
	if a := m.apps.selected(); a != nil && m.apps.section == secLogs && len(a.Containers) > 0 {
		target = a.Containers[min(m.apps.container, len(a.Containers)-1)].Name
	}
	if !force && target == m.logsTarget {
		return nil
	}
	m.logSelection = logSelection{}
	m.logsGeneration++
	m.logsTarget, m.logsText, m.logsError, m.logsOffset = target, "", "", 0
	if target == "" {
		return nil
	}
	return m.fetchAppLogs()
}

func (m *model) fetchAppLogs() tea.Cmd {
	a := m.apps.selected()
	if a == nil || m.logsTarget == "" {
		return nil
	}
	generation, app, target := m.logsGeneration, a.Name, m.logsTarget
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		out, err := m.api.AppLogs(ctx, app, target)
		return appLogsMsg{generation, out, err}
	}
}

func (m *model) onAppLogs(msg appLogsMsg) tea.Cmd {
	if msg.generation != m.logsGeneration || m.tab != tabApps || m.disconnected {
		return nil
	}
	m.logsText, m.logsError = msg.text, ""
	if msg.err != nil {
		m.logsError = msg.err.Error()
	} else if strings.TrimSpace(msg.text) == "" {
		m.logsText = "(o container ainda nao escreveu nada no log)"
	}
	generation := m.logsGeneration
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return appLogsTick(generation) })
}

type appLogsTick uint64

func (m *model) onAppLogsTick(g appLogsTick) tea.Cmd {
	if uint64(g) != m.logsGeneration || m.tab != tabApps || m.disconnected {
		return nil
	}
	if len(m.logSelection.lines) > 0 {
		return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return g })
	}
	return m.fetchAppLogs()
}

func (m *model) viewApps(h int) string {
	t := m.th
	if m.w < 80 {
		m.logArea = logArea{}
		return t.panel(false, m.w-2, max(1, h-2)).Render("Amplie o terminal para pelo menos 80 colunas para ver os apps.")
	}
	leftW := min(36, max(26, m.w/4))
	left := m.apps.viewList(leftW, h)
	rightW := m.w - leftW - 1
	innerH := max(1, h-2)
	var right string
	if a := m.apps.selected(); a != nil && m.apps.section == secLogs {
		right = m.zones.Mark("app-logs", m.viewAppLogs(*a, rightW, innerH))
	} else {
		m.logArea = logArea{}
		right = m.apps.viewDetail(rightW, innerH)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)
}

func (m *model) viewAppLogs(a core.AppStatus, w, h int) string {
	t := m.th
	p := m.apps
	var b strings.Builder
	mark, status, c := appStatus(a, t)
	b.WriteString(t.bold().Render(a.Name) + "  " + t.fg(c).Render(mark+" "+status) + "\n")
	b.WriteString(p.viewSections() + "\n\n")
	name := "nenhum"
	if len(a.Containers) > 0 {
		name = a.Containers[min(p.container, len(a.Containers)-1)].Name
	}
	for i, f := range p.fields {
		if f.id == "log-container" {
			b.WriteString(p.zones.Mark(f.id, "  "+t.fg(t.text).Width(18).Render("Container")+t.selector("‹ "+name+" ›", p.inForm && i == p.focus)))
			// linhas na tela durante a selecao, como no titulo dos logs da Infra
			if pos := m.logPosition(); pos != "" {
				b.WriteString("  " + t.bold().Render("Logs · "+pos))
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
	empty := ""
	if len(a.Containers) == 0 {
		empty = "Nenhum container: o app ainda nao teve deploy ou esta parado."
	}
	m.writeLogs(&b, empty, w, h)
	return t.panel(p.inForm, w-2, h).MaxHeight(h + 2).Render(b.String())
}
