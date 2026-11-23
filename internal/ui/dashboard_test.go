package ui

import (
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
	c := core.New(core.Deps{
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
		"100%┤", "Verificar novamente"} {
		if !strings.Contains(screen(m), want) {
			t.Fatalf("faltou %q no dashboard:\n%s", want, screen(m))
		}
	}

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
