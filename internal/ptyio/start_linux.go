//go:build linux

package ptyio

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// Start roda cmd num terminal novo (pty), como terminal de controle dele,
// e devolve o lado mestre. O processo e esperado numa goroutine; ele
// termina quando sai sozinho ou quando todos fecham o mestre (SIGHUP).
func Start(cmd *exec.Cmd, rows, cols uint16) (*os.File, error) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("abrir um terminal: %w", err)
	}
	fd := int(master.Fd())
	slave, err := func() (*os.File, error) {
		if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
			return nil, err
		}
		n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
		if err != nil {
			return nil, err
		}
		return os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	}()
	if err != nil {
		master.Close()
		return nil, fmt.Errorf("abrir um terminal: %w", err)
	}
	defer slave.Close()
	if rows > 0 && cols > 0 {
		unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Row: rows, Col: cols})
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	// sessao nova com o pty como terminal de controle: ctrl+c e o tamanho
	// da janela chegam no processo
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		master.Close()
		return nil, err
	}
	go cmd.Wait()
	return master, nil
}
