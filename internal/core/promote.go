package core

import (
	"context"
	"fmt"
	"time"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/github"
)

func (g githubSource) Compare(ctx context.Context, repo, base, head string) (out apps.Comparison, err error) {
	err = g.with(ctx, repo, func(s *github.Session) error {
		c, err := s.Compare(ctx, base, head)
		out = apps.Comparison{Status: c.Status, AheadBy: c.AheadBy, BehindBy: c.BehindBy}
		return err
	})
	return out, err
}

func (g githubSource) Tree(ctx context.Context, repo, commit string) (tree string, err error) {
	err = g.with(ctx, repo, func(s *github.Session) (err error) {
		tree, err = s.Tree(ctx, commit)
		return err
	})
	return tree, err
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
	res, err := c.logDeploy("promocao", h.PromoteTo, func() (apps.DeployResult, error) {
		return m.Promote(ctx, from, confirm)
	}, "de", from)
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
			if ctx.Err() == nil {
				c.AutoDeployOnce(ctx)
			}
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
	// com a cota da API do GitHub no fim, a rodada espera: a reserva fica
	// pros deploys manuais (o automatico so atrasa)
	if q, low := c.gh.Limits.Low(c.deps.Now()); low {
		c.autoLog(autoQuota, fmt.Errorf("pausado: restam %d de %d requisicoes da API do GitHub (cota %s) ate %s; a reserva fica pros deploys manuais",
			q.Remaining, q.Limit, q.Name, q.Reset.Local().Format("15:04")))
		close(finished)
		return nil, finished
	}
	c.autoLog(autoQuota, nil)
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
		if err != nil || head == a.Commit || a.Tried(head, c.deps.Now()) || head == a.AutoHold {
			continue
		}
		// daemon desligando: o commit fica pra quando ele voltar
		if !c.work.Begin() {
			break
		}
		unlock, err := c.lockApp(a.Name)
		if err != nil {
			c.work.End()
			continue // ocupado (deploy manual em andamento): tenta na proxima rodada
		}
		started = append(started, a.Name)
		pending++
		go func(name string) {
			defer func() { unlock(); c.work.End(); results <- struct{}{} }()
			// o deploy nao ve o desligamento do daemon: o Close espera
			ctx := context.WithoutCancel(ctx)
			c.setDeploying(name, true)
			_, err := c.logDeploy("deploy_automatico", name, func() (apps.DeployResult, error) {
				return m.AutoDeploy(ctx, name, head)
			}, "commit_novo", short(head))
			c.setDeploying(name, false)
			if err != nil {
				retry := false
				if a, gerr := m.Get(name); gerr == nil {
					if at, ok := a.AutoRetryAt(); ok {
						retry = true
						c.deps.Logger.Info("deploy automatico tenta de novo (falha parece passageira)",
							"acao", "deploy_automatico", "app", name, "as", at.Local().Format("15:04"))
					}
				}
				// falha passageira com nova tentativa marcada ainda nao e aviso
				if !retry {
					c.notifyAutoDeploy(name, head, err)
				}
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
// autoQuota e a chave do autoLog pra pausa por limite da API.
const autoQuota = "limite do GitHub"

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
		c.deps.Logger.Warn("consulta do deploy automatico falhou", "acao", "deploy_automatico", "app", app, "erro", msg)
	}
}

func short(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}
