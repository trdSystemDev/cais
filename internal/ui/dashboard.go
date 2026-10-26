package ui

import (
	"context"
	"fmt"
	"strings"

	"github.com/rivo/tview"

	"github.com/trdSystemDev/cais/internal/core"
)

// dashboard mostra o estado do ambiente. Nao guarda estado proprio: o
// botao so dispara a acao no core, e render redesenha a partir da foto
// que o core publica (venha a acao daqui, do MCP ou de onde for).
type dashboard struct {
	*tview.Flex
	status *tview.TextView
	button *tview.Button
}

func newDashboard(c core.API) *dashboard {
	d := &dashboard{
		Flex:   tview.NewFlex().SetDirection(tview.FlexRow),
		status: tview.NewTextView().SetDynamicColors(true),
	}
	d.status.SetBorder(true).SetTitle(" Ambiente ")

	d.button = tview.NewButton("Verificar novamente").SetSelectedFunc(func() {
		go c.CheckEnvironment(context.Background())
	})

	buttons := tview.NewFlex().
		AddItem(tview.NewBox(), 1, 0, false).
		AddItem(d.button, 23, 0, true).
		AddItem(tview.NewBox(), 0, 1, false)

	d.AddItem(d.status, 0, 1, false).
		AddItem(tview.NewBox(), 1, 0, false).
		AddItem(buttons, 1, 0, true).
		AddItem(tview.NewBox(), 1, 0, false)
	return d
}

func (d *dashboard) render(s core.State) {
	d.status.SetText(renderEnvironment(s.Environment))
	d.button.SetDisabled(s.Environment.Checking)
}

func renderEnvironment(e core.Environment) string {
	if e.Checking || !e.Checked() {
		return "\n [yellow]Verificando o ambiente..."
	}

	var b strings.Builder
	b.WriteString("\n [::b]Docker[::-]\n")
	line(&b, "CLI", e.Docker.CLI)
	if !e.Docker.Installed {
		// sem o CLI nao da pra checar o resto; nao repete a mesma mensagem
		b.WriteString("   [gray]-  Daemon   depende do CLI\n")
		b.WriteString("   [gray]-  Compose  depende do CLI\n")
	} else {
		line(&b, "Daemon", e.Docker.Daemon)
		line(&b, "Compose", e.Docker.Compose)
	}

	b.WriteString("\n [::b]Sistema[::-]\n")
	fmt.Fprintf(&b, "   [gray]Host     [white]%s\n", tview.Escape(e.Host.Hostname))
	fmt.Fprintf(&b, "   [gray]Sistema  [white]%s/%s\n", e.Host.OS, e.Host.Arch)
	if !e.Host.Supported() {
		b.WriteString("   [yellow]! O cais foi feito pra rodar na VPS Linux; aqui so da pra navegar na interface.\n")
	}

	b.WriteString("\n")
	if e.Ready() {
		b.WriteString(" [green::b]Pronto.[-::-] O Docker esta funcionando -- o proximo passo e a aba Setup.\n")
	} else {
		b.WriteString(" [red::b]Resolva os itens em vermelho[-::-] antes de usar o Setup.\n")
	}
	fmt.Fprintf(&b, "\n [gray]Ultima verificacao: %s\n", e.CheckedAt.Local().Format("02/01/2006 15:04:05"))
	return b.String()
}

func line(b *strings.Builder, label string, c core.Component) {
	if !c.OK() {
		fmt.Fprintf(b, "   [red]x[-] [gray]%-8s [red]%s\n", label, tview.Escape(firstLine(c.Error)))
		return
	}
	fmt.Fprintf(b, "   [green]v[-] [gray]%-8s [white]%s\n", label, tview.Escape(c.Version))
}

// firstLine corta mensagens de erro longas do docker (que as vezes vem
// com varias linhas de dica) pra caber numa linha da tela.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
