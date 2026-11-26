package ui

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/trdSystemDev/cais/internal/backup"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/databases"
)

// bkPanel e o painel de Backups da aba Setup: o destino (Google Drive ou
// OneDrive), o client dele, o login, a chave do backup, o horario, o backup
// agora e o proximo e o ultimo backup. Ativo (recebe o teclado) quando o Tab passa do formulario
// pra ele ou num clique.
type bkPanel struct {
	api   core.API
	zones *zone.Manager
	th    theme

	active bool
	focus  int
	at     *textinput.Model
	days   *textinput.Model
	// filled: horario e retencao ja vieram do estado (nao sobrescreve o
	// que a pessoa esta digitando)
	filled bool

	busy string // acao em andamento
	err  string
	done string
	// priv e a chave privada recem gerada: aparece ate a pessoa confirmar
	// que guardou, e nunca mais
	priv       string
	confirmKey bool // "Gerar outra" pede confirmacao
	// keyMade: a chave acabou de ser gerada aqui (o estado com a publica
	// pode ainda nao ter chegado; sem isso um clique rapido geraria outra
	// sem confirmar)
	keyMade bool
	info    *infoBox // ajuda aberta pelo [i] (modal)
}

type bkDoneMsg struct {
	action string
	err    error
	priv   string
	run    core.BackupRun
}

func newBkPanel(api core.API, zones *zone.Manager, th theme) *bkPanel {
	p := &bkPanel{api: api, zones: zones}
	mk := func(placeholder string, w int) *textinput.Model {
		ti := textinput.New()
		ti.Prompt = ""
		ti.Placeholder = placeholder
		ti.SetWidth(w)
		return &ti
	}
	p.at = mk("03:00", 5)
	p.days = mk("30", 4)
	p.setTheme(th)
	return p
}

func (p *bkPanel) inputs() []*textinput.Model {
	return []*textinput.Model{p.at, p.days}
}

func (p *bkPanel) setTheme(th theme) {
	p.th = th
	for _, ti := range p.inputs() {
		th.styleInput(ti)
	}
}

// ids na ordem do Tab, conforme o que esta na tela
func (p *bkPanel) ids(st core.BackupStatus) []string {
	ids := []string{"bk-info", "bk-provider", "bk-info-login"}
	switch {
	case st.Login != nil:
		ids = append(ids, "bk-login-copy", "bk-login-cancel")
	case st.Email != "":
		ids = append(ids, "bk-disconnect")
	default:
		ids = append(ids, "bk-connect")
	}
	ids = append(ids, "bk-info-key")
	if p.priv != "" {
		ids = append(ids, "bk-key-copy", "bk-key-done")
	} else {
		ids = append(ids, "bk-key")
	}
	return append(ids, "bk-info-schedule", "bk-time", "bk-days", "bk-schedule", "bk-info-run", "bk-run")
}

func (p *bkPanel) input(id string) *textinput.Model {
	switch id {
	case "bk-time":
		return p.at
	case "bk-days":
		return p.days
	}
	return nil
}

func (p *bkPanel) focusID(st core.BackupStatus) string {
	ids := p.ids(st)
	return ids[min(p.focus, len(ids)-1)]
}

func (p *bkPanel) typing(st core.BackupStatus) bool {
	return p.active && p.input(p.focusID(st)) != nil
}

func (p *bkPanel) setFocus(st core.BackupStatus, i int) tea.Cmd {
	ids := p.ids(st)
	p.focus = ((i % len(ids)) + len(ids)) % len(ids)
	var cmd tea.Cmd
	for j, id := range ids {
		if ti := p.input(id); ti != nil {
			if j == p.focus && p.active {
				cmd = ti.Focus()
			} else {
				ti.Blur()
			}
		}
	}
	return cmd
}

func (p *bkPanel) deactivate() {
	p.active, p.confirmKey = false, false
	for _, ti := range p.inputs() {
		ti.Blur()
	}
}

