package ui

import (
	tea "charm.land/bubbletea/v2"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/sysstat"
)

func TestDashboardUsoDaMaquina(t *testing.T) {
	root := t.TempDir()
	var mu sync.Mutex
	now := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	busy, total := uint64(0), uint64(0)
	const gb = 1 << 30
	c := core.New(core.Deps{TraefikMetrics: noTraffic,
		CheckDocker: okChecker,
		ConfigPath:  filepath.Join(root, "config.yaml"),
		SecretsDir:  filepath.Join(root, "secrets"),
		Now: func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return now
		},
		ReadSystem: func() (sysstat.Sample, error) {
			mu.Lock()
			defer mu.Unlock()
			return sysstat.Sample{CPUBusy: busy, CPUTotal: total, CPUs: 4, Load: [3]float64{0.5, 0.4, 0.3},
				MemTotal: 16 * gb, MemUsed: 4 * gb, SwapTotal: 2 * gb, SwapUsed: gb / 2,
				Uptime: 26 * time.Hour,
				Disks:  []sysstat.Disk{{Path: "/", Total: 100 * gb, Used: 95 * gb}, {Path: "/var/lib/docker", Total: 200 * gb, Used: 20 * gb}}}, nil
		},
	})
	m := open(t, c)
	waitFor(t, m, "Pronto.")
	run(t, m, m.sysCmd())
	waitFor(t, m, "Processador …")
	for i := range 5 {
		mu.Lock()
		now, busy, total = now.Add(2*time.Second), busy+uint64(10*(i+1)), total+100
		mu.Unlock()
		run(t, m, m.sysCmd())
	}
	for _, want := range []string{"Processador 50%", "4 nucleos", "Memoria 25%", "4.0 GB de 16.0 GB",
		"ligada ha 1d 2h", "carga 0.50 0.40 0.30", "/var/lib/docker", " 95%  95.0 GB de 100.0 GB", "swap", "ultimos 10s",
		"100%┤"} {
		if !strings.Contains(screen(m), want) {
			t.Fatalf("faltou %q no dashboard:\n%s", want, screen(m))
		}
	}
	// o ambiente fica embaixo: rolando
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	if !strings.Contains(screen(m), "Verificar novamente") {
		t.Fatalf("faltou o Verificar novamente no fim:\n%s", screen(m))
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyHome})

	// fora do dashboard o polling para: o tick velho nao pede mais nada
	old := m.sysGeneration
	m.setTab(tabApps)
	if _, cmd := m.Update(sysTick(old)); cmd != nil {
		t.Fatal("tick do dashboard continuou fora dele")
	}
	if cmd := m.onSysStats(sysStatsMsg{generation: m.sysGeneration, stats: core.SystemStats{}}); cmd != nil {
		t.Fatal("agendou leitura fora do dashboard")
	}
}

// as requisicoes do proxy: a taxa, os mais acessados e o erro sem metricas
func TestDashboardRequisicoes(t *testing.T) {
	c, _, _ := testCore(t, okChecker)
	m := open(t, c)
	m.traffic = core.Traffic{At: time.Now(), Rate: 23.4, Errors: 0.3, History: []float64{10, 20, 23.4}, Total: 1234567,
		Services: []core.ServiceTraffic{{Name: "loja", Rate: 15.2, Errors: 0.3}, {Name: "blog", Rate: 6.1}}}
	s := screen(m)
	for _, want := range []string{"Requisicoes no proxy", "total 1.234.567", "Agora 23/s", "erros 5xx 1,3%", "loja", "15/s", "5xx 0,3/s", "6,1/s"} {
		if !strings.Contains(s, want) {
			t.Errorf("falta %q:\n%s", want, s)
		}
	}
	m.traffic = core.Traffic{At: time.Now(), Rate: -1, Error: "o proxy nao esta no ar"}
	if s := screen(m); !strings.Contains(s, "Sem as requisicoes: o proxy nao esta no ar") {
		t.Errorf("sem proxy:\n%s", s)
	}
}

// o que nao cabe no dashboard rola: setas, end e a roda do mouse
func TestDashboardRola(t *testing.T) {
	c, _, _ := testCore(t, okChecker)
	m := open(t, c)
	m.traffic = core.Traffic{At: time.Now(), Rate: 1, History: []float64{1, 2}, Total: 10}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 22})
	s := screen(m)
	if !strings.Contains(s, "↓ mais abaixo") || strings.Contains(s, "Ambiente") {
		t.Fatalf("terminal baixo:\n%s", s)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	if s := screen(m); !strings.Contains(s, "Ambiente") || !strings.Contains(s, "↑ mais acima") || strings.Contains(s, "↓ mais abaixo") {
		t.Fatalf("end:\n%s", s)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	if m.dashScroll != 0 {
		t.Fatalf("home: %d", m.dashScroll)
	}
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if !strings.Contains(screen(m), "↑ mais acima") || m.dashScroll == 0 {
		t.Fatalf("roda: %d", m.dashScroll)
	}
	before := m.dashScroll // o desenho ja limitou ao fim
	m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.dashScroll != before-1 {
		t.Fatalf("seta pra cima: %d -> %d", before, m.dashScroll)
	}
}
