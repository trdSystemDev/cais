package ui

import (
	"context"
	"strconv"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/trdSystemDev/vpsctl/internal/core"
)

var (
	styleTab       = tcell.StyleDefault.Foreground(tcell.ColorSilver).Background(tcell.ColorDefault)
	styleTabFocus  = tcell.StyleDefault.Foreground(tcell.ColorWhite).Background(tcell.ColorDarkSlateGray)
	styleTabActive = tcell.StyleDefault.Foreground(tcell.ColorWhite).Background(tcell.ColorNavy).Bold(true)
	styleQuit      = tcell.StyleDefault.Foreground(tcell.ColorWhite).Background(tcell.ColorMaroon)
)

type tab struct {
	name   string
	button *tview.Button
	page   tview.Primitive
}

// App e a aplicacao TUI inteira: barra de abas, paginas e rodape. E so uma
// visao do core: le o estado via Subscribe e dispara acoes nele.
type App struct {
	core   *core.Core
	app    *tview.Application
	pages  *tview.Pages
	tabs   []*tab
	active int
	dash   *dashboard
}

// New monta a interface. Nada e desenhado ate Run ser chamado.
func New(c *core.Core) *App {
	a := &App{
		core:  c,
		app:   tview.NewApplication().EnableMouse(true),
		pages: tview.NewPages(),
		dash:  newDashboard(c),
	}

	a.addTab("Dashboard", a.dash)
	a.addTab("Apps", placeholder("Apps", 3, "Criar app pelo link do GitHub, deploy, logs ao vivo, restart."))
	a.addTab("Secrets", placeholder("Secrets", 3, "Secrets centrais e por app: gerar, copiar, propagar."))
	a.addTab("Bancos", placeholder("Bancos", 4, "Databases e usuarios no Postgres/MySQL, instancias dedicadas."))
	a.addTab("Infra", placeholder("Infra", 2, "Status das stacks base: BunkerWeb, registry, Woodpecker, DBGate."))
	a.addTab("Setup", placeholder("Setup", 2, "Assistente que prepara uma VPS vazia com tudo acima."))

	tabBar := tview.NewFlex()
	for _, t := range a.tabs {
		tabBar.AddItem(t.button, len(t.name)+4, 0, false)
		tabBar.AddItem(tview.NewBox(), 1, 0, false)
	}
	quit := tview.NewButton("Sair").SetSelectedFunc(a.app.Stop)
	quit.SetStyle(styleQuit).SetActivatedStyle(styleQuit)
	tabBar.AddItem(tview.NewBox(), 0, 1, false)
	tabBar.AddItem(quit, 8, 0, false)

	footer := tview.NewTextView().
		SetDynamicColors(true).
		SetText(" [gray]Clique nas abas e botoes  ·  [white]1-6[gray] troca de aba  ·  [white]Tab[gray] proximo campo  ·  [white]Ctrl+Q[gray] sai")

	root := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(tabBar, 1, 0, false).
		AddItem(a.pages, 0, 1, true).
		AddItem(footer, 1, 0, false)

	a.app.SetRoot(root, true)
	a.app.SetInputCapture(a.globalKeys)
	a.selectTab(0)
	return a
}

// render redesenha todas as telas a partir de uma foto do core. Roda
// sempre na goroutine da UI (via QueueUpdateDraw).
func (a *App) render(s core.State) {
	a.dash.render(s)
}

func (a *App) addTab(name string, page tview.Primitive) {
	idx := len(a.tabs)
	b := tview.NewButton(name)
	b.SetSelectedFunc(func() { a.selectTab(idx) })
	// o tview converte em duplo-clique qualquer clique ate 500ms depois do
	// anterior (mesmo em outro lugar da tela), e Button ignora duplo-clique.
	// Trocar de aba e idempotente, entao aqui duplo-clique vale como clique.
	b.SetMouseCapture(func(action tview.MouseAction, ev *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		if action == tview.MouseLeftDoubleClick {
			action = tview.MouseLeftClick
		}
		return action, ev
	})
	a.tabs = append(a.tabs, &tab{name: name, button: b, page: page})
	a.pages.AddPage(name, page, true, idx == 0)
}

func (a *App) selectTab(idx int) {
	a.active = idx
	for i, t := range a.tabs {
		if i == idx {
			t.button.SetStyle(styleTabActive).SetActivatedStyle(styleTabActive)
		} else {
			t.button.SetStyle(styleTab).SetActivatedStyle(styleTabFocus)
		}
	}
	t := a.tabs[idx]
	a.pages.SwitchToPage(t.name)
	a.app.SetFocus(t.page)
}

// globalKeys trata os atalhos que valem em qualquer tela. Numeros so trocam
// de aba quando o foco nao esta num campo de texto, pra nao roubar digitacao.
func (a *App) globalKeys(ev *tcell.EventKey) *tcell.EventKey {
	if ev.Key() == tcell.KeyCtrlQ {
		a.app.Stop()
		return nil
	}
	if ev.Key() == tcell.KeyRune && !a.typing() {
		r := ev.Rune()
		if r >= '1' && int(r-'1') < len(a.tabs) {
			a.selectTab(int(r - '1'))
			return nil
		}
	}
	return ev
}

func (a *App) typing() bool {
	switch a.app.GetFocus().(type) {
	case *tview.InputField, *tview.TextArea:
		return true
	}
	return false
}

// Run se inscreve no core, dispara a checagem inicial do ambiente e
// bloqueia ate o usuario sair.
func (a *App) Run() error {
	states, unsubscribe := a.core.Subscribe()
	defer unsubscribe()
	go func() {
		for s := range states {
			a.app.QueueUpdateDraw(func() { a.render(s) })
		}
	}()

	go a.core.CheckEnvironment(context.Background())
	return a.app.Run()
}

// ActiveTab devolve o nome da aba selecionada.
func (a *App) ActiveTab() string {
	return a.tabs[a.active].name
}

func placeholder(title string, phase int, desc string) tview.Primitive {
	tv := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	tv.SetBorder(true).SetTitle(" " + title + " ")
	tv.SetText("\n\n[::b]" + title + "[::-]\n\n[gray]" + desc + "\n\n[yellow]Em construcao -- chega na fase " + strconv.Itoa(phase))
	return tv
}
