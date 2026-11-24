// Package ptyio liga o terminal local ao mestre de um pty aberto pelo
// daemon (o shell de um container): o cais app shell e o Shell da TUI.
package ptyio

import (
	"errors"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/muesli/cancelreader"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// Size e o tamanho do terminal em out (0, 0 se nao for um terminal).
func Size(out io.Writer) (rows, cols uint16) {
	if f, ok := out.(*os.File); ok {
		if w, h, err := term.GetSize(int(f.Fd())); err == nil {
			return uint16(h), uint16(w)
		}
	}
	return 0, 0
}

// IsTerminal diz se in e um terminal (o shell precisa de um).
func IsTerminal(in io.Reader) bool {
	f, ok := in.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// Attach copia in pro mestre e o mestre pra out ate o shell sair, com o
// terminal local em modo cru (ctrl+c, setas e tab vao pro shell) e o
// tamanho da janela acompanhando. Fecha o mestre no fim.
func Attach(master *os.File, in io.Reader, out io.Writer) error {
	defer master.Close()
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		old, err := term.MakeRaw(int(f.Fd()))
		if err != nil {
			return err
		}
		defer term.Restore(int(f.Fd()), old)
	}

	resize := func() {
		if rows, cols := Size(out); rows > 0 {
			unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: rows, Col: cols})
		}
	}
	resize()
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	done := make(chan struct{})
	defer func() {
		signal.Stop(winch)
		close(done)
	}()
	go func() {
		for {
			select {
			case <-winch:
				resize()
			case <-done:
				return
			}
		}
	}()

	// o leitor do terminal local tem que parar quando o shell sai: senao
	// ele come a proxima tecla de quem chamou (a TUI)
	r := in
	if cr, err := cancelreader.NewReader(in); err == nil {
		defer cr.Cancel()
		r = cr
	}
	go io.Copy(master, r)
	_, err := io.Copy(out, master)
	// o shell saiu: o pty fecha com EIO
	if errors.Is(err, syscall.EIO) || errors.Is(err, os.ErrClosed) {
		err = nil
	}
	return err
}
