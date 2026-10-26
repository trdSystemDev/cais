package core

import (
	"context"
	"sync"
	"time"

	"github.com/trdSystemDev/vpsctl/internal/docker"
)

// Deps sao as dependencias externas do Core. Campos vazios recebem a
// implementacao real; os testes trocam por fakes.
type Deps struct {
	CheckDocker func(context.Context) docker.Status
	Now         func() time.Time
}

// Core guarda o estado e executa as acoes. E seguro pra uso concorrente:
// a TUI e o MCP podem chamar acoes ao mesmo tempo.
type Core struct {
	deps Deps

	mu      sync.Mutex
	state   State
	subs    map[int]chan State
	nextSub int
	checks  int // checagens de ambiente em andamento
}

// New cria o Core. Nenhuma acao roda aqui: quem decide quando checar o
// ambiente e a interface (ou o cliente MCP).
func New(d Deps) *Core {
	if d.CheckDocker == nil {
		d.CheckDocker = docker.Check
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Core{deps: d, subs: map[int]chan State{}}
}

// Snapshot devolve a foto atual do estado.
func (c *Core) Snapshot() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Subscribe devolve um canal que recebe o estado atual na hora e depois
// cada mudanca. Se o leitor atrasar, fotos intermediarias sao descartadas
// e ele recebe so a mais recente -- interface reativa so precisa do ultimo
// estado, e um leitor lento nunca trava o Core. A funcao devolvida cancela
// a inscricao e fecha o canal; pode ser chamada mais de uma vez.
func (c *Core) Subscribe() (<-chan State, func()) {
	ch := make(chan State, 1)

	c.mu.Lock()
	id := c.nextSub
	c.nextSub++
	c.subs[id] = ch
	ch <- c.state
	c.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			c.mu.Lock()
			delete(c.subs, id)
			close(ch)
			c.mu.Unlock()
		})
	}
}

// update aplica fn no estado, publica a nova foto pra todos os inscritos e
// a devolve. fn roda com o lock, entao tambem pode mexer em campos internos
// do Core (ex: contadores) de forma atomica com o estado.
func (c *Core) update(fn func(*State)) State {
	c.mu.Lock()
	defer c.mu.Unlock()

	fn(&c.state)
	c.state.Version++
	s := c.state

	for _, ch := range c.subs {
		// so o Core envia nesses canais, e sempre com o lock: depois de
		// esvaziar, o envio seguinte nunca bloqueia
		select {
		case <-ch:
		default:
		}
		ch <- s
	}
	return s
}
