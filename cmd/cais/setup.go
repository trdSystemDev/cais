package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/trdSystemDev/cais/internal/bootstrap"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/rpc"
)

// subcommand separa "cais setup <sub> ..." do resto dos argumentos.
func subcommand(args []string) (string, []string) {
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		return args[0], args[1:]
	}
	return "", args
}

func runSetup(args []string) error {
	sub, args := subcommand(args)
	switch sub {
	case "", "plan":
		return setupPlan(args)
	case "config":
		return setupConfig(args)
	case "secret":
		return setupSecret(args)
	case "notify-test":
		return setupNotifyTest(args)
	default:
		return groupError("setup", sub)
	}
}

func setupPlan(args []string) error {
	fs, socket := newFlags("setup")
	fs.Parse(args)
	c, err := connect(*socket)
	if err != nil {
		return err
	}
	defer c.Close()

	ctx := context.Background()
	info, err := c.LoadConfig(ctx)
	if err != nil {
		return err
	}
	p, err := c.PlanSetup(ctx)
	if err != nil {
		return err
	}
	printPlan(os.Stdout, info, p)
	return nil
}

func printPlan(out io.Writer, info core.ConfigInfo, p bootstrap.Plan) {
	if !info.Exists {
		fmt.Fprintf(out, "Ainda sem configuracao (%s).\n\n", info.Path)
		fmt.Fprintln(out, "Comece com:")
		fmt.Fprintln(out, "  cais setup config --email voce@exemplo.com.br --github-app-id <id>")
		fmt.Fprintln(out, "  cais service proxy on")
		return
	}

	cfg := p.Config
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(out, "Config: %s\n", info.Path)
	fmt.Fprintf(w, "  e-mail\t%s\n", cfg.Email)
	if cfg.LetsEncrypt() {
		fmt.Fprintf(w, "  tls\tLet's Encrypt\n")
	} else {
		fmt.Fprintf(w, "  tls\tCA local, so pra teste (nomes de %s)\n", cfg.TestDomain)
	}
	fmt.Fprintf(w, "  portas\t%d (http), %d (https)\n", cfg.Proxy.HTTPPort, cfg.Proxy.HTTPSPort)
	defer func() {
		if warn := cfg.PortsWarning(); warn != "" {
			fmt.Fprintf(out, "\nAtencao: %s\n", warn)
		}
	}()
	if cfg.Notify.On() {
		fmt.Fprintf(w, "  avisos\t%s (de %s, pela Brevo)\n", strings.Join(cfg.Notify.To, ", "), cfg.Notify.From)
	} else {
		fmt.Fprintf(w, "  avisos\tdesligados (cais setup config --notify-to)\n")
	}
	w.Flush()

	if len(p.Stacks) > 0 {
		fmt.Fprintln(out, "\nServicos ligados:")
		for _, s := range p.Stacks {
			state := "em dia"
			switch s.Change {
			case bootstrap.ChangeModified:
				state = "config salva pra aplicar: cais service " + s.Name + " on"
			case bootstrap.ChangeNew:
				state = "ainda nao subiu: cais service " + s.Name + " on"
			}
			fmt.Fprintf(w, "  %s\t%s\n", s.Name, state)
		}
		w.Flush()
	} else if info.Exists {
		fmt.Fprintln(out, "\nNenhum servico ligado. Comece pelo proxy (publica os apps com HTTPS):")
		fmt.Fprintln(out, "  cais service proxy on")
	}
	if len(p.Disable) > 0 {
		fmt.Fprintln(out, "\nDesligados, ainda no disco (saem na proxima aplicacao; os volumes, com os dados, ficam):")
		for _, s := range p.Disable {
			fmt.Fprintf(w, "  %s\t%s\n", s.Name, s.Desc)
		}
		w.Flush()
	}
	if len(p.Problems) > 0 {
		fmt.Fprintln(out, "\nProblemas:")
		for _, pr := range p.Problems {
			fmt.Fprintf(out, "  - %s\n", pr)
		}
	}
	if len(p.Missing) > 0 {
		fmt.Fprintln(out, "\nFalta informar:")
		for _, m := range p.Missing {
			fmt.Fprintf(out, "  - %s: %s\n      cais setup secret %s\n", m.Name, m.Desc, m.Name)
		}
	}
}

