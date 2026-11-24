//go:build !linux

package ptyio

import (
	"errors"
	"os"
	"os/exec"
)

// Start so existe no Linux (a VPS).
func Start(cmd *exec.Cmd, rows, cols uint16) (*os.File, error) {
	return nil, errors.New("terminal (pty) so no Linux")
}
