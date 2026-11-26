package docker

import (
	"context"
	"strconv"
)

// LogsOptions escolhe o pedaco do log: as ultimas Tail linhas (0: 200) e,
// com Since, so as de depois dele (duracao como 10m ou um horario RFC 3339,
// como o docker logs --since aceita).
type LogsOptions struct {
	Tail  int    `json:"tail,omitempty"`
	Since string `json:"since,omitempty"`
}

// ContainerLogs le stdout e stderr sem seguir o processo. Mantem no maximo
// 256 KiB, inclusive quando uma linha do container e muito grande.
func ContainerLogs(ctx context.Context, container string, opt LogsOptions) (string, error) {
	tail := opt.Tail
	if tail <= 0 {
		tail = 200
	}
	args := []string{"logs", "--tail", strconv.Itoa(tail), "--timestamps"}
	if opt.Since != "" {
		args = append(args, "--since", opt.Since)
	}
	cmd := command(ctx, "docker", append(args, container)...)
	out := &tailBuffer{limit: 256 * 1024}
	cmd.Stdout, cmd.Stderr = out, out
	err := run(ctx, cmd)
	return out.String(), err
}