// printDBAccess explica como chegar nos bancos: so por tunel SSH, que eles
// so escutam no loopback da VPS.
func printDBAccess(out io.Writer, p bootstrap.Plan) {
	if len(p.Databases) == 0 {
		return
	}
	fmt.Fprintln(out, "\nBancos: so pelo loopback da VPS, via tunel SSH. No seu computador:")
	fmt.Fprintf(out, "\n  %s\n", p.Tunnel)
	fmt.Fprintln(out, "\n  e no seu cliente (DBeaver, TablePlus, psql...):")
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, d := range p.Databases {
		fmt.Fprintf(w, "    %s\tlocalhost:%d\tusuario %s\tsenha: sudo cat %s\n", d.Name, d.Port, d.User, d.PasswordFile)
	}
	w.Flush()
}

// printTrust explica como confiar na CA local, no modo tls: selfsigned.
func printTrust(out io.Writer, p bootstrap.Plan) {
	if p.CAFile == "" {
		return
	}
	fmt.Fprintf(out, "\nCertificados assinados pela CA local do cais (%s).\n", p.CAFile)
	fmt.Fprintln(out, "Ela so assina nomes do dominio do teste. Pra confiar nela nesta maquina:")
	fmt.Fprintf(out, "\n  # navegador, curl e o resto do sistema\n")
	fmt.Fprintf(out, "  sudo trust anchor --store %s\n", p.CAFile)
	fmt.Fprintf(out, "\n  # pra desfazer: sudo trust anchor --remove %s\n", p.CAFile)
}

func setupConfig(args []string) error {
	fs, socket := newFlags("setup config")
	testDomain := fs.String("test-domain", "", "dominio do modo de teste (--tls selfsigned), ex: trd.localhost")
	email := fs.String("email", "", "e-mail do Let's Encrypt")
	tls := fs.String("tls", "", "letsencrypt (padrao) ou selfsigned (teste local)")
	httpPort := fs.Int("http-port", 0, "porta http do proxy no host")
	httpsPort := fs.Int("https-port", 0, "porta https do proxy no host")
	ghAppID := fs.Int64("github-app-id", 0, "App ID do GitHub App do cais (baixa o codigo dos repositorios privados)")
	hostS3 := fs.String("host-s3", "", "endpoint S3 do Garage (obrigatorio com o Garage ligado)")
	notifyFrom := fs.String("notify-from", "", "remetente dos avisos por e-mail (validado na Brevo)")
	notifyTo := fs.String("notify-to", "", "destinatarios dos avisos, separados por virgula (vazio desliga)")
	fs.Parse(args)

	c, err := connect(*socket)
	if err != nil {
		return err
	}
	defer c.Close()
	ctx := context.Background()

	info, err := c.LoadConfig(ctx)
	if err != nil {
		return err
	}
	cfg := info.Config
	// so o que foi passado muda; o resto fica como esta no arquivo
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "test-domain":
			cfg.TestDomain = *testDomain
		case "email":
			cfg.Email = *email
		case "tls":
			cfg.TLS = *tls
		case "http-port":
			cfg.Proxy.HTTPPort = *httpPort
		case "https-port":
			cfg.Proxy.HTTPSPort = *httpsPort
		case "github-app-id":
			cfg.GitHub.AppID = *ghAppID
		case "host-s3":
			cfg.Hosts.S3 = *hostS3
		case "notify-from":
			cfg.Notify.From = *notifyFrom
		case "notify-to":
			cfg.Notify.To = splitList(*notifyTo)
		}
	})
	if err := c.SaveConfig(ctx, cfg); err != nil {
		return err
	}
	fmt.Printf("Config gravada em %s.\n\n", info.Path)

	info, err = c.LoadConfig(ctx)
	if err != nil {
		return err
	}
	p, err := c.PlanSetup(ctx)
	if err != nil {
		return err
	}
	printPlan(os.Stdout, info, p)
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func setupSecret(args []string) error {
	fs, socket := newFlags("setup secret")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("uso: cais setup secret <nome> (o valor vem pelo stdin)")
	}
	name := fs.Arg(0)

	value, err := readSecret("Valor de " + name + ": ")
	if err != nil {
		return err
	}
	c, err := connect(*socket)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.SetSecret(context.Background(), name, value); err != nil {
		return err
	}
	fmt.Printf("%s gravado.\n", name)
	return nil
}

