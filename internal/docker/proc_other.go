//go:build !unix

package docker

import "os/exec"

// Fora de unix nao ha grupo de processos: fica o cancelamento padrao (mata
// so o processo) e o WaitDelay fecha os pipes.
func groupCancel(*exec.Cmd) {}

func killGroup(*exec.Cmd) {}
