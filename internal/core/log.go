package core

import (
	"time"

	"github.com/trdSystemDev/cais/internal/apps"
)

// Logs do daemon (log/slog). Os campos sao fixos pra filtrar no journal:
//
//	acao     deploy, deploy_automatico, rollback, promocao, recuperacao,
//	         backup, restore, aviso, limpeza, desligamento
//	app      o app (deploys)
//	commit   o commit no ar depois da acao (curto)
//	duracao  quanto levou
//	erro     a falha
//
// Nenhum valor de secret, senha ou chave vai pra log: os campos sao nomes,
// commits e contagens.

// logDeploy registra o comeco e o fim de um deploy de qualquer tipo
// (acao) e devolve o que run devolveu.
func (c *Core) logDeploy(acao, app string, run func() (apps.DeployResult, error), attrs ...any) (apps.DeployResult, error) {
	l := c.deps.Logger.With("acao", acao, "app", app)
	l.Info("deploy comecou", attrs...)
	start := time.Now()
	res, err := run()
	fields := []any{"commit", short(res.Commit), "duracao", time.Since(start).Round(time.Millisecond)}
	if err != nil {
		l.Error("deploy falhou", append(fields, "erro", err)...)
	} else {
		l.Info("deploy terminou", fields...)
	}
	return res, err
}
