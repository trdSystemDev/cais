package core

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/trdSystemDev/cais/internal/apps"
)

func (g githubSource) Compare(ctx context.Context, repo, base, head string) (apps.Comparison, error) {
	s, err := g.open(ctx, repo)
	if err != nil {
		return apps.Comparison{}, err
	}
	c, err := s.Compare(ctx, base, head)
	return apps.Comparison{Status: c.Status, AheadBy: c.AheadBy, BehindBy: c.BehindBy}, err
}

func (g githubSource) Tree(ctx context.Context, repo, commit string) (string, error) {
	s, err := g.open(ctx, repo)
	if err != nil {
		return "", err
	}
	return s.Tree(ctx, commit)
}

// CheckPromotion diz se o commit no ar na homologacao pode ir pra producao
// (e por que nao, quando nao pode). Nao muda nada.
func (c *Core) CheckPromotion(ctx context.Context, from string) (apps.Promotion, error) {
	return c.appManager().CheckPromotion(ctx, from)
}

// PromoteApp sobe na producao o commit validado na homologacao, pelo deploy
// normal da producao. So a producao fica travada: a homologacao segue no ar.
func (c *Core) PromoteApp(ctx context.Context, from string, confirm bool) (apps.DeployResult, error) {
	m := c.appManager()
	h, err := m.Get(from)
	if err != nil {
		return apps.DeployResult{}, err
	}
	if h.PromoteTo == "" {
		return apps.DeployResult{}, fmt.Errorf("%s nao e homologacao de nenhum app", from)
	}
	unlock, err := c.lockApp(h.PromoteTo)
	if err != nil {
		return apps.DeployResult{}, err
	}
	defer unlock()

	c.setDeploying(h.PromoteTo, true)
	res, err := m.Promote(ctx, from, confirm)
	c.setDeploying(h.PromoteTo, false)
	c.CheckApps(ctx)
	return res, err
}

// RunAutoDeploy consulta o GitHub a cada intervalo e faz o deploy dos apps
// com deploy automatico cuja branch ganhou um commit novo. E consulta (e
// nao webhook) pra o cais nao abrir porta. Para quando ctx acaba.
func (c *Core) RunAutoDeploy(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.AutoDeployOnce(ctx)
		}
	}
}

// AutoDeployOnce faz uma rodada do deploy automatico e devolve os apps em
// que um deploy comecou (os deploys seguem em goroutines; done fecha quando
// todos terminam). Commit que ja foi tentado (deu certo ou nao) nao e
// tentado de novo: um commit quebrado nao vira um deploy por minuto.
func (c *Core) AutoDeployOnce(ctx context.Context) (started []string, done <-chan struct{}) {
	m := c.appManager()
	list, err := m.List()
	finished := make(chan struct{})
	if err != nil {
		c.autoLog("", err)
		close(finished)
		return nil, finished
	}
	pending := 0
	results := make(chan struct{})
	for _, a := range list {
		if !a.AutoDeploy {
			continue
		}
		head, err := m.Source.Resolve(ctx, a.Repo, a.Branch)
		c.autoLog(a.Name, err)
		// depois de um rollback, a ponta de entao nao sobe (desfaria o
		// rollback): so um commit novo
		if err != nil || head == a.Commit || (a.Last != nil && a.Last.Commit == head) || head == a.AutoHold {
			continue
		}
		unlock, err := c.lockApp(a.Name)
		if err != nil {
			continue // ocupado (deploy manual em andamento): tenta na proxima rodada
		}
		started = append(started, a.Name)
		pending++
		go func(name string) {
			defer func() { unlock(); results <- struct{}{} }()
			log.Printf("deploy automatico de %s: commit novo %s", name, short(head))
			c.setDeploying(name, true)
			_, err := m.AutoDeploy(ctx, name, head)
			c.setDeploying(name, false)
			if err != nil {
				log.Printf("deploy automatico de %s falhou: %v", name, err)
			}
			c.CheckApps(ctx)
		}(a.Name)
	}
	go func() {
		for range pending {
			<-results
		}
		close(finished)
	}()
	return started, finished
}

// autoLog registra erro de consulta do deploy automatico so quando muda
// (sem GitHub App configurado, seria uma linha por minuto pra sempre).
func (c *Core) autoLog(app string, err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	c.mu.Lock()
	if c.autoErrs == nil {
		c.autoErrs = map[string]string{}
	}
	changed := c.autoErrs[app] != msg
	c.autoErrs[app] = msg
	c.mu.Unlock()
	if changed && msg != "" {
		if app == "" {
			app = "apps"
		}
		log.Printf("deploy automatico (%s): %s", app, msg)
	}
}

func short(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}
