package ptyio

import (
	"bytes"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// o processo roda num terminal de verdade, recebe o que se digita e o
// Attach volta sozinho quando ele sai
func TestStartEAttach(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("pty so no Linux")
	}
	f, err := Start(exec.Command("sh", "-c", `[ -t 0 ] && echo terminal; read x; echo "recebi:$x"`), 30, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Attach(f, strings.NewReader("oi\n"), &out); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, "terminal") || !strings.Contains(s, "recebi:oi") {
		t.Errorf("saida: %q", s)
	}
}
