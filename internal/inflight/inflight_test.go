package inflight

import (
	"testing"
	"time"
)

func TestCloseEsperaOQueEstaRodandoERecusaONovo(t *testing.T) {
	var g Group
	if !g.Begin() {
		t.Fatal("Begin recusou antes do Close")
	}
	closed := make(chan struct{})
	go func() { g.Close(); close(closed) }()

	select {
	case <-closed:
		t.Fatal("Close voltou com trabalho em andamento")
	case <-time.After(50 * time.Millisecond):
	}
	// trabalho novo durante o desligamento e recusado
	for g.Begin() {
		g.End()
		time.Sleep(time.Millisecond)
	}
	if g.Count() != 1 {
		t.Fatalf("em andamento: %d", g.Count())
	}
	g.End()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close nao voltou depois do fim do trabalho")
	}
	g.Close() // de novo: volta na hora
}
