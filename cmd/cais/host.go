package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"

	"github.com/trdSystemDev/cais/internal/host"
)

// hostSystem e a maquina que o cais host confere e prepara (trocada nos
// testes).
var hostSystem = host.Local

// runHost e o cais host: roda direto na VPS, como root, sem o daemon (o
// harden prepara a maquina antes de ter algo do cais configurado).
func (e *cli) runHost(args []string) error {
	sub, args := subcommand(args)
	switch sub {
	case "", "check":
		return e.hostCheck(args)
	case "harden":
		return e.hostHarden(args)
	case "confirm":
		return e.hostConfirm(args)
	default:
		return groupError("host", sub)
	}
}

func (e *cli) hostCheck(args []string) error {
	fs, _ := e.newFlags("host check")
	if err := e.parse(fs, args); err != nil {
		return err
	}
	sys := hostSystem()
	if sys.Geteuid() != 0 {
		return errors.New("a checagem le a config do sshd e do ufw: rode com sudo (sudo cais host check)")
	}
	items := host.Check(context.Background(), sys)
	printHostItems(e.out, items)
	if bad := host.Failed(items); len(bad) > 0 {
		return &exitError{1, fmt.Errorf("%d de %d itens pra corrigir", len(bad), len(items))}
	}
	return nil
}

func printHostItems(out io.Writer, items []host.Item) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, it := range items {
		mark := "ok"
		if !it.OK {
			mark = "!!"
		}
		fmt.Fprintf(w, "[%s] %s\t%s\n", mark, it.Title, it.Detail)
	}
	w.Flush()
	for _, it := range host.Failed(items) {
		if it.Fix != "" {
			fmt.Fprintf(out, "\n%s:\n  %s\n", it.Title, it.Fix)
		}
	}
}

var hostStepMark = map[string]string{
	host.StepRunning: "..",
	host.StepChanged: "ok",
	host.StepOK:      "--",
	host.StepFailed:  "!!",
}

func (e *cli) hostHarden(args []string) error {
	fs, _ := e.newFlags("host harden")
	user := fs.String("user", "", "usuario admin (sudo, so chave) que substitui o root no SSH")
	keyFile := fs.String("key", "", "chave publica a mais pro admin (.pub ou authorized_keys)")
	yes := fs.Bool("yes", false, "nao pede confirmacao")
	if err := e.parse(fs, args); err != nil {
		return err
	}
	opts := host.Options{User: *user}
	if *keyFile != "" {
		b, err := os.ReadFile(*keyFile)
		if err != nil {
			return err
		}
		opts.Keys = string(b)
	}
	ctx := context.Background()
	sys := hostSystem()
	plan, err := host.Prepare(ctx, sys, opts)
	if err != nil {
		return err
	}

	fmt.Fprintf(e.out, "Preparar esta VPS (%s):\n", plan.OS)
	admin := "usar o admin " + opts.User
	if !plan.UserExists {
		admin = "criar o admin " + opts.User
	}
	for _, l := range []string{
		"atualizar o sistema; instalar ufw, fail2ban e unattended-upgrades",
		"atualizacoes de seguranca automaticas (sem reiniciar sozinho)",
		"limite nos logs dos containers",
		fmt.Sprintf("%s com sudo e %d chave(s) SSH (as do root entram)", admin, len(plan.Keys)),
		"firewall: entram so 22 (SSH), 80 e 443 (proxy)",
		"fail2ban no SSH",
		"SSH: sem login do root e sem senha, so chave",
	} {
		fmt.Fprintln(e.out, "  - "+l)
	}
	fmt.Fprintf(e.out, "\nDepois, entre como %s@<ip-da-vps> (o root deixa de entrar pelo SSH).\n", opts.User)
	if !*yes {
		fmt.Fprint(e.out, "Continuar? Digite sim: ")
		var typed string
		fmt.Fscanln(e.in, &typed)
		if strings.TrimSpace(typed) != "sim" {
			return errors.New("cancelado: nada mudou")
		}
	}
	if plan.NeedPassword {
		pw, err := e.readNewPassword(opts.User)
		if err != nil {
			return err
		}
		opts.Password = pw
	}

	fmt.Fprintln(e.out)
	res, err := host.Harden(ctx, sys, opts, func(s host.Step) {
		line := fmt.Sprintf("[%s] %s", hostStepMark[s.Status], s.Desc)
		if s.Status == host.StepOK {
			line += " -- ja estava"
		}
		if s.Detail != "" && s.Status != host.StepFailed {
			line += " -- " + s.Detail
		}
		fmt.Fprintln(e.out, line)
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(e.out, "\nPronto.")
	if res.PendingConfirm {
		fmt.Fprintf(e.out, `
IMPORTANTE: NAO feche este terminal ainda. Em outro, do seu computador:
  ssh %s@<ip-da-vps>
  sudo cais host confirm
Sem o confirm em 5 minutos, o SSH volta sozinho a aceitar o root e a senha
(se a chave nao funcionar, voce nao fica trancado fora).
`, opts.User)
	}
	fmt.Fprintln(e.out, "\nConfira tudo com: sudo cais host check")
	return nil
}

// readNewPassword pede a senha do sudo do admin. No terminal, sem eco e
// duas vezes; por pipe, uma linha.
func (e *cli) readNewPassword(user string) (string, error) {
	tty := false
	if f, ok := e.in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		tty = true
	}
	prompt := "Senha do sudo pro " + user + " (o SSH continua so por chave): "
	pw, err := e.readSecret(prompt)
	if err != nil {
		return "", err
	}
	if len(pw) < 8 {
		return "", errors.New("senha curta: use pelo menos 8 caracteres")
	}
	if tty {
		again, err := e.readSecret("De novo: ")
		if err != nil {
			return "", err
		}
		if again != pw {
			return "", errors.New("as senhas nao conferem: nada mudou")
		}
	}
	return pw, nil
}

func (e *cli) hostConfirm(args []string) error {
	fs, _ := e.newFlags("host confirm")
	if err := e.parse(fs, args); err != nil {
		return err
	}
	// o confirm prova que o login novo funcionou: do root (a sessao
	// antiga) nao prova nada
	if u := os.Getenv("SUDO_USER"); u == "" || u == "root" {
		return errors.New("rode do login novo do admin (ssh admin@vps, depois sudo cais host confirm): e ele que prova que a chave funciona")
	}
	ok, err := host.Confirm(context.Background(), hostSystem())
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintln(e.out, "Nada pendente: o SSH ja esta confirmado (ou voltou sozinho; rode o harden de novo).")
		return nil
	}
	fmt.Fprintln(e.out, "Confirmado: o SSH fica so com chave e sem o root.")
	return nil
}
