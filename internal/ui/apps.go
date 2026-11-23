package ui

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/core"
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
	promos map[string]promoResult // homologacao -> resultado
	// rollbackArm e o commit do historico com o "voltar" ja apertado uma
	// vez (o segundo aperto confirma)
	rollbackArm string
	promoSig    map[string]string // homologacao -> commits no ar quando consultou

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
		rollback bool
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
	if p.creating {
		p.fieldsCreate()
		p.setFocus(p.focus)
		return
	}
	a := p.selected()
	if a == nil {
		p.setFocus(0)
		return
	}
	switch p.section {
	case secGeneral:
		p.fieldsGeneral(a)
	case secEnv:
		p.fieldsEnv()
	case secSecrets:
		p.fieldsSecrets(a)
	case secLogs:
		p.fieldsLogs(a)
	}
	p.setFocus(p.focus)
}

func (p *appsPage) add(f *field) { p.fields = append(p.fields, f) }

// btn e um botao que fica desligado enquanto uma acao roda.
func (p *appsPage) btn(id, label string, press func() tea.Cmd) *field {
	return &field{id: id, label: label, press: press, disabled: func() bool { return p.busy != "" }}
}

// infoChip e o [i] ao lado do titulo de uma parte: abre a ajuda dela.
func (p *appsPage) infoChip(id string, box infoBox) {
	p.add(&field{id: id, label: "i", chip: true, press: func() tea.Cmd { p.info = &box; return nil }})
}

// fieldsCreate sao os campos do app novo.
func (p *appsPage) fieldsCreate() {
	p.add(&field{id: "new-name", label: "Nome do app", input: p.newName})
	p.add(&field{id: "new-repo", label: "Repositorio", input: p.newRepo})
	p.add(&field{id: "new-branch", label: "Branch", input: p.newBranch})
	p.add(&field{id: "new-compose", label: "Compose", input: p.newCompose})
	p.add(&field{id: "new-auto", label: "Deploy", press: func() tea.Cmd { p.newAuto = !p.newAuto; return nil }})
	p.add(p.btn("new-create", "Criar", p.create))
	cancel := p.btn("new-cancel", "Cancelar", p.cancelCreate)
	cancel.kind = btnNeutral
	p.add(cancel)
}

// fieldsGeneral e a secao Geral: deploy, historico, promocao, repositorio e exclusao.
func (p *appsPage) fieldsGeneral(a *core.AppStatus) {
	p.infoChip("info-deploy", infoDeploy)
	p.add(&field{id: "ref", label: "Commit ou branch", input: p.ref})
	p.add(&field{id: "auto", label: "Deploy", press: p.toggleAuto, disabled: func() bool { return p.busy != "" }})
	p.add(p.btn("deploy", "Deploy", p.deploy))
	restart, stop := p.btn("restart", "Reiniciar", p.restart), p.btn("stop", "Parar", p.stop)
	restart.kind, stop.kind = btnInfo, btnWarn
	p.add(restart)
	p.add(stop)

	// historico: "voltar" nos deploys que deram certo (um por commit,
	// menos o que esta no ar)
	seen := map[string]bool{a.Commit: true}
	for _, d := range a.History {
		if !d.OK() || seen[d.Commit] {
			continue
		}
		seen[d.Commit] = true
		commit := d.Commit
		f := p.btn("rollback:"+commit, "voltar", func() tea.Cmd { return p.rollback(commit) })
		f.chip, f.kind = true, btnInfo
		if p.rollbackArm == commit {
			f.label, f.kind = "confirmar: voltar pra "+shortCommit(commit), btnWarn
		}
		p.add(f)
	}

	// promocao: na homologacao e na producao que tem homologacao
	hs := p.homologs(*a)
	if len(hs) > 0 {
		p.infoChip("info-promote", infoPromote)
		p.add(p.btn("promo-refresh", "atualizar", func() tea.Cmd { return p.promoRefresh(true) }))
		p.fields[len(p.fields)-1].chip = true
	}
	for _, h := range hs {
		from := h.Name
		r, ok := p.promos[from]
		f := p.btn("promote:"+from, "Promover", func() tea.Cmd { return p.promote(from, r.p.State == apps.PromoteOlder) })
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
		p.add(f)
	}
	if len(hs) == 0 && a.PromoteTo == "" {
		p.add(p.btn("homolog-new", "Criar homologacao", p.startHomolog))
	}
	p.infoChip("info-repo", infoRepo)
	p.add(&field{id: "repo", label: "Repositorio", input: p.repo})
	p.add(&field{id: "branch", label: "Branch", input: p.branch})
	p.add(&field{id: "compose", label: "Compose", input: p.compose})
	p.add(p.btn("app-save", "Salvar", p.saveApp))
	p.infoChip("info-delete", infoBox{"Excluir app", "Apaga containers, imagens, codigo, variaveis e secrets do app. " +
		"O database e os buckets da infra ficam (aba Infra).\n\n" +
		"Digite " + a.Name + " no Confirmar pra liberar o botao. Os volumes (dados) so sao apagados se voce trocar " +
		"Volumes pra APAGAR; nao tem volta."})
	p.add(&field{id: "delete-confirm", label: "Confirmar", input: p.delConfirm})
	p.add(p.btn("delete-volumes", "", func() tea.Cmd { p.delVolumes = !p.delVolumes; return nil }))
	name := a.Name
	p.add(&field{id: "delete", label: "Excluir app", kind: btnDanger, press: p.deleteApp, disabled: func() bool {
		return p.busy != "" || strings.TrimSpace(p.delConfirm.Value()) != name
	}})
}

