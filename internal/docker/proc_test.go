//go:build unix

package docker

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func withGrace(t *testing.T, d time.Duration) {
	t.Helper()
	old := killGrace
	killGrace = d
	t.Cleanup(func() { killGrace = old })
}

// O caso do docker com o compose: o processo que o cais roda tem um filho
// que segura a saida. Antes, matar so o pai deixava o Run preso ate o filho
// terminar (aqui, 30s).
func TestCanceladoNaoEsperaOFilho(t *testing.T) {
	withGrace(t, 2*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := execRunner(ctx, "sh", "-c", "sleep 30; true")
	took := time.Since(start)

	if err == nil {
		t.Fatal("comando cancelado devolveu sucesso")
	}
	// o filho morre com o SIGTERM do grupo: nem precisa da carencia
	if took > time.Second {
		t.Fatalf("Run voltou em %v; esperado logo depois do prazo (200ms)", took)
	}
}

// Um filho que ignora o SIGTERM: o Run volta no fim da carencia e o filho
// nao fica vivo (o grupo leva SIGKILL).
func TestFilhoQueIgnoraSIGTERMMorreNaCarencia(t *testing.T) {
	grace := 500 * time.Millisecond
	withGrace(t, grace)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	cmd := command(ctx, "sh", "-c", `trap "" TERM; sleep 30 & echo $!; wait`)
	var out strings.Builder
	cmd.Stdout = &out
	start := time.Now()
	err := run(ctx, cmd)
	took := time.Since(start)

	if err == nil {
		t.Fatal("comando cancelado devolveu sucesso")
	}
	if limit := 200*time.Millisecond + grace + time.Second; took > limit {
		t.Fatalf("Run voltou em %v; limite %v (prazo + carencia + 1s)", took, limit)
	}

	pid, perr := strconv.Atoi(strings.TrimSpace(out.String()))
	if perr != nil {
		t.Fatalf("pid do filho: %q", out.String())
	}
	// o init recolhe o filho morto num instante; ate la ele ainda responde
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("o filho %d continua vivo depois do Run", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Quem trata o SIGTERM e sai sozinho nao espera a carencia.
func TestSIGTERMTratadoSaiSemEsperarACarencia(t *testing.T) {
	withGrace(t, 5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := execRunner(ctx, "sh", "-c", `trap "exit 0" TERM; while :; do sleep 0.05; done`)
	took := time.Since(start)

	if took > 2*time.Second {
		t.Fatalf("Run voltou em %v; o processo saiu com o SIGTERM e nao devia esperar a carencia (5s)", took)
	}
	_ = err // saiu com 0 pelo trap: o erro pode ser nil ou o do contexto
}

// Sem cancelamento nada muda: saida e erro como antes.
func TestSemCancelamentoComportamentoIgual(t *testing.T) {
	out, err := execRunner(context.Background(), "sh", "-c", "echo ok; pwd")
	if err != nil {
		t.Fatal(err)
	}
	if out != "ok\n/" {
		t.Fatalf("saida %q; esperado ok e a pasta neutra /", out)
	}
	if _, err := execRunner(context.Background(), "sh", "-c", "echo falhou >&2; exit 3"); err == nil || err.Error() != "falhou" {
		t.Fatalf("erro %v; esperado o stderr", err)
	}
}
