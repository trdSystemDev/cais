package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/rivo/tview"

	"github.com/trdSystemDev/cais/internal/bootstrap"
	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/stacks"
)

// setupPage e a aba Setup: formulario do config.yaml a esquerda e, a
// direita, o plano (o que o Setup faria) ou o andamento da execucao. Como o
// resto da TUI, so chama acoes do core; o progresso chega pelo estado, entao
// um Setup disparado pelo CLI tambem aparece aqui.
type setupPage struct {
	*tview.Flex
	app  *tview.Application
	core core.API

	form     *tview.Form
	domain   *tview.InputField
	email    *tview.InputField
	tls      *tview.DropDown
	stacks   map[string]*tview.Checkbox
	ghClient *tview.InputField
	ghAdmin  *tview.InputField
	ghSecret *tview.InputField
	hostCI   *tview.InputField
	hostReg  *tview.InputField

	panel  *tview.TextView // plano ou progresso
	status *tview.TextView // resultado da ultima acao

	// so tocados na goroutine da UI
	cfg      config.Config // ultima config lida, base do que o form nao mostra
	plan     bootstrap.Plan
	setup    core.Setup // ultima foto do Setup no estado
	showRun  bool       // painel mostra a execucao em vez do plano
	runStart bool       // o clique em Executar ainda nao virou Running no estado
	saveErr  string     // por que o ultimo Salvar falhou (cabe varias linhas)
}

var tlsOptions = []string{config.TLSLetsEncrypt, config.TLSSelfSigned}

func newSetupPage(app *tview.Application, c core.API) *setupPage {
	p := &setupPage{
		Flex:   tview.NewFlex(),
		app:    app,
		core:   c,
		form:   tview.NewForm(),
		stacks: map[string]*tview.Checkbox{},
		panel:  tview.NewTextView().SetDynamicColors(true).SetWordWrap(true),
		status: tview.NewTextView().SetDynamicColors(true),
	}

	input := func(label, placeholder string, width int) *tview.InputField {
		f := tview.NewInputField().SetLabel(label).SetPlaceholder(placeholder).SetFieldWidth(width)
		p.form.AddFormItem(f)
		return f
	}
	p.domain = input("Dominio", "exemplo.com.br", 30)
	p.email = input("E-mail", "voce@exemplo.com.br", 30)
	p.tls = tview.NewDropDown().SetLabel("Certificados").
		SetOptions([]string{"Let's Encrypt", "CA local (teste)"}, nil).SetCurrentOption(0)
	p.form.AddFormItem(p.tls)
	for _, st := range stacks.All {
		cb := tview.NewCheckbox().SetLabel(st.Name)
		p.stacks[st.Name] = cb
		p.form.AddFormItem(cb)
	}
	p.ghClient = input("GitHub client ID", "so com woodpecker", 30)
	p.ghAdmin = input("GitHub admin", "seu usuario", 30)
	p.ghSecret = input("GitHub secret", "so pra gravar ou trocar", 30)
	p.ghSecret.SetMaskCharacter('*')
	p.hostCI = input("Host do CI", "padrao ci.<dominio>", 30)
	p.hostReg = input("Host do registry", "padrao registry.<dominio>", 30)

	p.form.AddButton("Salvar", p.save)
	p.form.AddButton("Executar setup", p.run)
	// sem linha em branco entre os campos: o form inteiro cabe em ~20
	// linhas e os botoes aparecem mesmo num terminal de 30
	p.form.SetItemPadding(0)
	p.form.SetBorder(true).SetTitle(" Configuracao ")

	p.panel.SetBorder(true).SetTitle(" Plano ")

	left := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(p.form, 0, 1, true).
		AddItem(p.status, 2, 0, false)
	p.AddItem(left, 54, 0, true).
		AddItem(p.panel, 0, 1, false)
	return p
}

// load le a config e o plano do core e preenche a tela. Roda fora da
// goroutine da UI (as chamadas podem ir pelo socket).
func (p *setupPage) load() {
	go func() {
		ctx := context.Background()
		info, err := p.core.LoadConfig(ctx)
		if err != nil {
			p.app.QueueUpdateDraw(func() { p.setStatus("red", "Erro lendo a config: "+err.Error()) })
			return
		}
		plan, err := p.core.PlanSetup(ctx)
		p.app.QueueUpdateDraw(func() {
			p.fill(info.Config)
			if err != nil {
				p.setStatus("red", "Erro montando o plano: "+err.Error())
				return
			}
			p.plan = plan
			p.redraw()
		})
	}()
}

// refreshPlan remonta so o plano, sem mexer no que esta digitado.
func (p *setupPage) refreshPlan() {
	go func() {
		plan, err := p.core.PlanSetup(context.Background())
		p.app.QueueUpdateDraw(func() {
			if err != nil {
				p.setStatus("red", "Erro montando o plano: "+err.Error())
				return
			}
			p.plan = plan
			p.redraw()
		})
	}()
}

