package ui

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/databases"
)

// backupCard e a entrada "Backups" no fim da lista da aba Infra.
const backupCard = "backups"

// bkPanel e o painel de Backups da aba Infra: OAuth client do Google,
// login no Drive, chave do backup, horario e o backup manual. Ativo
// (recebe o teclado) depois de enter ou clique no card.
type bkPanel struct {
	api   core.API
	zones *zone.Manager
	th    theme

	active   bool
	focus    int
	clientID *textinput.Model
	secret   *textinput.Model
	at       *textinput.Model
	days     *textinput.Model
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
	p.clientID = mk("....apps.googleusercontent.com", 40)
	p.secret = mk("client secret", 40)
	p.secret.EchoMode = textinput.EchoPassword
	p.at = mk("03:00", 5)
	p.days = mk("30", 4)
	p.setTheme(th)
	return p
}

func (p *bkPanel) inputs() []*textinput.Model {
	return []*textinput.Model{p.clientID, p.secret, p.at, p.days}
}

func (p *bkPanel) setTheme(th theme) {
	p.th = th
	for _, ti := range p.inputs() {
		th.styleInput(ti)
	}
}

// ids na ordem do Tab, conforme o que esta na tela
func (p *bkPanel) ids(st core.BackupStatus) []string {
	ids := []string{"bk-client-id", "bk-client-secret", "bk-client-save"}
	switch {
	case st.Login != nil:
		ids = append(ids, "bk-login-cancel")
	case st.Email != "":
		ids = append(ids, "bk-disconnect")
	default:
		ids = append(ids, "bk-connect")
	}
	if p.priv != "" {
		ids = append(ids, "bk-key-copy", "bk-key-done")
	} else {
		ids = append(ids, "bk-key")
	}
	return append(ids, "bk-time", "bk-days", "bk-schedule", "bk-run")
}

