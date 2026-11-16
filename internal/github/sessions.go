package github

import (
	"context"
	"strconv"
	"sync"
	"time"
)

// renewBefore e quanto antes de vencer o token a sessao e trocada: uma
// chamada comecada com o token no fim nao pode cair no meio.
const renewBefore = 5 * time.Minute

// Sessions guarda uma sessao por repositorio enquanto o token vale (uma
// hora). Sem isso cada consulta gastaria 3 chamadas a mais (instalacao,
// token e branch padrao): com dezenas de apps no deploy automatico, a
// consulta a cada minuto chegaria perto do limite da API. O token continua
// restrito a um repositorio. O zero vale; pode ser usado por varias
// goroutines.
type Sessions struct {
	// Limits e o estado das cotas, comum a todas as sessoes.
	Limits Limits

	mu sync.Mutex
	m  map[string]*Session
}

func sessionKey(a App, r Repo) string {
	return a.base() + "|" + strconv.FormatInt(a.ID, 10) + "|" + r.String()
}

// Open devolve a sessao guardada do repositorio, ou abre uma nova se nao
// tem ou se o token esta pra vencer.
func (c *Sessions) Open(ctx context.Context, a App, r Repo) (*Session, error) {
	a.Limits = &c.Limits
	key := sessionKey(a, r)
	c.mu.Lock()
	old := c.m[key]
	c.mu.Unlock()
	if old != nil && a.now().Before(old.Expires.Add(-renewBefore)) {
		return old, nil
	}
	s, err := a.Open(ctx, r)
	if err != nil {
		return nil, err
	}
	if old != nil {
		// os ETags continuam valendo com o token novo
		old.mu.Lock()
		s.etags = make(map[string]etagged, len(old.etags))
		for k, v := range old.etags {
			s.etags[k] = v
		}
		old.mu.Unlock()
	}
	c.mu.Lock()
	if c.m == nil {
		c.m = map[string]*Session{}
	}
	c.m[key] = s
	c.mu.Unlock()
	return s, nil
}

// Forget descarta a sessao do repositorio (token recusado: o app foi
// reinstalado ou a chave trocada); a proxima chamada abre outra.
func (c *Sessions) Forget(a App, r Repo) {
	c.mu.Lock()
	delete(c.m, sessionKey(a, r))
	c.mu.Unlock()
}