func (p *setupPage) fill(c config.Config) {
	p.cfg = c
	p.domain.SetText(c.Domain)
	p.email.SetText(c.Email)
	if c.LetsEncrypt() {
		p.tls.SetCurrentOption(0)
	} else {
		p.tls.SetCurrentOption(1)
	}
	for name, cb := range p.stacks {
		cb.SetChecked(c.HasStack(name))
	}
	p.ghClient.SetText(c.Woodpecker.GitHubClient)
	p.ghAdmin.SetText(c.Woodpecker.Admin)
	p.hostCI.SetText(c.Hosts.CI)
	p.hostReg.SetText(c.Hosts.Registry)
}

// formConfig monta a config a partir do formulario, mantendo o que ele nao
// mostra (portas do proxy) como estava.
func (p *setupPage) formConfig() config.Config {
	c := p.cfg
	c.Domain = strings.TrimSpace(p.domain.GetText())
	c.Email = strings.TrimSpace(p.email.GetText())
	opt, _ := p.tls.GetCurrentOption()
	c.TLS = ""
	if opt > 0 {
		c.TLS = tlsOptions[opt]
	}
	c.Stacks = nil
	for _, st := range stacks.All {
		if p.stacks[st.Name].IsChecked() {
			c.Stacks = append(c.Stacks, st.Name)
		}
	}
	c.Woodpecker.GitHubClient = strings.TrimSpace(p.ghClient.GetText())
	c.Woodpecker.Admin = strings.TrimSpace(p.ghAdmin.GetText())
	c.Hosts.CI = strings.TrimSpace(p.hostCI.GetText())
	c.Hosts.Registry = strings.TrimSpace(p.hostReg.GetText())
	return c
}

func (p *setupPage) save() {
	c := p.formConfig()
	secret := p.ghSecret.GetText()
	p.setStatus("yellow", "Salvando...")
	go func() {
		ctx := context.Background()
		err := p.core.SaveConfig(ctx, c)
		if err == nil && secret != "" {
			err = p.core.SetSecret(ctx, "github_client_secret", secret)
		}
		p.app.QueueUpdateDraw(func() {
			p.showRun = false
			if err != nil {
				// a validacao junta varios erros, um por linha: vao pro painel
				p.saveErr = err.Error()
				p.setStatus("red", "Nao salvou: veja o motivo ao lado.")
				p.redraw()
				return
			}
			p.saveErr = ""
			p.cfg = c
			p.ghSecret.SetText("")
			p.setStatus("green", "Config salva.")
		})
		p.refreshPlan()
	}()
}

func (p *setupPage) run() {
	if p.setup.Running {
		return
	}
	p.showRun = true
	p.runStart = true
	p.setStatus("yellow", "Executando o Setup...")
	p.redraw()
	go func() {
		_, err := p.core.RunSetup(context.Background())
		p.app.QueueUpdateDraw(func() {
			p.runStart = false
			if err != nil {
				p.setStatus("red", "Setup parou: "+firstLine(err.Error()))
			} else {
				p.setStatus("green", "Setup concluido.")
			}
		})
		// a CA e os bancos aparecem no fim; o plano novo traz as instrucoes
		p.refreshPlan()
	}()
}

// render recebe cada foto do core. Um Setup rodando (daqui, do CLI ou do
// MCP) troca o painel pro andamento.
func (p *setupPage) render(s core.State) {
	p.setup = s.Setup
	if s.Setup.Running {
		p.showRun = true
	}
	p.redraw()
}

func (p *setupPage) redraw() {
	running := p.setup.Running || p.runStart
	if p.showRun {
		p.panel.SetTitle(" Execucao ")
		p.panel.SetText(renderRun(p.setup, running, p.plan))
	} else {
		p.panel.SetTitle(" Plano ")
		text := renderPlan(p.plan)
		if p.saveErr != "" {
			var b strings.Builder
			b.WriteString("\n [red::b]Nao salvou[-::-]\n")
			for _, l := range strings.Split(p.saveErr, "\n") {
				fmt.Fprintf(&b, "   [red]%s\n", tview.Escape(l))
			}
			text = b.String() + text
		}
		p.panel.SetText(text)
	}
	if b := p.form.GetButton(p.form.GetButtonIndex("Executar setup")); b != nil {
		b.SetDisabled(running || !p.plan.Ready())
	}
}

func (p *setupPage) setStatus(color, msg string) {
	p.status.SetText(fmt.Sprintf(" [%s]%s", color, tview.Escape(msg)))
}

