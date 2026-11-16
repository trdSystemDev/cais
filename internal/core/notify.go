package core

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/notify"
	"github.com/trdSystemDev/cais/internal/secrets"
)

// secretBrevoKey e a chave da API da Brevo, informada pela pessoa.
const secretBrevoKey = "brevo_api_key"

// notifyTimeout e quanto um envio pode levar: a Brevo fora do ar nao
// segura o desligamento do daemon.
const notifyTimeout = 30 * time.Second

// mail monta o e-mail pros destinatarios da config. ok falso: avisos
// desligados (sem destinatarios), o que nao e erro.
func (c *Core) mail(subject, text string) (m notify.Mail, key string, ok bool, err error) {
	cfg, err := config.Load(c.deps.ConfigPath)
	if err != nil {
		return m, "", false, err
	}
	if !cfg.Notify.On() {
		return m, "", false, nil
	}
	k, err := c.secretStore().Get(secretBrevoKey)
	if errors.Is(err, secrets.ErrNotFound) {
		return m, "", false, errors.New("falta a chave da API da Brevo: cais setup secret " + secretBrevoKey)
	}
	if err != nil {
		return m, "", false, err
	}
	m = notify.Mail{
		From:    cfg.Notify.From,
		To:      cfg.Notify.To,
		Subject: "[cais " + cfg.Domain + "] " + subject,
		Text:    text + "\n\n-- \ncais em " + cfg.Domain + "\n",
	}
	return m, strings.TrimSpace(string(k)), true, nil
}

// notify manda um aviso em segundo plano. Falha no envio so vai pro log:
// o aviso nunca atrapalha o que falhou de verdade.
func (c *Core) notify(subject, text string) {
	m, key, ok, err := c.mail(subject, text)
	if err != nil {
		log.Printf("aviso por e-mail (%s): %v", subject, err)
		return
	}
	if !ok || !c.work.Begin() {
		return
	}
	go func() {
		defer c.work.End()
		ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
		defer cancel()
		if err := notify.Send(ctx, c.deps.BrevoURL, key, m); err != nil {
			log.Printf("aviso por e-mail (%s): %v", subject, err)
		}
	}()
}

// TestNotify manda um e-mail de teste agora e devolve o erro da Brevo,
// se houver.
func (c *Core) TestNotify(ctx context.Context) error {
	m, key, ok, err := c.mail("e-mail de teste", "Os avisos do cais estao chegando.\n\n"+
		"Voce vai receber um e-mail quando um deploy automatico falhar ou quando o backup nao der certo.")
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("avisos desligados: informe os destinatarios (cais setup config --notify-to)")
	}
	ctx, cancel := context.WithTimeout(ctx, notifyTimeout)
	defer cancel()
	return notify.Send(ctx, c.deps.BrevoURL, key, m)
}

// notifyAutoDeploy avisa que o deploy automatico de um app falhou de vez
// (sem nova tentativa marcada).
func (c *Core) notifyAutoDeploy(app, commit string, err error) {
	c.notify("deploy automatico de "+app+" falhou",
		fmt.Sprintf("O deploy automatico de %s (commit %s) falhou:\n\n%v\n\n"+
			"Quando o deploy falha, o cais volta a versao anterior. Os detalhes e os logs estao na aba Apps.\n"+
			"O mesmo commit nao e tentado de novo: o proximo deploy automatico sai com um commit novo.",
			app, short(commit), err))
}

// notifyBackup avisa que o backup automatico nao deu certo. Backup que nao
// chegou no Drive avisa so na primeira falha da sequencia: o cais tenta de
// novo a cada hora e cada tentativa seria um e-mail. Backup sem algum
// database nao repete no dia, entao avisa toda vez.
func (c *Core) notifyBackup(prev *BackupRun, run BackupRun, err error) {
	var b strings.Builder
	switch {
	case err != nil && prev != nil && prev.Error != "":
		return
	case err != nil:
		fmt.Fprintf(&b, "O backup automatico nao chegou no Drive:\n\n%v\n", err)
		if run.Local != "" {
			fmt.Fprintf(&b, "\nO arquivo ficou na VPS (%s).", run.Local)
		}
		b.WriteString("\nO cais tenta de novo a cada hora. Novo aviso so depois de um backup que der certo.")
	case len(run.Errors) > 0:
		fmt.Fprintf(&b, "O backup automatico chegou no Drive, mas sem estes databases:\n\n- %s\n",
			strings.Join(run.Errors, "\n- "))
	default:
		return
	}
	b.WriteString("\nDetalhes na aba Backup.")
	c.notify("backup falhou", b.String())
}
