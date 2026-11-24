package apps

import "context"

// BuildQueue e a fila global de build: um build por vez na VPS, na ordem de
// chegada. Builds de apps diferentes juntos disputam memoria e CPU com os
// apps no ar, e numa VPS pequena o OOM killer acaba derrubando container de
// producao. So o build passa pela fila: download, troca e up nao.
type BuildQueue struct {
	slot chan struct{}
	// Waiting e avisado quando um app entra (true) e sai (false) da espera
	// por um build que ja esta rodando.
	Waiting func(app string, on bool)
}

// NewBuildQueue cria a fila com n builds ao mesmo tempo (minimo 1).
func NewBuildQueue(n int) *BuildQueue {
	return &BuildQueue{slot: make(chan struct{}, max(1, n))}
}

// acquire espera a vez do app e devolve a funcao que libera. Fila nil nao
// espera. pause e resume param e retomam o relogio do limite do deploy
// enquanto ele espera (a espera nao conta no Timeout).
func (q *BuildQueue) acquire(ctx context.Context, app string, pause, resume func()) (func(), error) {
	if q == nil {
		return func() {}, nil
	}
	release := func() { <-q.slot }
	select {
	case q.slot <- struct{}{}:
		return release, nil
	default:
	}
	if q.Waiting != nil {
		q.Waiting(app, true)
		defer q.Waiting(app, false)
	}
	pause()
	defer resume()
	select {
	case q.slot <- struct{}{}:
		return release, nil
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}
