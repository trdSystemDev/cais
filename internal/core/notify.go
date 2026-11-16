package core

import (
	"context"
	"errors"
	"log"
	"strconv"
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
func (c *Core) mail(msg notify.Message) (m notify.Mail, key string, ok bool, err error) {
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
	msg.Server = c.vpsName()
	m = notify.Mail{
		From:    cfg.Notify.From,
		To:      cfg.Notify.To,
		Subject: "[cais " + msg.Server + "] " + msg.Title,
		Text:    msg.Text(),
		HTML:    msg.HTML(),
	}
	return m, strings.TrimSpace(string(k)), true, nil
}

// notify manda um aviso em segundo plano. Falha no envio so vai pro log:
// o aviso nunca atrapalha o que falhou de verdade.
func (c *Core) notify(msg notify.Message) {
	m, key, ok, err := c.mail(msg)
	if err != nil {
		log.Printf("aviso por e-mail (%s): %v", msg.Title, err)
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
			log.Printf("aviso por e-mail (%s): %v", msg.Title, err)
		}
	}()
}

// when e o horario no fuso da VPS, do jeito que aparece no aviso.
func (c *Core) when() string {
	return c.deps.Now().Local().Format("02/01/2006 15:04")
}

// TestNotify manda um e-mail de teste agora e devolve o erro da Brevo,
// se houver.
func (c *Core) TestNotify(ctx context.Context) error {
	m, key, ok, err := c.mail(notify.Message{
		Kind:  notify.KindTest,
		Title: "Os avisos do cais estao chegando",
		Intro: "Este e um e-mail de teste. Daqui pra frente voce recebe um aviso quando um deploy automatico falhar ou quando o backup nao der certo.",
		Facts: []notify.Fact{{Label: "Enviado em", Value: c.when()}},
		Next:  "Se este e-mail caiu no lixo eletronico, marque como \"nao e lixo\" pra os proximos chegarem na caixa de entrada.",
	})
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
	c.notify(notify.Message{
		Kind:  notify.KindFailure,
		Title: "Deploy automatico de " + app + " falhou",
		Intro: "O commit novo nao subiu. Quando o deploy falha, o cais volta a versao anterior, entao o app continua no ar com ela.",
		Facts: []notify.Fact{
			{Label: "App", Value: app},
			{Label: "Commit", Value: short(commit)},
			{Label: "Quando", Value: c.when()},
		},
		Detail: err.Error(),
		Next:   "O mesmo commit nao e tentado de novo: o proximo deploy automatico sai com um commit novo. Os detalhes e os logs estao na aba Apps.",
	})
}

// notifyBackup avisa que o backup automatico nao deu certo. Backup que nao
// chegou no Drive avisa so na primeira falha da sequencia: o cais tenta de
// novo a cada hora e cada tentativa seria um e-mail. Backup sem algum
// database nao repete no dia, entao avisa toda vez.
func (c *Core) notifyBackup(prev *BackupRun, run BackupRun, err error) {
	msg := notify.Message{
		Kind:  notify.KindFailure,
		Title: "Backup falhou",
		Facts: []notify.Fact{{Label: "Quando", Value: c.when()}},
	}
	switch {
	case err != nil && prev != nil && prev.Error != "":
		return
	case err != nil:
		msg.Intro = "O backup automatico nao chegou no Google Drive."
		msg.Detail = err.Error()
		if run.Local != "" {
			msg.Facts = append(msg.Facts, notify.Fact{Label: "Ficou na VPS", Value: run.Local})
		}
		msg.Next = "O cais tenta de novo a cada hora. Voce so recebe outro aviso depois de um backup que der certo. Detalhes na aba Backup."
	case len(run.Errors) > 0:
		msg.Title = "Backup incompleto"
		msg.Intro = "O backup automatico chegou no Google Drive, mas sem " + plural(len(run.Errors), "este database", "estes databases") + ":"
		msg.Items = run.Errors
		msg.Facts = append(msg.Facts, notify.Fact{Label: "No backup", Value: strconv.Itoa(run.Databases) + " databases"})
		msg.Next = "Um backup incompleto nao e repetido no mesmo dia. Detalhes na aba Backup."
	default:
		return
	}
	c.notify(msg)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
