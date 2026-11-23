package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/trdSystemDev/cais/internal/sysstat"
)

func TestSystemStatsHistorico(t *testing.T) {
	now := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	busy, total := uint64(0), uint64(0)
	var readErr error
	c := New(Deps{
		Now: func() time.Time { return now },
		ReadSystem: func() (sysstat.Sample, error) {
			return sysstat.Sample{CPUBusy: busy, CPUTotal: total, MemTotal: 100, MemUsed: 40}, readErr
		},
	})
	ctx := context.Background()

	st, _ := c.SystemStats(ctx)
	if st.CPU != -1 || len(st.CPUHistory) != 0 || len(st.MemHistory) != 1 {
		t.Fatalf("primeira leitura: %+v", st)
	}
	// 2s depois: 50 de 200 ocupados
	now, busy, total = now.Add(2*time.Second), 50, 200
	st, _ = c.SystemStats(ctx)
	if st.CPU != 25 || len(st.CPUHistory) != 1 || st.MemHistory[1] != 40 {
		t.Fatalf("segunda leitura: %+v", st)
	}
	// logo em seguida: a mesma leitura, sem ler de novo
	busy, total = 1000, 1000
	if again, _ := c.SystemStats(ctx); again.CPU != 25 || len(again.MemHistory) != 2 {
		t.Fatalf("leu de novo antes do intervalo: %+v", again)
	}
	// muito tempo sem ninguem olhar: o historico recomeca
	now = now.Add(time.Minute)
	st, _ = c.SystemStats(ctx)
	if st.CPU != -1 || len(st.MemHistory) != 1 {
		t.Fatalf("depois de um buraco: %+v", st)
	}
	// o historico tem limite
	for range SystemHistory + 10 {
		now, busy, total = now.Add(2*time.Second), busy+10, total+20
		st, _ = c.SystemStats(ctx)
	}
	if len(st.CPUHistory) != SystemHistory || len(st.MemHistory) != SystemHistory || st.CPU != 50 {
		t.Fatalf("limite: cpu %d, mem %d, %v", len(st.CPUHistory), len(st.MemHistory), st.CPU)
	}
	// erro de leitura vira texto, nao erro da chamada
	now, readErr = now.Add(2*time.Second), errors.New("sem /proc")
	if st, err := c.SystemStats(ctx); err != nil || st.Error != "sem /proc" {
		t.Fatalf("erro: %+v %v", st, err)
	}
}
