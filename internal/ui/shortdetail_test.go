package ui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/host"
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

// as linhas dos itens vem quebradas na largura do painel: quebradas pelo
// terminal ocupavam mais linhas do que a rolagem contava, e o Preparar VPS
// (o fim do painel) saia cortado
func TestSegurancaQuebraNaLargura(t *testing.T) {
	p := newSetupPage(nil, zone.New(), newTheme(true))
	long := strings.Repeat("4000/tcp (container um-app-com-nome-comprido-1), ", 6)
	p.env = core.Environment{Security: []host.Item{
		{ID: "upgrades", Title: "Pacotes do sistema (atualizar uma vez por mes)", Detail: "139 pacotes esperando e nenhuma atualizacao a mao no ultimo mes: hora de atualizar", Fixable: true},
		{ID: "ports", Title: "Portas abertas pra internet", Detail: "fora do SSH e do proxy: " + long},
	}}
	const w = 90
	for i, l := range strings.Split(p.viewSecurity(w), "\n") {
		if lipgloss.Width(l) > w {
			t.Errorf("linha %d com %d colunas (painel de %d): %q", i, lipgloss.Width(l), w, l)
		}
	}
}