func renderPlan(p bootstrap.Plan) string {
	var b strings.Builder
	if !p.Configured {
		b.WriteString("\n [yellow]Ainda sem configuracao.[-]\n\n Preencha dominio e e-mail, escolha as stacks e clique em [::b]Salvar[::-].\n")
		for _, pr := range p.Problems {
			fmt.Fprintf(&b, "\n [gray]%s\n", tview.Escape(pr))
		}
		return b.String()
	}

	b.WriteString("\n [::b]Stacks[::-]\n")
	for _, s := range p.Stacks {
		color := map[string]string{bootstrap.ChangeNew: "green", bootstrap.ChangeModified: "yellow"}[s.Change]
		if color == "" {
			color = "gray"
		}
		fmt.Fprintf(&b, "   %-11s [%s]%-9s[-] [gray]%s\n", s.Name, color, s.Change, tview.Escape(s.Desc))
	}
	if len(p.Disable) > 0 {
		b.WriteString("\n [::b]Desativadas[::-] [gray](derrubadas no Setup; os volumes ficam)[-]\n")
		for _, s := range p.Disable {
			fmt.Fprintf(&b, "   %-11s [gray]%s\n", s.Name, tview.Escape(s.Desc))
		}
	}
	if len(p.CreateSecrets) > 0 {
		fmt.Fprintf(&b, "\n [::b]Secrets a gerar[::-]\n   [gray]%s\n", strings.Join(p.CreateSecrets, ", "))
	}
	if len(p.Problems) > 0 {
		b.WriteString("\n [red::b]Problemas[-::-]\n")
		for _, pr := range p.Problems {
			fmt.Fprintf(&b, "   [red]%s\n", tview.Escape(pr))
		}
	}
	if len(p.Missing) > 0 {
		b.WriteString("\n [red::b]Falta informar[-::-]\n")
		for _, m := range p.Missing {
			fmt.Fprintf(&b, "   [red]%s[-]: %s\n", m.Name, tview.Escape(m.Desc))
		}
	}
	if p.Ready() {
		b.WriteString("\n [green::b]Pronto pra executar.[-::-]")
		if p.CAFile == "" {
			b.WriteString(" Antes, aponte o DNS dos nomes pra esta VPS.")
		}
		b.WriteString("\n")
	}
	renderAccess(&b, p)
	return b.String()
}

var stepMark = map[bootstrap.Status]string{
	bootstrap.Pending: "[gray] .. ",
	bootstrap.Running: "[yellow] >> ",
	bootstrap.Done:    "[green] ok ",
	bootstrap.Failed:  "[red] !! ",
}

func renderRun(s core.Setup, running bool, plan bootstrap.Plan) string {
	var b strings.Builder
	b.WriteString("\n")
	if len(s.Steps) == 0 {
		b.WriteString(" [yellow]Comecando...\n")
		return b.String()
	}
	for _, st := range s.Steps {
		fmt.Fprintf(&b, "%s[-]%s", stepMark[st.Status], tview.Escape(st.Desc))
		if st.Detail != "" && st.Status != bootstrap.Failed {
			fmt.Fprintf(&b, " [gray]-- %s", tview.Escape(st.Detail))
		}
		b.WriteString("\n")
	}
	switch {
	case running:
		b.WriteString("\n [yellow]Executando... pode sair da tela: o Setup continua no daemon.\n")
	case s.Error != "":
		fmt.Fprintf(&b, "\n [red::b]Parou:[-::-] [red]%s\n\n [gray]Corrija e execute de novo: nada que ja existe e refeito.\n", tview.Escape(s.Error))
	default:
		b.WriteString("\n [green::b]Setup concluido.[-::-]\n")
		renderAccess(&b, plan)
	}
	return b.String()
}

// renderAccess mostra como chegar nos bancos e, no modo de teste, como
// confiar na CA local.
func renderAccess(b *strings.Builder, p bootstrap.Plan) {
	if len(p.Databases) > 0 {
		b.WriteString("\n [::b]Bancos[::-] [gray](so por tunel SSH)[-]\n")
		fmt.Fprintf(b, "   [white]%s\n", tview.Escape(p.Tunnel))
		for _, d := range p.Databases {
			fmt.Fprintf(b, "   [gray]%-8s localhost:%-5s usuario %-8s senha: sudo cat %s\n",
				d.Name, strconv.Itoa(d.Port), d.User, tview.Escape(d.PasswordFile))
		}
	}
	if p.CAFile != "" {
		b.WriteString("\n [::b]CA local[::-] [gray](confie nela nesta maquina)[-]\n")
		if p.Config.HasStack("registry") {
			dir := "/etc/docker/certs.d/" + p.Config.RegistryHost()
			fmt.Fprintf(b, "   [gray]sudo mkdir -p %s\n   sudo cp %s %s/ca.crt\n", dir, tview.Escape(p.CAFile), dir)
		}
		fmt.Fprintf(b, "   [gray]sudo trust anchor --store %s\n", tview.Escape(p.CAFile))
	}
}
