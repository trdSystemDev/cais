package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/host"
)

// hostSystem e a maquina que o cais host confere e prepara (trocada nos
// testes).
var hostSystem = host.Local

// hostConfigFile e a config que guarda o horario automatico do reinicio
// (trocada nos testes).
var hostConfigFile = config.ConfigFile

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
	case "reboot":
		return e.hostReboot(args)
	case "access":
		return e.hostAccess(args)
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
		"Docker: limite nos logs e o live-restore (atualizar o Docker nao derruba os containers)",
		"atualizar o sistema; instalar ufw, fail2ban e unattended-upgrades",
		"atualizacoes de seguranca automaticas (sem reiniciar sozinho)",
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
	lastPhase := ""
	res, err := host.Harden(ctx, sys, opts, func(s host.Step) {
		switch {
		case s.Status == host.StepPending:
			return
		case s.Status == host.StepRunning && s.Detail != "":
			// a fase do passo (o andamento, que muda a cada segundo, so na TUI)
			if s.Detail != lastPhase {
				lastPhase = s.Detail
				fmt.Fprintln(e.out, "     "+s.Detail)
			}
			return
		}
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

// hostReboot e o cais host reboot: sem argumento mostra o pendente e o
// agendado; HH:MM agenda uma vez (hoje ou amanha), cancel desfaz e auto
// HH:MM|off e o horario em que a VPS reinicia sozinha quando uma
// atualizacao pede (na config; o daemon confere de hora em hora).
func (e *cli) hostReboot(args []string) error {
	fs, _ := e.newFlags("host reboot")
	if err := e.parse(fs, args); err != nil {
		return err
	}
	ctx, sys, pos := context.Background(), hostSystem(), fs.Args()
	if sys.Geteuid() != 0 {
		return errors.New("rode com sudo (sudo cais host reboot ...)")
	}
	usage := errors.New("uso: cais host reboot [HH:MM | cancel | auto HH:MM|off]")
	switch {
	case len(pos) == 0:
	case len(pos) == 1 && pos[0] == "cancel":
		if err := host.CancelReboot(ctx, sys); err != nil {
			return err
		}
		// com o automatico ligado e o reinicio pendente, ele agenda de novo
		if cfg, err := config.Load(hostConfigFile()); err == nil {
			if _, err := host.EnsureAutoReboot(ctx, sys, cfg.RebootTime, time.Now()); err != nil {
				return err
			}
		}
	case len(pos) == 1:
		at, ok := config.NextClock(time.Now(), pos[0])
		if !ok {
			return fmt.Errorf("horario %q invalido (use HH:MM, ex: 01:00)", pos[0])
		}
		if err := host.ScheduleReboot(ctx, sys, at, false); err != nil {
			return err
		}
	case len(pos) == 2 && pos[0] == "auto":
		clock := pos[1]
		if clock == "off" {
			clock = ""
		}
		cfg, err := config.Load(hostConfigFile())
		if errors.Is(err, config.ErrNotFound) {
			cfg, err = config.Default(), nil
		}
		if err != nil {
			return err
		}
		cfg.RebootTime = clock
		if err := cfg.Validate(); err != nil {
			return err
		}
		if err := config.Save(hostConfigFile(), cfg); err != nil {
			return err
		}
		if _, err := host.EnsureAutoReboot(ctx, sys, clock, time.Now()); err != nil {
			return err
		}
	default:
		return &usageError{usage}
	}
	e.printReboot(ctx, sys)
	return nil
}

func (e *cli) printReboot(ctx context.Context, sys host.System) {
	r := host.RebootStatus(ctx, sys)
	switch {
	case r.Pending:
		fmt.Fprintln(e.out, "Reinicio pendente: uma atualizacao (kernel ou biblioteca) so vale depois de reiniciar.")
	default:
		fmt.Fprintln(e.out, "Nenhum reinicio pendente.")
	}
	switch {
	case r.Scheduled() && r.Auto:
		fmt.Fprintln(e.out, "Agendado: "+host.When(r.At, time.Now())+" (horario automatico).")
	case r.Scheduled():
		fmt.Fprintln(e.out, "Agendado: "+host.When(r.At, time.Now())+" (cancele com sudo cais host reboot cancel).")
	}
	clock := ""
	if cfg, err := config.Load(hostConfigFile()); err == nil {
		clock = cfg.RebootTime
	}
	if clock != "" {
		fmt.Fprintln(e.out, "Horario automatico: "+clock+" (quando uma atualizacao pede, a VPS reinicia sozinha nesse horario).")
	} else {
		fmt.Fprintln(e.out, "Horario automatico: desligado (ligue com sudo cais host reboot auto 01:00).")
	}
}

// hostAccess e o cais host access: sem argumento lista quem entra; add da
// acesso a uma chave (do arquivo, ou - pra ler da entrada), so a TUI ou
// como admin (-admin NOME); remove tira uma chave de todo lugar (pela
// impressao digital) ou um admin (-admin NOME).
func (e *cli) hostAccess(args []string) error {
	sub, args := subcommand(args)
	fs, _ := e.newFlags("host access")
	admin := fs.String("admin", "", "o admin (usuario proprio com sudo): add cria se faltar, remove tira")
	if err := e.parse(fs, args); err != nil {
		return err
	}
	ctx, sys, pos := context.Background(), hostSystem(), fs.Args()
	if sys.Geteuid() != 0 {
		return errors.New("rode com sudo (sudo cais host access ...)")
	}
	usage := &usageError{errors.New("uso: cais host access [add [-admin NOME] arquivo.pub|- | remove SHA256:...|-admin NOME]")}
	switch sub {
	case "", "list":
	case "add":
		if len(pos) != 1 {
			return usage
		}
		var b []byte
		var err error
		if pos[0] == "-" {
			b, err = io.ReadAll(e.in)
		} else {
			b, err = os.ReadFile(pos[0])
		}
		if err != nil {
			return err
		}
		req := host.AccessRequest{Key: strings.TrimSpace(string(b)), Admin: *admin}
		if _, _, err := host.ParseKey(req.Key); err != nil {
			return err
		}
		if req.Admin != "" && host.AdminNeedsPassword(ctx, sys, req.Admin) {
			if req.Password, err = e.readNewPassword(req.Admin); err != nil {
				return err
			}
		}
		detail, err := host.AddAccess(ctx, sys, req)
		if err != nil {
			return err
		}
		fmt.Fprintln(e.out, detail)
	case "remove":
		var detail string
		var err error
		switch {
		case *admin != "" && len(pos) == 0:
			detail, err = host.RemoveAdmin(ctx, sys, *admin)
		case *admin == "" && len(pos) == 1:
			detail, err = host.RemoveKey(ctx, sys, pos[0])
		default:
			return usage
		}
		if err != nil {
			return err
		}
		fmt.Fprintln(e.out, detail)
	default:
		return groupError("host access", sub)
	}
	a, err := host.ListAccess(ctx, sys)
	if err != nil {
		return err
	}
	e.printAccess(a)
	return nil
}

func (e *cli) printAccess(a host.Access) {
	w := tabwriter.NewWriter(e.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "\nCHAVE\tIMPRESSAO DIGITAL\tENTRA COMO")
	for _, k := range a.Keys {
		var where []string
		for _, u := range k.Users {
			switch u {
			case host.CaisUser:
				where = append(where, "TUI (ssh cais@)")
			case "root":
				where = append(where, "root")
			default:
				where = append(where, "admin "+u)
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", cmpOrCLI(k.Comment, k.Type), k.Fingerprint, strings.Join(where, ", "))
	}
	w.Flush()
	var admins []string
	for _, ad := range a.Admins {
		admins = append(admins, fmt.Sprintf("%s (%d chave(s))", ad.Name, ad.Keys))
	}
	fmt.Fprintln(e.out, "\nAdmins: "+cmpOrCLI(strings.Join(admins, ", "), "nenhum"))
}

func cmpOrCLI(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
