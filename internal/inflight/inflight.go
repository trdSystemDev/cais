// Package inflight conta o trabalho em andamento pra o daemon desligar sem
// cortar nada no meio: depois do Close nada novo comeca, e o Close so volta
// quando o que ja estava rodando termina.
package inflight

import "sync"

// Group conta o trabalho em andamento. O valor zero esta pronto pra uso.
type Group struct {
	mu     sync.Mutex
	cond   *sync.Cond
	n      int
	closed bool
}

// Begin registra um trabalho que vai comecar. Falso depois do Close: quem
// chamou nao deve comecar.
func (g *Group) Begin() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return false
	}
	g.n++
	return true
}

// End marca o fim de um trabalho que o Begin aceitou.
func (g *Group) End() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n--
	if g.n == 0 && g.cond != nil {
		g.cond.Broadcast()
	}
}

// Count e quantos trabalhos estao em andamento.
func (g *Group) Count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.n
}

// Close recusa trabalho novo e espera o que esta em andamento terminar.
// Pode ser chamado mais de uma vez.
func (g *Group) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	if g.cond == nil {
		g.cond = sync.NewCond(&g.mu)
	}
	for g.n > 0 {
		g.cond.Wait()
	}
}
