package ui

import (
	"context"
	"fmt"
	"io"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/ptyio"
)

// O Shell da secao Logs de um app: o daemon abre o docker exec -it num
// terminal novo e manda ele pelo socket; a TUI sai da tela, liga o
// terminal a ele ate o shell sair e volta. Funciona pelo ssh cais@vps (o
// usuario cais nao fala com o Docker: quem abre e o daemon).

type (
	shellOpenedMsg struct {
		f         *os.File
		container string
		err       error
		infra     string // console de um banco da Infra (senao, shell de app)
	}
	shellDoneMsg struct {
		err   error
		infra string
	}
)

// shell abre o shell do container escolhido no seletor dos Logs.
func (p *appsPage) shell() tea.Cmd {
	a := p.selected()
	if a == nil || len(a.Containers) == 0 {
		return nil
	}
	ct := a.Containers[min(p.container, len(a.Containers)-1)]
	opener, ok := p.api.(core.ShellOpener)
	if !ok {
		p.shellErr = "esta conexao com o daemon nao abre shell"
		return nil
	}
	p.shellErr = ""
	req := core.ShellRequest{App: a.Name, Service: ct.Name, Term: os.Getenv("TERM")}
	// abre antes de sair da tela: se falhar, o erro aparece na TUI
	return func() tea.Msg {
		f, err := opener.OpenShell(context.Background(), req)
		return shellOpenedMsg{f: f, container: "Shell em " + ct.Name, err: err}
	}
}

// shellExec liga o terminal ao shell aberto (tea.Exec: a TUI devolve o
// terminal enquanto ele roda).
type shellExec struct {
	f         *os.File
	container string
	in        io.Reader
	out       io.Writer
}

func (s *shellExec) SetStdin(r io.Reader)  { s.in = r }
func (s *shellExec) SetStdout(w io.Writer) { s.out = w }
func (s *shellExec) SetStderr(io.Writer)   {}

func (s *shellExec) Run() error {
	fmt.Fprintf(s.out, "%s. Saia com exit ou ctrl+d pra voltar ao cais.\r\n", s.container)
	return ptyio.Attach(s.f, s.in, s.out)
}

// onShell trata as respostas do shell (Apps) e do console (Infra); ok diz
// se a mensagem era dele. O erro aparece onde o botao foi apertado.
func (m *model) onShell(msg tea.Msg) (tea.Cmd, bool) {
	fail := func(infra, text string) {
		if infra != "" {
			m.infraMsgStack, m.infraMsgC, m.infraMsg = infra, "danger", text
		} else {
			m.apps.shellErr = text
		}
	}
	switch msg := msg.(type) {
	case shellOpenedMsg:
		if msg.err != nil {
			fail(msg.infra, firstLine(msg.err.Error()))
			return nil, true
		}
		infra := msg.infra
		return tea.Exec(&shellExec{f: msg.f, container: msg.container}, func(err error) tea.Msg {
			return shellDoneMsg{err: err, infra: infra}
		}), true
	case shellDoneMsg:
		if msg.err != nil {
			fail(msg.infra, "o terminal parou: "+msg.err.Error())
		}
		return nil, true
	}
	return nil, false
}

// openConsole abre o psql ou o mysql do banco da Infra.
func (m *model) openConsole(engine string) tea.Cmd {
	opener, ok := m.api.(core.ShellOpener)
	if !ok {
		m.infraMsgStack, m.infraMsgC, m.infraMsg = engine, "danger", "esta conexao com o daemon nao abre terminal"
		return nil
	}
	m.infraMsg = ""
	title := map[string]string{"postgres": "psql", "mysql": "mysql"}[engine] + " no " + engine + " (administrador)"
	req := core.ShellRequest{Engine: engine, Term: os.Getenv("TERM")}
	return func() tea.Msg {
		f, err := opener.OpenShell(context.Background(), req)
		return shellOpenedMsg{f: f, container: title, err: err, infra: engine}
	}
}