func (p *bkPanel) input(id string) *textinput.Model {
	switch id {
	case "bk-client-id":
		return p.clientID
	case "bk-client-secret":
		return p.secret
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

// activate passa o teclado pro painel, no primeiro passo que falta.
func (p *bkPanel) activate(st core.BackupStatus) tea.Cmd {
	p.active = true
	p.fill(st)
	first := "bk-run"
	switch {
	case st.ClientID == "":
		first = "bk-client-id"
	case st.Email == "" && st.Login == nil:
		first = "bk-connect"
	case st.Recipient == "":
		first = "bk-key"
	case st.Time == "":
		first = "bk-time"
	}
	return p.setFocus(st, max(0, slices.Index(p.ids(st), first)))
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
	switch id {
	case "bk-client-save":
		cid, secret := strings.TrimSpace(p.clientID.Value()), strings.TrimSpace(p.secret.Value())
		if cid == "" || secret == "" {
			p.err = "Informe o client ID e o client secret."
			return nil
		}
		return p.run("client", func(ctx context.Context) bkDoneMsg {
			return bkDoneMsg{err: p.api.SetBackupClient(ctx, cid, secret)}
		})
	case "bk-connect":
		return p.run("login", func(ctx context.Context) bkDoneMsg {
			_, err := p.api.StartDriveLogin(ctx)
			return bkDoneMsg{err: err}
		})
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
			p.err = "Complete os passos 1 a 3 antes."
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
		if d.action != "backup" {
			return nil, true
		}
	}
	switch d.action {
	case "client":
		p.secret.SetValue("")
		p.done = "OAuth client gravado. Agora conecte o Drive."
	case "logout":
		p.done = "Drive desconectado (os backups que ja estao la ficam)."
	case "key":
		p.priv, p.keyMade = d.priv, true
		return p.setFocus(st, slices.Index(p.ids(st), "bk-key-copy")), true
	case "schedule":
		p.done = "Horario gravado."
		if strings.TrimSpace(p.at.Value()) == "" {
			p.done = "Backup automatico desligado (o manual continua)."
		}
	case "backup":
		if d.err == nil {
			p.done = fmt.Sprintf("Backup %s no Drive (%s).", d.run.File, databases.HumanSize(d.run.Size))
		}
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
			next := map[string]string{"bk-client-id": "bk-client-secret", "bk-client-secret": "bk-client-save",
				"bk-time": "bk-days", "bk-days": "bk-schedule"}[id]
			return p.setFocus(st, slices.Index(p.ids(st), next))
		}
		return p.press(st, id)
	case "space":
		if p.input(id) == nil {
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

func (p *bkPanel) view(st core.BackupStatus, w, h int) string {
	t := p.th
	muted := t.fg(t.muted)
	wrap := lipgloss.NewStyle().Width(w - 6)
	focusID := ""
	if p.active {
		focusID = p.focusID(st)
	}
	field := func(id, label string, width int) string {
		marker, ls := "  ", t.fg(t.text).Width(16)
		if id == focusID {
			marker, ls = t.fg(t.accent).Bold(true).Render("› "), t.fg(t.accent).Bold(true).Width(16)
		}
		return p.zones.Mark(id, marker+ls.Render(label)+t.input(p.input(id), width))
	}
	btn := func(id, label string, k btnKind) string {
		if p.busy != "" {
			k = btnOff
		}
		return p.zones.Mark(id, t.button(label, k, id == focusID))
	}
	step := func(n int, title string, ok bool, detail string) string {
		mark := t.fg(t.danger).Render("✗")
		if ok {
			mark = t.fg(t.ok).Render("✓")
		}
		return mark + " " + t.bold().Render(fmt.Sprintf("%d · %s", n, title)) + "  " + muted.Render(ansi.Truncate(detail, max(1, w-12-len(title)), "…"))
	}

	var b strings.Builder
	b.WriteString(t.bold().Render("Backups") + "  " + muted.Render("bancos e estado do cais, criptografados, no Google Drive") + "\n")
	if !p.active {
		b.WriteString(muted.Render("enter ou clique pra configurar · esc sai") + "\n")
	}
	b.WriteString("\n")

	// 1. OAuth client
	b.WriteString(step(1, "OAuth client do Google", st.ClientID != "", cmpOrStr(st.ClientID, "falta")) + "\n")
	if st.ClientID == "" {
		b.WriteString(wrap.Inherit(muted).Render("No Google Cloud: ative a Google Drive API, crie em Credenciais um ID do cliente OAuth "+
			"do tipo \"TVs e dispositivos de entrada limitada\" e publique a tela de consentimento (em teste o login expira em 7 dias).") + "\n")
	}
	b.WriteString(field("bk-client-id", "Client ID", 41) + "\n")
	b.WriteString(field("bk-client-secret", "Client secret", 41) + "\n")
	b.WriteString("  " + btn("bk-client-save", "Gravar client", btnNormal) + "\n\n")

	// 2. Drive
	switch {
	case st.Login != nil:
		b.WriteString(step(2, "Google Drive", false, "aguardando o login") + "\n")
		b.WriteString("  Abra " + t.fg(t.accent).Bold(true).Render(st.Login.URL) + " em qualquer aparelho e digite:\n")
		b.WriteString("    " + t.fg(t.accent).Bold(true).Render(st.Login.Code) + "  " + muted.Render("(expira as "+st.Login.Expires.Local().Format("15:04")+")") + "\n")
		b.WriteString("  " + btn("bk-login-cancel", "Cancelar", btnNeutral) + "\n\n")
	case st.Email != "":
		b.WriteString(step(2, "Google Drive", true, st.Email) + "\n")
		b.WriteString("  " + btn("bk-disconnect", "Desconectar", btnWarn) + "\n\n")
	default:
		b.WriteString(step(2, "Google Drive", false, "nao conectado") + "\n")
		k := btnNormal
		if st.ClientID == "" {
			k = btnOff
		}
		b.WriteString("  " + btn("bk-connect", "Conectar", k) + "\n\n")
	}
	if st.LoginError != "" {
		b.WriteString(wrap.Foreground(t.danger).Render(st.LoginError) + "\n\n")
	}

	// 3. chave
	short := st.Recipient
	if len(short) > 20 {
		short = short[:12] + "…" + short[len(short)-6:]
	}
	b.WriteString(step(3, "Chave do backup", st.Recipient != "", cmpOrStr(short, "falta")) + "\n")
	if p.priv != "" {
		b.WriteString(wrap.Foreground(t.warn).Bold(true).Render("Guarde esta chave privada agora (gerenciador de senhas). Ela NAO fica na VPS: sem ela nenhum backup abre.") + "\n")
		// a chave e uma linha so: se o painel e estreito ela quebra, e o
		// Copiar (inteira, sem a quebra) e o jeito certo
		b.WriteString(t.fg(t.accent).Render(p.priv) + "\n")
		if lipgloss.Width(p.priv) > w-6 {
			b.WriteString(muted.Render("A chave e uma linha so: use Copiar (selecionar com o mouse leva a quebra junto).") + "\n")
		}
		b.WriteString("  " + btn("bk-key-copy", "Copiar", btnInfo) + " " + btn("bk-key-done", "Ja guardei", btnNormal) + "\n\n")
	} else {
		label, k := "Gerar chave", btnNormal
		if st.Recipient != "" || p.keyMade {
			label, k = "Gerar outra", btnNeutral
		}
		if p.confirmKey {
			label, k = "Confirmar: gerar outra", btnDanger
		}
		b.WriteString("  " + btn("bk-key", label, k) + "\n")
		if p.confirmKey {
			b.WriteString(wrap.Foreground(t.warn).Render("Os backups ja feitos continuam abrindo so com a chave privada antiga.") + "\n")
		}
		b.WriteString("\n")
	}

	// 4. horario
	when := "desligado (so manual)"
	if st.Time != "" {
		when = fmt.Sprintf("todo dia as %s, guarda %d dias", st.Time, st.RetentionDays)
	}
	b.WriteString(step(4, "Horario", st.Time != "", when) + "\n")
	b.WriteString(field("bk-time", "Horario (HH:MM)", 6) + "  " + muted.Render("vazio desliga") + "\n")
	b.WriteString(field("bk-days", "Retencao (dias)", 6) + "\n")
	b.WriteString("  " + btn("bk-schedule", "Salvar horario", btnNormal) + "\n\n")

	// backup agora e o ultimo
	label, k := "Fazer backup agora", btnNormal
	if !st.Ready() {
		k = btnOff
	}
	if st.Running {
		label, k = "Fazendo backup...", btnOff
	}
	b.WriteString(btn("bk-run", label, k))
	if st.Running && st.Step != "" {
		b.WriteString("  " + t.fg(t.warn).Render(st.Step))
	}
	b.WriteString("\n")
	if !st.Next.IsZero() {
		next := "Proximo: " + st.Next.Local().Format("02/01 15:04")
		if st.Retry {
			next += " (nova tentativa: o ultimo nao chegou no Drive)"
		}
		b.WriteString(muted.Render(next) + "\n")
	}
	if l := st.Last; l != nil {
		color, text := t.ok, fmt.Sprintf("Ultimo: %s · %s · %d databases", l.At.Local().Format("02/01 15:04"), databases.HumanSize(l.Size), l.Databases)
		if !l.OK() {
			color = t.danger
			text = "Ultimo (" + l.At.Local().Format("02/01 15:04") + ") falhou: " + cmpOrStr(l.Error, strings.Join(l.Errors, "; "))
		}
		b.WriteString(wrap.Foreground(color).Render(text) + "\n")
	}
	if p.err != "" {
		b.WriteString("\n" + wrap.Foreground(t.danger).Render(p.err) + "\n")
	}
	if p.done != "" {
		b.WriteString("\n" + wrap.Foreground(t.ok).Render(p.done) + "\n")
	}
	b.WriteString("\n" + wrap.Inherit(muted).Render("Restaurar: cais backup files e cais backup restore <arquivo> -key <arquivo> postgres/<db> (na VPS).") + "\n")
	return t.panel(p.active, w-2, h).MaxHeight(h + 2).Render(b.String())
}

// lastBackupLabel e o status curto do card Backups.
func lastBackupLabel(st core.BackupStatus, now time.Time) (string, bool) {
	switch {
	case st.Running:
		return "fazendo backup...", true
	case !st.Ready():
		return "nao configurado", false
	case st.Last == nil:
		return "configurado, sem backup ainda", true
	case !st.Last.OK():
		return "ultimo falhou", false
	case now.Sub(st.Last.At) > 36*time.Hour && st.Time != "":
		return "atrasado (ultimo " + st.Last.At.Local().Format("02/01") + ")", false
	}
	return "ok · " + st.Last.At.Local().Format("02/01 15:04"), true
}
