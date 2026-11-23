package docker

import (
	"context"
)

// ContainerLogs le stdout e stderr sem seguir o processo. Mantem no maximo
// 256 KiB, inclusive quando uma linha do container e muito grande.
func ContainerLogs(ctx context.Context, container string) (string, error) {
	cmd := command(ctx, "docker", "logs", "--tail", "200", "--timestamps", container)
	out := &tailBuffer{limit: 256 * 1024}
	cmd.Stdout, cmd.Stderr = out, out
	err := run(ctx, cmd)
	return out.String(), err
}