// readSecret le do terminal sem eco, ou do stdin inteiro quando vem de um
// pipe (cais setup secret x < arquivo). Nunca por argumento, que ficaria no
// historico do shell e no ps.
func readSecret(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, prompt)
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return strings.TrimSpace(string(b)), err
	}
	b, err := io.ReadAll(os.Stdin)
	return strings.TrimRight(string(b), "\r\n"), err
}

func setupNotifyTest(args []string) error {
	fs, socket := newFlags("setup notify-test")
	fs.Parse(args)
	c, err := connect(*socket)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.TestNotify(context.Background()); err != nil {
		return err
	}
	fmt.Println("E-mail de teste enviado.")
	return nil
}

func runService(args []string) error {
	fs, socket := newFlags("service")
	fs.Parse(args)
	if fs.NArg() != 2 || (fs.Arg(1) != "on" && fs.Arg(1) != "off") {
		return errors.New("uso: cais service <postgres|mysql|garage> on|off")
	}
	c, err := connect(*socket)
	if err != nil {
		return err
	}
	defer c.Close()
	return withProgress(c, func(ctx context.Context) error {
		_, err := c.SetService(ctx, fs.Arg(0), fs.Arg(1) == "on")
		return err
	})
}

// withProgress roda uma acao que dispara o Setup e imprime os passos dele
// ao vivo; no fim, as instrucoes de acesso.
func withProgress(c *rpc.Client, action func(context.Context) error) error {
	ctx := context.Background()

	// o Subscribe entrega primeiro o estado atual, que pode ter os passos
	// de uma execucao anterior: so imprime o que comecou depois de agora
	prev, err := c.Snapshot(ctx)
	if err != nil {
		return err
	}
	states, unsubscribe, err := c.Subscribe(ctx)
	if err != nil {
		return err
	}
	p := &stepPrinter{since: prev.Setup.StartedAt, seen: map[string]bootstrap.Status{}}
	printed := make(chan struct{})
	go func() {
		defer close(printed)
		for st := range states {
			p.print(st.Setup)
		}
	}()

	runErr := action(ctx)
	unsubscribe()
	<-printed
	// a resposta pode chegar antes da ultima notificacao, e em caso de erro
	// ela nem traz os passos: o estado final vem do daemon
	if final, err := c.Snapshot(ctx); err == nil {
		p.print(final.Setup)
	}
	if runErr != nil {
		return runErr
	}
	fmt.Println("\nPronto.")
	if p, err := c.PlanSetup(ctx); err == nil {
		printDBAccess(os.Stdout, p)
		printTrust(os.Stdout, p)
	}
	return nil
}

var stepMark = map[bootstrap.Status]string{
	bootstrap.Running: "..",
	bootstrap.Done:    "ok",
	bootstrap.Failed:  "!!",
}

// stepPrinter imprime cada mudanca de status dos passos de execucoes que
// comecaram depois de since.
type stepPrinter struct {
	since time.Time
	seen  map[string]bootstrap.Status
}

func (p *stepPrinter) print(s core.Setup) {
	if !s.StartedAt.After(p.since) {
		return
	}
	for _, step := range s.Steps {
		if step.Status == bootstrap.Pending || p.seen[step.ID] == step.Status {
			continue
		}
		p.seen[step.ID] = step.Status
		line := fmt.Sprintf("[%s] %s", stepMark[step.Status], step.Desc)
		// num passo que falhou, o detalhe e o erro, impresso no fim
		if step.Detail != "" && step.Status != bootstrap.Failed {
			line += " -- " + step.Detail
		}
		fmt.Println(line)
	}
}