// fill poe horario e retencao do estado nos campos (uma vez).
func (p *bkPanel) fill(st core.BackupStatus) {
	if p.filled {
		return
	}
	p.filled = true
	p.at.SetValue(st.Time)
	if st.RetentionDays > 0 {
		p.days.SetValue(strconv.Itoa(st.RetentionDays))
	}
}

func (p *bkPanel) run(action string, fn func(ctx context.Context) bkDoneMsg) tea.Cmd {
	if p.busy != "" {
		return nil
	}
	p.busy, p.err, p.done = action, "", ""
	return func() tea.Msg {
		msg := fn(context.Background())
		msg.action = action
		return msg
	}
}

func (p *bkPanel) press(st core.BackupStatus, id string) tea.Cmd {
	if id != "bk-key" {
		p.confirmKey = false
	}
	if strings.HasPrefix(id, "bk-info") {
		box := bkInfo(st, id)
		p.info = &box
		return nil
	}
	switch id {
	case "bk-provider":
		next := backup.ProviderOneDrive
		if st.Provider == backup.ProviderOneDrive {
			next = backup.ProviderGoogle
		}
		return p.run("provider", func(ctx context.Context) bkDoneMsg {
			return bkDoneMsg{err: p.api.SetBackupProvider(ctx, next)}
		})
	case "bk-connect":
		return p.run("login", func(ctx context.Context) bkDoneMsg {
			_, err := p.api.StartDriveLogin(ctx)
			return bkDoneMsg{err: err}
		})
	case "bk-login-copy":
		if st.Login == nil {
			return nil
		}
		p.done = "Codigo copiado (o terminal precisa aceitar OSC 52; confira colando)."
		return tea.SetClipboard(st.Login.Code)
	case "bk-login-cancel":
		return p.run("cancel", func(ctx context.Context) bkDoneMsg { return bkDoneMsg{err: p.api.CancelDriveLogin(ctx)} })
	case "bk-disconnect":
		return p.run("logout", func(ctx context.Context) bkDoneMsg { return bkDoneMsg{err: p.api.DisconnectDrive(ctx)} })
	case "bk-key":
		if (st.Recipient != "" || p.keyMade) && !p.confirmKey {
			p.confirmKey = true
			return nil
		}
		p.confirmKey = false
		return p.run("key", func(ctx context.Context) bkDoneMsg {
			priv, err := p.api.NewBackupKey(ctx)
			return bkDoneMsg{err: err, priv: priv}
		})
	case "bk-key-copy":
		p.done = "Chave copiada (o terminal precisa aceitar OSC 52; confira colando)."
		return tea.SetClipboard(p.priv)
	case "bk-key-done":
		p.priv, p.done = "", "A chave privada nao aparece mais. Sem ela nenhum backup abre."
		return p.setFocus(st, slices.Index(p.ids(st), "bk-time"))
	case "bk-schedule":
		at := strings.TrimSpace(p.at.Value())
		days := 0
		if v := strings.TrimSpace(p.days.Value()); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				p.err = "Retencao: numero de dias (ex: 30)."
				return nil
			}
			days = n
		}
		return p.run("schedule", func(ctx context.Context) bkDoneMsg {
			return bkDoneMsg{err: p.api.SetBackupSchedule(ctx, at, days)}
		})
	case "bk-run":
		if !st.Ready() {
			p.err = "Complete os passos de cima antes (conectar e a chave)."
			return nil
		}
		return p.run("backup", func(ctx context.Context) bkDoneMsg {
			run, err := p.api.RunBackup(ctx)
			return bkDoneMsg{err: err, run: run}
		})
	}
	return nil
}

