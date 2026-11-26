package ui

import (
	"fmt"
	"strings"
	"testing"
)

// a lista de portas de uma VPS com dezenas de containers ocupava a tela
// inteira: na TUI ficam as primeiras e quantas faltam
func TestShortDetail(t *testing.T) {
	var ports []string
	for i := range 50 {
		ports = append(ports, fmt.Sprintf("%d/tcp (container app-%d)", 4000+i, i))
	}
	got := shortDetail("fora do SSH e do proxy: "+strings.Join(ports, ", "), 6)
	want := "fora do SSH e do proxy: " + strings.Join(ports[:6], ", ") + " e mais 44 (cais host check lista todas)"
	if got != want {
		t.Fatalf("\ngot  %s\nwant %s", got, want)
	}
	for _, d := range []string{"so SSH e o proxy (22, 80, 443)", "fora do SSH e do proxy: 3306/tcp (mysqld)", "Ubuntu 24.04.3 LTS"} {
		if got := shortDetail(d, 6); got != d {
			t.Errorf("mudou um detalhe curto: %q -> %q", d, got)
		}
	}
}
