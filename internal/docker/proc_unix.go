//go:build unix

package docker

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// groupCancel poe o comando num grupo de processos proprio e faz o
// cancelamento mandar SIGTERM pro grupo: o docker e o compose recebem
// juntos e o compose interrompe o build ou o up.
func groupCancel(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}

// killGroup manda SIGKILL pro grupo do comando (o que sobrou dele).
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