func (p *bkPanel) update(st core.BackupStatus, msg tea.Msg) (tea.Cmd, bool) {
	d, ok := msg.(bkDoneMsg)
	if !ok {
		return nil, false
	}
	p.busy = ""
	if d.err != nil {
		p.err = firstLine(d.err.Error())
		return nil, true
	}
	switch d.action {
	case "logout":
		p.done = backup.ProviderName(st.Provider) + " desconectado (os backups que ja estao la ficam)."
	case "key":
		p.priv, p.keyMade = d.priv, true
		return p.setFocus(st, slices.Index(p.ids(st), "bk-key-copy")), true
	case "schedule":
		p.done = "Horario gravado."
		if strings.TrimSpace(p.at.Value()) == "" {
			p.done = "Backup automatico desligado (o manual continua)."
		}
	case "backup":
		p.done = fmt.Sprintf("Backup %s no %s (%s).", d.run.File, backup.ProviderName(d.run.Provider), databases.HumanSize(d.run.Size))
	}
	return nil, true
}

func (p *bkPanel) key(st core.BackupStatus, msg tea.KeyPressMsg) tea.Cmd {
	id := p.focusID(st)
	switch k := msg.String(); k {
	case "esc":
		p.deactivate()
		return nil
	case "tab", "down":
		return p.setFocus(st, p.focus+1)
	case "shift+tab", "up":
		return p.setFocus(st, p.focus-1)
	case "enter":
		if p.input(id) != nil {
			next := map[string]string{"bk-time": "bk-days", "bk-days": "bk-schedule"}[id]
			return p.setFocus(st, slices.Index(p.ids(st), next))
		}
		return p.press(st, id)
	case "space":
		if p.input(id) == nil {
			return p.press(st, id)
		}
	case "left", "right":
		if id == "bk-provider" {
			return p.press(st, id)
		}
	}
	if ti := p.input(id); ti != nil {
		v, cmd := ti.Update(msg)
		*ti = v
		return cmd
	}
	return nil
}

func (p *bkPanel) paste(st core.BackupStatus, msg tea.PasteMsg) tea.Cmd {
	if ti := p.input(p.focusID(st)); ti != nil && p.active {
		v, cmd := ti.Update(msg)
		*ti = v
		return cmd
	}
	return nil
}

// click no painel: ativa e foca o que foi clicado.
func (p *bkPanel) click(st core.BackupStatus, msg tea.MouseMsg) (tea.Cmd, bool) {
	for i, id := range p.ids(st) {
		if p.zones.Get(id).InBounds(msg) {
			if !p.active {
				p.active = true
				p.fill(st)
			}
			cmd := p.setFocus(st, i)
			if p.input(id) == nil {
				return tea.Batch(cmd, p.press(st, id)), true
			}
			return cmd, true
		}
	}
	return nil, false
}

func (p *bkPanel) view(st core.BackupStatus, w, h int, head string) string {
	v := p.newView(st, w)
	var b strings.Builder
	v.viewHeader(&b, head)
	for _, part := range []func(*strings.Builder){v.viewLogin, v.viewKey, v.viewSchedule, v.viewRun} {
		part(&b)
	}
	return p.th.panel(p.active, w, h).MaxHeight(h + 2).Render(b.String())
}

// bkView e o que as partes do painel de backup usam pra se desenhar.
type bkView struct {
	p           *bkPanel
	t           theme
	st          core.BackupStatus
	w           int
	muted, wrap lipgloss.Style
	focusID     string
	name        string // o destino (Google Drive ou OneDrive)
	onedrive    bool
	rule        string
}

func (p *bkPanel) newView(st core.BackupStatus, w int) *bkView {
	t := p.th
	v := &bkView{p: p, t: t, st: st, w: w, muted: t.fg(t.muted), wrap: lipgloss.NewStyle().Width(w - 6),
		name: backup.ProviderName(st.Provider), onedrive: st.Provider == backup.ProviderOneDrive, rule: "\n" + t.rule(w-6) + "\n"}
	if p.active {
		v.focusID = p.focusID(st)
	}
	return v
}

