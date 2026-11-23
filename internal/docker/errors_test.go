//go:build unix

package docker

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// A causa do erro (codigo de saida) continua na cadeia, mesmo depois do
// corte das ultimas linhas e do embrulho com o contexto da acao.
func TestErroGuardaOCodigoDeSaida(t *testing.T) {
	_, err := execRunner(context.Background(), "sh", "-c", "echo progresso >&2; echo deu ruim >&2; exit 3")
	err = fmt.Errorf("docker compose up (x): %w", tail(err, 1))

	if got, want := err.Error(), "docker compose up (x): deu ruim"; got != want {
		t.Fatalf("mensagem %q; esperado %q (so a ultima linha do stderr)", got, want)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
		t.Fatalf("errors.As nao achou o ExitError com codigo 3: %v", err)
	}
	var cmdErr *CmdError
	if !errors.As(err, &cmdErr) || cmdErr.Args[0] != "sh" {
		t.Fatalf("errors.As nao achou o CmdError com o comando: %#v", cmdErr)
	}
}

// Prazo estourado e reconhecivel com errors.Is, sem olhar o texto.
func TestErroDePrazoReconhecivel(t *testing.T) {
	withGrace(t, time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := execRunner(ctx, "sh", "-c", "echo comecou >&2; sleep 30")
	err = fmt.Errorf("docker compose build (x): %w", tail(err, 20))

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("errors.Is(err, DeadlineExceeded) falso: %v", err)
	}
	if !strings.Contains(err.Error(), "comecou") {
		t.Fatalf("a mensagem perdeu o stderr: %v", err)
	}
}

// Sem stderr, a mensagem e a da causa.
func TestErroSemStderrUsaACausa(t *testing.T) {
	_, err := execRunner(context.Background(), "sh", "-c", "exit 4")
	if err == nil || err.Error() != "exit status 4" {
		t.Fatalf("erro %v; esperado a mensagem da causa", err)
	}
}

// Stderr enorme (build verboso): o erro guarda so o fim, dentro do limite.
func TestErroGuardaSoOFimDoStderr(t *testing.T) {
	// ~1 MiB de linhas e, por ultimo, o erro de verdade
	script := `i=0; while [ $i -lt 20000 ]; do echo "linha de progresso $i com texto pra encher" >&2; i=$((i+1)); done; echo "ERRO FINAL" >&2; exit 1`
	_, err := execRunner(context.Background(), "sh", "-c", script)

	var cmdErr *CmdError
	if !errors.As(err, &cmdErr) {
		t.Fatalf("sem CmdError: %v", err)
	}
	if len(cmdErr.Stderr) > stderrLimit {
		t.Fatalf("stderr guardado com %d bytes; limite %d", len(cmdErr.Stderr), stderrLimit)
	}
	if !strings.HasSuffix(cmdErr.Stderr, "ERRO FINAL") {
		t.Fatalf("o fim do stderr se perdeu: ...%q", cmdErr.Stderr[max(0, len(cmdErr.Stderr)-60):])
	}
	if got := tail(err, 1).Error(); got != "ERRO FINAL" {
		t.Fatalf("ultima linha %q", got)
	}
}

// O exec com stdin (SQL com senha) mantem a mensagem e passa a guardar a
// causa.
func TestExecInputErroComCausa(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("sem docker")
	}
	_, err := ExecInput(context.Background(), "cais-container-que-nao-existe", []byte("x"), "true")
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("errors.As nao achou o ExitError: %v", err)
	}
	if !strings.HasPrefix(err.Error(), "docker exec cais-container-que-nao-existe true: ") {
		t.Fatalf("mensagem mudou: %v", err)
	}
}

func TestTailBuffer(t *testing.T) {
	b := &tailBuffer{limit: 5}
	b.Write([]byte("abc"))
	b.Write([]byte("defg"))
	if b.String() != "cdefg" {
		t.Fatalf("%q", b.String())
	}
	b.Write([]byte("0123456789"))
	if b.String() != "56789" {
		t.Fatalf("%q", b.String())
	}
}
