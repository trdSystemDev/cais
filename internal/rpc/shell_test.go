package rpc

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/core/coretest"
	"github.com/trdSystemDev/cais/internal/ptyio"
)

// shellAPI e o core falso com o OpenShell: abre um sh de verdade num pty.
type shellAPI struct {
	*coretest.Recorder
	got chan core.ShellRequest
}

func (s *shellAPI) OpenShell(_ context.Context, req core.ShellRequest) (*os.File, error) {
	s.got <- req
	if req.App == "nenhum" {
		return nil, errors.New("app nenhum nao encontrado")
	}
	return ptyio.Start(exec.Command("sh", "-c", "stty size; echo ola-do-shell"), req.Rows, req.Cols)
}

// o terminal do shell atravessa o socket como descritor, numa conexao so
// pra ele; o erro volta como erro
func TestShellPeloSocket(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("pty so no Linux")
	}
	api := &shellAPI{Recorder: coretest.NewRecorder(), got: make(chan core.ShellRequest, 2)}
	path := filepath.Join(t.TempDir(), "cais.sock")
	ln, err := Listen(path, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { Serve(ctx, ln, api); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	c, err := Dial(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	req := core.ShellRequest{App: "loja", Service: "web", Term: "xterm", Rows: 40, Cols: 120}
	f, err := c.OpenShell(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if got := <-api.got; got != req {
		t.Errorf("pedido: %+v", got)
	}
	f.SetReadDeadline(time.Now().Add(3 * time.Second))
	out, _ := io.ReadAll(f) // termina com EIO quando o sh sai
	f.Close()
	if s := string(out); !strings.Contains(s, "40 120") || !strings.Contains(s, "ola-do-shell") {
		t.Errorf("saida: %q", s)
	}

	if _, err := c.OpenShell(ctx, core.ShellRequest{App: "nenhum"}); err == nil || !strings.Contains(err.Error(), "nao encontrado") {
		t.Errorf("erro: %v", err)
	}
	// a conexao principal continua servindo
	if _, err := c.Snapshot(ctx); err != nil {
		t.Errorf("snapshot depois do shell: %v", err)
	}
}