func (v *bkView) field(id, label string, width int) string {
	t := v.t
	marker, ls := "  ", t.fg(t.text).Width(16)
	if id == v.focusID {
		marker, ls = t.fg(t.accent).Bold(true).Render("› "), t.fg(t.accent).Bold(true).Width(16)
	}
	return v.p.zones.Mark(id, marker+ls.Render(label)+t.input(v.p.input(id), width))
}

func (v *bkView) btn(id, label string, k btnKind) string {
	if v.p.busy != "" {
		k = btnOff
	}
	return v.p.zones.Mark(id, v.t.button(label, k, id == v.focusID))
}

func (v *bkView) info(id string) string {
	return v.p.zones.Mark(id, v.t.chip("i", btnInfo, id == v.focusID))
}

// step e o titulo de um passo: feito ou nao, o [i] da ajuda e o resumo
func (v *bkView) step(n int, title string, ok bool, detail, infoID string) string {
	t := v.t
	mark := t.fg(t.danger).Render("✗")
	if ok {
		mark = t.fg(t.ok).Render("✓")
	}
	return mark + " " + t.bold().Render(fmt.Sprintf("%d · %s", n, title)) + " " + v.info(infoID) + "  " +
		v.muted.Render(ansi.Truncate(detail, max(1, v.w-16-len(title)), "…"))
}

// viewHeader desenha o titulo com o destino e o resultado da ultima acao.
func (v *bkView) viewHeader(b *strings.Builder, head string) {
	p, t, wrap, focusID, name, rule := v.p, v.t, v.wrap, v.focusID, v.name, v.rule
	b.WriteString(head + "\n\n")
	// destino no titulo, como seletor: trocar nao apaga o login do outro
	dest := t.fg(t.muted).Render("Destino ")
	if focusID == "bk-provider" {
		dest = t.fg(t.accent).Bold(true).Render("› Destino ")
	}
	b.WriteString(t.bold().Render("Backups") + " " + v.info("bk-info") + "    " +
		p.zones.Mark("bk-provider", dest+t.selector("‹ "+name+" ›", focusID == "bk-provider")) + "\n")
	// o resultado da ultima acao fica no topo: no fim do painel ele some
	// quando o terminal e baixo
	switch {
	case p.err != "":
		b.WriteString(wrap.Foreground(t.danger).Render(p.err) + "\n")
	case p.done != "":
		b.WriteString(wrap.Foreground(t.ok).Render(p.done) + "\n")
	}
	b.WriteString(rule)
}

// viewLogin desenha o passo 1: o login no destino.
func (v *bkView) viewLogin(b *strings.Builder) {
	t, st, muted, wrap, name, rule := v.t, v.st, v.muted, v.wrap, v.name, v.rule
	// 1. login no destino
	switch {
	case st.Login != nil:
		b.WriteString(v.step(1, name, false, "aguardando o login", "bk-info-login") + "\n")
		// o link e clicavel (OSC 8) no terminal que aceita; o codigo vai
		// pelo Copiar pra colar na pagina
		b.WriteString("  Abra " + t.fg(t.accent).Bold(true).Underline(true).Hyperlink(st.Login.URL).Render(st.Login.URL) +
			" no navegador (qualquer aparelho) e digite:\n")
		b.WriteString("    " + t.fg(t.accent).Bold(true).Render(st.Login.Code) + "  " + muted.Render("(expira as "+st.Login.Expires.Local().Format("15:04")+")") + "\n")
		b.WriteString("  " + v.btn("bk-login-copy", "Copiar codigo", btnInfo) + " " + v.btn("bk-login-cancel", "Cancelar", btnNeutral) + "\n")
	case st.Email != "":
		b.WriteString(v.step(1, name, true, st.Email, "bk-info-login") + "\n")
		b.WriteString("  " + v.btn("bk-disconnect", "Desconectar", btnWarn) + "\n")
	default:
		b.WriteString(v.step(1, name, false, "nao conectado", "bk-info-login") + "\n")
		b.WriteString("  " + v.btn("bk-connect", "Conectar", btnNormal) + "\n")
	}
	if st.LoginError != "" {
		b.WriteString(wrap.Foreground(t.danger).Render(st.LoginError) + "\n")
	}
	b.WriteString(rule)
}

