package docker

import (
	"context"
	"os/exec"
)

// ContainerLogs le stdout e stderr sem seguir o processo. Mantem no maximo
// 256 KiB, inclusive quando uma linha do container e muito grande.
func ContainerLogs(ctx context.Context, container string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", "logs", "--tail", "200", "--timestamps", container)
	out := &logTail{}
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	return string(out.data), err
}

type logTail struct{ data []byte }

func (b *logTail) Write(p []byte) (int, error) {
	const limit = 256 * 1024
	n := len(p)
	if len(p) >= limit {
		b.data = append(b.data[:0], p[len(p)-limit:]...)
	} else {
		if extra := len(b.data) + len(p) - limit; extra > 0 {
			b.data = b.data[extra:]
		}
		b.data = append(b.data, p...)
	}
	return n, nil
}
