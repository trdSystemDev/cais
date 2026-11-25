package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/trdSystemDev/cais/internal/core"
)

// updateWait e quanto o cais update espera o daemon voltar na versao nova;
// updatePoll, de quanto em quanto tempo pergunta (trocados nos testes).
var (
	updateWait = 10 * time.Minute
	updatePoll = 2 * time.Second
)

// runUpdate pede ao daemon a atualizacao (o install.sh do release, como
// root, numa unidade do systemd) e espera ele voltar na versao nova. Pelo
// daemon, funciona sem sudo: ssh cais@vps update.
func (e *cli) runUpdate(args []string) error {
	fs, socket := e.newFlags("update")
	version := fs.String("version", "", "tag do release (padrao: o mais novo); serve pra voltar de versao")
	if err := e.parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New("uso: cais update [-version v0.5.3]")
	}
	c, err := e.connect(*socket)
	if err != nil {
		return err
	}
	r, err := c.UpdateCais(context.Background(), *version)
	c.Close()
	if err != nil {
		return err
	}
	if !r.Started {
		fmt.Fprintf(e.out, "o cais ja esta na %s\n", r.To)
		return nil
	}
	fmt.Fprintf(e.out, "Atualizando de %s pra %s: os apps e os bancos continuam no ar, so o daemon reinicia.\n", r.From, r.To)
	deadline := time.Now().Add(updateWait)
	for time.Now().Before(deadline) {
		time.Sleep(updatePoll)
		if v := e.daemonVersion(*socket); v == r.To {
			fmt.Fprintf(e.out, "Pronto: o cais esta na %s.\n", r.To)
			return nil
		}
	}
	return fmt.Errorf("o daemon nao voltou na %s em %s (um deploy ou backup em andamento segura o reinicio); acompanhe com: sudo journalctl -u %s -u cais",
		r.To, updateWait, core.UpdateUnit)
}

// daemonVersion e a versao do daemon no ar (vazia: fora do ar, reiniciando).
func (e *cli) daemonVersion(socket string) string {
	c, err := e.connect(socket)
	if err != nil {
		return ""
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h, err := c.CheckHealth(ctx)
	if err != nil {
		return ""
	}
	return h.Version
}