// fieldsEnv e a secao Variaveis.
func (p *appsPage) fieldsEnv() {
	for i, r := range p.envRows {
		p.add(&field{id: fmt.Sprintf("env-name:%d", i), input: r.name})
		p.add(&field{id: fmt.Sprintf("env-value:%d", i), input: r.value})
		rm := p.btn(fmt.Sprintf("env-del:%d", i), "✕ remover", func() tea.Cmd { p.envRows = del(p.envRows, i); p.rebuild(); return nil })
		rm.kind, rm.chip = btnDanger, true
		p.add(rm)
	}
	p.add(p.btn("env-add", "+ Nova variavel", func() tea.Cmd {
		p.envRows = append(p.envRows, p.newEnvRow("", ""))
		p.rebuild()
		return p.setFocus(len(p.envRows)*3 - 3) // o nome da nova
	}))
	p.add(p.btn("env-import", "Importar .env", func() tea.Cmd { p.importing = true; p.importText, p.importErr = "", ""; p.rebuild(); return nil }))
	p.add(p.btn("env-save", "Salvar", p.saveEnv))
	if p.importing {
		p.add(&field{id: "env-import-apply", label: "Aplicar", press: p.applyImport, disabled: func() bool { return p.importText == "" }})
		cancel := p.btn("env-import-cancel", "Cancelar", func() tea.Cmd { p.closeImport(); p.rebuild(); return nil })
		cancel.kind = btnNeutral
		p.add(cancel)
	}
}

// fieldsSecrets e a secao Secrets: secrets, bancos e buckets ligados.
func (p *appsPage) fieldsSecrets(a *core.AppStatus) {
	p.infoChip("info-secrets", infoSecrets)
	for _, s := range a.Secrets {
		if s.Infra != "" {
			continue // credencial de banco ou bucket: desliga por ele
		}
		name := s.Name
		f := p.btn("secret-del:"+name, "✕ apagar", func() tea.Cmd { return p.deleteSecret(name) })
		f.kind, f.chip = btnDanger, true
		p.add(f)
	}
	p.add(&field{id: "secret-name", label: "Secret", input: p.secretName})
	p.add(&field{id: "secret-value", label: "Valor", input: p.secretValue})
	p.add(p.btn("secret-save", "Gravar", p.saveSecret))

	p.infoChip("info-dbs", infoDatabases)
	for _, l := range a.Databases {
		l := l
		f := p.btn("db-unlink:"+l.ID(), "desligar", func() tea.Cmd { return p.unlinkDB(l) })
		f.kind, f.chip = btnDanger, true
		p.add(f)
	}
	p.add(&field{id: "db-pick", label: "Banco", press: func() tea.Cmd {
		if n := len(p.linkable()); n > 0 {
			p.dbPick = (p.dbPick + 1) % n
		}
		return nil
	}})
	p.add(p.btn("db-link", "Ligar banco", p.linkDB))

	p.infoChip("info-buckets", infoBuckets)
	for _, l := range a.Buckets {
		l := l
		f := p.btn("bucket-unlink:"+l.Name, "desligar", func() tea.Cmd { return p.unlinkBucket(l) })
		f.kind, f.chip = btnDanger, true
		p.add(f)
	}
	p.add(&field{id: "bucket-pick", label: "Bucket", press: func() tea.Cmd {
		if n := len(p.linkableBuckets()); n > 0 {
			p.bucketPick = (p.bucketPick + 1) % n
		}
		return nil
	}})
	p.add(p.btn("bucket-link", "Ligar bucket", p.linkBucket))
}

// fieldsLogs e a secao Logs.
func (p *appsPage) fieldsLogs(a *core.AppStatus) {
	p.add(&field{id: "log-container", label: "Container", press: func() tea.Cmd {
		if n := len(a.Containers); n > 0 {
			p.container = (p.container + 1) % n
		}
		return nil
	}})
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
