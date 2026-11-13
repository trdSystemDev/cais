package core

import (
	"context"
	"log"
	"time"
)

// RunMaintenance faz a limpeza do disco a cada every (a primeira depois de
// first): o cache de build e o que mais cresce com deploy e nada o apaga
// sozinho. Disco cheio derruba tudo de uma vez, bancos inclusive. Para
// quando ctx acaba.
func (c *Core) RunMaintenance(ctx context.Context, first, every time.Duration) {
	wait := first
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		c.MaintenanceOnce(ctx)
		wait = every
	}
}

// MaintenanceOnce faz uma rodada da limpeza.
func (c *Core) MaintenanceOnce(ctx context.Context) {
	if err := c.deps.PruneBuildCache(ctx); err != nil {
		log.Printf("limpeza do cache de build: %v", err)
	}
}
