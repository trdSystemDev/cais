package docker

import (
	"os"
	"os/exec"

	"github.com/trdSystemDev/cais/internal/ptyio"
)

// shellScript prefere o bash e cai pro sh (alpine, busybox).
const shellScript = "if command -v bash >/dev/null 2>&1; then exec bash; else exec sh; fi"

// StartTerminal roda args no container (docker exec -it) num terminal novo e
// devolve o lado mestre: quem tem ele le e escreve como se fosse o terminal
// do comando, e o tamanho da janela muda nele. Sem args, abre o shell (bash
// ou sh). O docker exec termina quando o comando sai, ou quando todos
// fecham o mestre.
func StartTerminal(container, term string, rows, cols uint16, args ...string) (*os.File, error) {
	if term == "" {
		term = "xterm-256color"
	}
	if len(args) == 0 {
		args = []string{"sh", "-c", shellScript}
	}
	cmd := exec.Command("docker", append([]string{"exec", "-it", "-e", "TERM=" + term, container}, args...)...)
	return ptyio.Start(cmd, rows, cols)
}
