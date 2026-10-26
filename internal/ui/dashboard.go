package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/rivo/tview"

	"github.com/trdSystemDev/vpsctl/internal/docker"
)

// dashboard mostra o estado do ambiente (Docker, sistema) e tem um botao
// pra checar de novo. A checagem roda fora da goroutine da UI.
type dashboard struct {
	*tview.Flex
	app    *tview.Application
	check  DockerChecker
	status *tview.TextView
	button *tview.Button
}

func newDashboard(app *tview.Application, check DockerChecker) *dashboard {
	d := &dashboard{
		Flex:   tview.NewFlex().SetDirection(tview.FlexRow),
		app:    app,
		check:  check,
		status: tview.NewTextView().SetDynamicColors(true),
	}
	d.status.SetBorder(true).SetTitle(" Ambiente ")

	d.button = tview.NewButton("Verificar novamente").SetSelectedFunc(d.refresh)

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

// refresh dispara uma checagem nova. Pode ser chamado antes de Run: a
// atualizacao fica na fila do tview e e aplicada quando a UI subir.
func (d *dashboard) refresh() {
	d.status.SetText("\n [yellow]Verificando o ambiente...")
	d.button.SetDisabled(true)
	go func() {
		s := d.check(context.Background())
		d.app.QueueUpdateDraw(func() {
			d.status.SetText(renderStatus(s))
			d.button.SetDisabled(false)
		})
	}()
}

func renderStatus(s docker.Status) string {
	var b strings.Builder
	b.WriteString("\n [::b]Docker[::-]\n")
	line(&b, "CLI", s.ClientVersion, s.ClientErr)
	if errors.Is(s.ClientErr, docker.ErrNotInstalled) {
		// sem o CLI nao da pra checar o resto; nao repete a mesma mensagem
		b.WriteString("   [gray]-  Daemon   depende do CLI\n")
		b.WriteString("   [gray]-  Compose  depende do CLI\n")
	} else {
		line(&b, "Daemon", s.ServerVersion, s.ServerErr)
		line(&b, "Compose", s.ComposeVersion, s.ComposeErr)
	}

	b.WriteString("\n [::b]Sistema[::-]\n")
	host, _ := os.Hostname()
	fmt.Fprintf(&b, "   [gray]Host     [white]%s\n", tview.Escape(host))
	fmt.Fprintf(&b, "   [gray]Sistema  [white]%s/%s\n", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS != "linux" {
		b.WriteString("   [yellow]! O vpsctl foi feito pra rodar na VPS Linux; aqui so da pra navegar na interface.\n")
	}

	b.WriteString("\n")
	if s.OK() {
		b.WriteString(" [green::b]Pronto.[-::-] O Docker esta funcionando -- o proximo passo e a aba Setup.\n")
	} else {
		b.WriteString(" [red::b]Resolva os itens em vermelho[-::-] antes de usar o Setup.\n")
	}
	return b.String()
}

func line(b *strings.Builder, label, version string, err error) {
	if err != nil {
		fmt.Fprintf(b, "   [red]x[-] [gray]%-8s [red]%s\n", label, tview.Escape(firstLine(err.Error())))
		return
	}
	fmt.Fprintf(b, "   [green]v[-] [gray]%-8s [white]%s\n", label, tview.Escape(version))
}

// firstLine corta mensagens de erro longas do docker (que as vezes vem
// com varias linhas de dica) pra caber numa linha da tela.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
