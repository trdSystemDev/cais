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
		if err != nil {
			// o Run devolve o sinal ("signal: terminated"); o motivo vai
			// junto na cadeia pra errors.Is(err, context.DeadlineExceeded)
			err = &canceledError{err: err, cause: context.Cause(ctx)}
		}
	}
	return err
}

// canceledError e a falha de um comando cancelado: a mensagem e a do
// comando e a cadeia tem tambem o motivo do cancelamento.
type canceledError struct {
	err, cause error
}

func (e *canceledError) Error() string   { return e.err.Error() }
func (e *canceledError) Unwrap() []error { return []error{e.err, e.cause} }