// viewKey desenha o passo 2: a chave do backup.
func (v *bkView) viewKey(b *strings.Builder) {
	p, t, st, w, muted, wrap, rule := v.p, v.t, v.st, v.w, v.muted, v.wrap, v.rule
	// 2. chave
	short := st.Recipient
	if len(short) > 20 {
		short = short[:12] + "…" + short[len(short)-6:]
	}
	b.WriteString(v.step(2, "Chave do backup", st.Recipient != "", cmpOrStr(short, "falta"), "bk-info-key") + "\n")
	if p.priv != "" {
		b.WriteString(wrap.Foreground(t.warn).Bold(true).Render("Guarde esta chave privada agora (gerenciador de senhas). Ela NAO fica na VPS: sem ela nenhum backup abre.") + "\n")
		// a chave e uma linha so: se o painel e estreito ela quebra, e o
		// Copiar (inteira, sem a quebra) e o jeito certo
		b.WriteString(t.fg(t.accent).Render(p.priv) + "\n")
		if lipgloss.Width(p.priv) > w-6 {
			b.WriteString(muted.Render("A chave e uma linha so: use Copiar (selecionar com o mouse leva a quebra junto).") + "\n")
		}
		b.WriteString("  " + v.btn("bk-key-copy", "Copiar", btnInfo) + " " + v.btn("bk-key-done", "Ja guardei", btnNormal) + "\n")
	} else {
		label, k := "Gerar chave", btnNormal
		if st.Recipient != "" || p.keyMade {
			label, k = "Gerar outra", btnNeutral
		}
		if p.confirmKey {
			label, k = "Confirmar: gerar outra", btnDanger
		}
		b.WriteString("  " + v.btn("bk-key", label, k) + "\n")
		if p.confirmKey {
			b.WriteString(wrap.Foreground(t.warn).Render("Os backups ja feitos continuam abrindo so com a chave privada antiga.") + "\n")
		}
	}
	b.WriteString(rule)
}

// viewSchedule desenha o passo 3: o horario e a retencao.
func (v *bkView) viewSchedule(b *strings.Builder) {
	st, rule := v.st, v.rule
	// 3. horario
	when := "desligado (so manual)"
	if st.Time != "" {
		when = fmt.Sprintf("todo dia as %s, guarda %d dias", st.Time, st.RetentionDays)
	}
	b.WriteString(v.step(3, "Horario", st.Time != "", when, "bk-info-schedule") + "\n")
	b.WriteString(v.field("bk-time", "Horario (HH:MM)", 6) + "\n")
	b.WriteString(v.field("bk-days", "Retencao (dias)", 6) + "\n")
	b.WriteString("\n")
	b.WriteString("  " + v.btn("bk-schedule", "Salvar horario", btnNormal) + "\n" + rule)
}

// viewRun desenha o backup agora: o ultimo, o proximo e o botao.
func (v *bkView) viewRun(b *strings.Builder) {
	t, st, w, muted, wrap, name := v.t, v.st, v.w, v.muted, v.wrap, v.name
	// backup agora: o ultimo no titulo (ou a falha dele embaixo) e o
	// proximo ao lado do botao, pra caber em terminal baixo
	title := t.bold().Render("Backup agora") + " " + v.info("bk-info-run")
	failed := ""
	if l := st.Last; l != nil {
		when := l.At.Local().Format("02/01 15:04")
		if l.OK() {
			last := fmt.Sprintf("Ultimo: %s · %s · %d databases", when, databases.HumanSize(l.Size), l.Databases)
			title += "  " + t.fg(t.ok).Render(ansi.Truncate(last, max(1, w-26), "…"))
		} else {
			failed = "Ultimo (" + when + ") falhou: " + cmpOrStr(l.Error, strings.Join(l.Errors, "; "))
		}
	}
	b.WriteString(title + "\n")
	label, k := "Fazer backup agora", btnNormal
	if !st.Ready() {
		k = btnOff
	}
	if st.Running {
		label, k = "Fazendo backup...", btnOff
	}
	b.WriteString("  " + v.btn("bk-run", label, k))
	switch {
	case st.Running && st.Step != "":
		b.WriteString("  " + t.fg(t.warn).Render(st.Step))
	case !st.Next.IsZero():
		next := "Proximo: " + st.Next.Local().Format("02/01 15:04")
		if st.Retry {
			next += " (nova tentativa: o ultimo nao chegou no " + name + ")"
		}
		b.WriteString("  " + muted.Render(next))
	}
	b.WriteString("\n")
	if failed != "" {
		b.WriteString(wrap.Foreground(t.danger).PaddingLeft(2).Render(failed) + "\n")
	}
}

