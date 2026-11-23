package docker

import (
	"context"
	"os/exec"
	"time"
)

// killGrace e quanto um comando cancelado (prazo do deploy, desligamento)
// tem pra sair sozinho depois do SIGTERM. Passou, o grupo inteiro leva
// SIGKILL e os pipes fecham. Variavel pros testes.
var killGrace = 10 * time.Second

// command monta todo comando que o pacote roda. Pasta neutra: comando do
// compose sem -f nunca pega um arquivo da pasta de onde o daemon foi
// iniciado. E um cancelamento que alcanca os filhos: o docker roda o
// compose como outro processo, e matar so o docker deixava o compose (e o
// build) vivo segurando a saida, com o Wait preso ate ele terminar.
func command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = "/"
	cmd.WaitDelay = killGrace
	groupCancel(cmd)
	return cmd
}

// run roda cmd e, se ctx foi cancelado, garante que nenhum processo do
// grupo fica vivo depois (um filho que ignorou o SIGTERM).
func run(ctx context.Context, cmd *exec.Cmd) error {
	err := cmd.Run()
	if ctx.Err() != nil {
		killGroup(cmd)
	}
	return err
}
