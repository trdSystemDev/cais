package core

import (
	"context"
	"sync"
	"time"

	"github.com/trdSystemDev/cais/internal/sysstat"
)

// SystemHistory e quantas leituras o historico do uso da maquina guarda.
const SystemHistory = 90

// systemMinGap e o intervalo minimo entre leituras: quem pede antes disso
// recebe a ultima (duas telas abertas nao dobram as leituras).
const systemMinGap = 1500 * time.Millisecond

// systemMaxGap: depois de tanto tempo sem ninguem olhar, o historico
// recomeca (um buraco no meio do grafico pareceria uso constante).
const systemMaxGap = 30 * time.Second

// SystemStats e o uso da maquina onde o cais roda: a leitura atual e o
// historico recente do processador e da memoria (em %, do mais velho pro
// mais novo), pros graficos.
type SystemStats struct {
	At  time.Time      `json:"at,omitzero"`
	Now sysstat.Sample `json:"now"`
	// CPU e o uso atual do processador (-1 na primeira leitura: precisa de
	// duas pra calcular).
	CPU        float64   `json:"cpu"`
	CPUHistory []float64 `json:"cpu_history,omitempty"`
	MemHistory []float64 `json:"mem_history,omitempty"`
	Error      string    `json:"error,omitempty"`
}

type systemRuntime struct {
	mu    sync.Mutex
	last  SystemStats
	prev  sysstat.Sample
	valid bool // prev e uma leitura de verdade
}

// SystemStats le o uso da maquina e devolve com o historico. O historico
// cresce enquanto alguem pede (a tela do dashboard pede a cada 2s); nao ha
// leitura de fundo.
func (c *Core) SystemStats(context.Context) (SystemStats, error) {
	r := &c.sys
	r.mu.Lock()
	defer r.mu.Unlock()
	now := c.deps.Now()
	if r.valid && now.Sub(r.last.At) < systemMinGap {
		return r.copy(), nil
	}
	s, err := c.deps.ReadSystem()
	if err != nil {
		r.last, r.valid = SystemStats{At: now, CPU: -1, Error: err.Error()}, false
		return r.copy(), nil
	}
	cpu := -1.0
	if r.valid && now.Sub(r.last.At) < systemMaxGap {
		cpu = sysstat.CPUPercent(r.prev, s)
	} else {
		r.last.CPUHistory, r.last.MemHistory = nil, nil
	}
	cpuH, memH := r.last.CPUHistory, r.last.MemHistory
	if cpu >= 0 {
		cpuH = appendCapped(cpuH, cpu)
	}
	memH = appendCapped(memH, s.MemPercent())
	r.last = SystemStats{At: now, Now: s, CPU: cpu, CPUHistory: cpuH, MemHistory: memH}
	r.prev, r.valid = s, true
	return r.copy(), nil
}

// copy devolve a leitura com historicos proprios (o publicado nao muda).
func (r *systemRuntime) copy() SystemStats {
	out := r.last
	out.CPUHistory = append([]float64(nil), r.last.CPUHistory...)
	out.MemHistory = append([]float64(nil), r.last.MemHistory...)
	out.Now.Disks = append([]sysstat.Disk(nil), r.last.Now.Disks...)
	return out
}

func appendCapped(h []float64, v float64) []float64 {
	h = append(h, v)
	if len(h) > SystemHistory {
		h = append([]float64(nil), h[len(h)-SystemHistory:]...)
	}
	return h
}