// bkInfo e a ajuda de cada [i] do painel (o texto do client depende do destino).
func bkInfo(st core.BackupStatus, id string) infoBox {
	name := backup.ProviderName(st.Provider)
	switch id {
	case "bk-info-login":
		return infoBox{"Conectar o " + name, "Conectar mostra um link e um codigo: abra o link em qualquer aparelho (celular, notebook), " +
			"entre na conta que vai guardar os backups e digite o codigo (Copiar codigo poe ele na area de transferencia). O cais guarda so o acesso a pasta dele. " +
			"Desconectar tira o acesso, mas os backups que ja estao no " + name + " ficam." + loginNote(st)}
	case "bk-info-key":
		return infoBox{"Chave do backup", "Cada backup e criptografado antes de sair da VPS. A chave publica fica no cais; a privada " +
			"aparece uma vez so, pra voce guardar num gerenciador de senhas. Ela NAO fica na VPS: sem ela nenhum backup abre. " +
			"Gerar outra vale pros proximos backups; os ja feitos continuam abrindo so com a privada antiga."}
	case "bk-info-schedule":
		return infoBox{"Horario", "O backup automatico roda todo dia nesse horario (hora da VPS). Vazio desliga, e fica so o " +
			"manual. Retencao: depois de cada backup, os arquivos mais velhos que esse numero de dias sao apagados do " + name + "."}
	case "bk-info-run":
		return infoBox{"Backup agora", "Faz um backup na hora, sem esperar o horario (precisa dos passos de cima, menos o horario). " +
			"Se o envio falhar, o cais tenta de novo e mostra a nova tentativa em Proximo.\n\n" +
			"Restaurar (na VPS): cais backup files lista os arquivos; cais backup restore <arquivo> -key <arquivo> postgres/<db> " +
			"volta um banco."}
	}
	return infoBox{"Backups", "Os bancos da infra e o estado do cais, num arquivo criptografado por dia, no destino escolhido. " +
		"Destino: ←→ troca entre Google Drive e OneDrive; o login de cada um fica guardado.\n\n" + core.BackupGap}
}

// loginNote explica que o app e o do cais (sem registrar nada) e onde
// ficam os backups em cada destino.
func loginNote(st core.BackupStatus) string {
	if st.Provider == backup.ProviderOneDrive {
		return "\n\nO cais ja vem com o app dele na Microsoft: nao precisa registrar nada, so entrar com a conta pessoal " +
			"(outlook.com, hotmail.com, live.com). Os backups ficam em Aplicativos/cais; o resto do OneDrive o cais nao ve."
	}
	return "\n\nO cais ja vem com o app dele no Google: nao precisa criar nada, so entrar com a conta. Os backups ficam " +
		"na pasta cais-backups-<nome da VPS> do seu Drive; o cais so ve os arquivos que ele mesmo criou."
}

// backupRefreshCmd pede ao core o estado do backup (chega pelo estado).
func (m *model) backupRefreshCmd() tea.Cmd {
	return func() tea.Msg {
		m.api.RefreshBackup(context.Background())
		return nil
	}
}
