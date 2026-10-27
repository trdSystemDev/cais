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
	"github.com/trdSystemDev/cais/internal/secrets"
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
	case "run":
		return setupRun(args)
	default:
		return fmt.Errorf("subcomando desconhecido: setup %s (veja: cais help)", sub)
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
		fmt.Fprintln(out, "  cais setup config --domain exemplo.com.br --email voce@exemplo.com.br \\")
		fmt.Fprintln(out, "      --github-client <client id> --github-admin <usuario do github>")
		return
	}

	cfg := p.Config
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(out, "Config: %s\n", info.Path)
	fmt.Fprintf(w, "  dominio\t%s\n", cfg.Domain)
	fmt.Fprintf(w, "  e-mail\t%s\n", cfg.Email)
	if cfg.LetsEncrypt() {
		fmt.Fprintf(w, "  tls\tLet's Encrypt\n")
	} else {
		fmt.Fprintf(w, "  tls\tautoassinado (so pra teste)\n")
	}
	fmt.Fprintf(w, "  portas\t%d (http), %d (https)\n", cfg.Proxy.HTTPPort, cfg.Proxy.HTTPSPort)
	if cfg.HasStack("registry") {
		fmt.Fprintf(w, "  registry\thttps://%s\n", cfg.RegistryHost())
	}
	if cfg.HasStack("woodpecker") {
		fmt.Fprintf(w, "  ci\thttps://%s (admin: %s)\n", cfg.CIHost(), cfg.Woodpecker.Admin)
	}
	w.Flush()

	if len(p.Stacks) > 0 {
		fmt.Fprintln(out, "\nStacks:")
		for _, s := range p.Stacks {
			fmt.Fprintf(w, "  %s\t%s\t%s\n", s.Name, s.Change, s.Desc)
		}
		w.Flush()
	}
	if len(p.Disable) > 0 {
		fmt.Fprintln(out, "\nDesativadas (o run derruba; os volumes, com os dados, ficam):")
		for _, s := range p.Disable {
			fmt.Fprintf(w, "  %s\t%s\n", s.Name, s.Desc)
		}
		w.Flush()
	}
	if len(p.CreateSecrets) > 0 {
		fmt.Fprintf(out, "\nSecrets que o cais vai gerar: %s\n", strings.Join(p.CreateSecrets, ", "))
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
	if p.Ready() {
		if p.CAFile == "" {
			fmt.Fprintln(out, "\nPronto. Antes de rodar, aponte o DNS dos nomes acima pra esta VPS.")
		} else {
			fmt.Fprintln(out, "\nPronto. Certificados da CA local: depois do run, confie nela (instrucoes no fim).")
		}
		fmt.Fprintln(out, "Execute: cais setup run")
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
	if p.Config.HasStack("registry") {
		dir := "/etc/docker/certs.d/" + p.Config.RegistryHost()
		fmt.Fprintf(out, "\n  # docker push/pull no registry (sem reiniciar o docker)\n")
		fmt.Fprintf(out, "  sudo mkdir -p %s\n  sudo cp %s %s/ca.crt\n", dir, p.CAFile, dir)
	}
	fmt.Fprintf(out, "\n  # navegador, curl e o resto do sistema (opcional)\n")
	fmt.Fprintf(out, "  sudo trust anchor --store %s\n", p.CAFile)
	fmt.Fprintf(out, "\n  # pra desfazer: sudo trust anchor --remove %s\n", p.CAFile)
}

func setupConfig(args []string) error {
	fs, socket := newFlags("setup config")
	domain := fs.String("domain", "", "dominio base")
	email := fs.String("email", "", "e-mail do Let's Encrypt")
	tls := fs.String("tls", "", "letsencrypt (padrao) ou selfsigned (teste local)")
	stackList := fs.String("stacks", "", "stacks ativas, separadas por virgula")
	httpPort := fs.Int("http-port", 0, "porta http do proxy no host")
	httpsPort := fs.Int("https-port", 0, "porta https do proxy no host")
	ghClient := fs.String("github-client", "", "Client ID do OAuth app do GitHub")
	ghAdmin := fs.String("github-admin", "", "usuario do GitHub que administra o Woodpecker")
	hostCI := fs.String("host-ci", "", "nome do Woodpecker (padrao ci.<dominio>)")
	hostReg := fs.String("host-registry", "", "nome do registry (padrao registry.<dominio>)")
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
		case "domain":
			cfg.Domain = *domain
		case "email":
			cfg.Email = *email
		case "tls":
			cfg.TLS = *tls
		case "stacks":
			cfg.Stacks = splitList(*stackList)
		case "http-port":
			cfg.Proxy.HTTPPort = *httpPort
		case "https-port":
			cfg.Proxy.HTTPSPort = *httpsPort
		case "github-client":
			cfg.Woodpecker.GitHubClient = *ghClient
		case "github-admin":
			cfg.Woodpecker.Admin = *ghAdmin
		case "host-ci":
			cfg.Hosts.CI = *hostCI
		case "host-registry":
			cfg.Hosts.Registry = *hostReg
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

func setupRun(args []string) error {
	fs, socket := newFlags("setup run")
	fs.Parse(args)
	c, err := connect(*socket)
	if err != nil {
		return err
	}
	defer c.Close()
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

	_, runErr := c.RunSetup(ctx)
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
	fmt.Println("\nSetup concluido.")
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

func runRegistry(args []string) error {
	sub, args := subcommand(args)
	fs, socket := newFlags("registry")
	fs.Parse(args)

	needUser := sub == "add" || sub == "remove"
	if needUser && fs.NArg() != 1 {
		return fmt.Errorf("uso: cais registry %s <usuario>", sub)
	}
	if sub != "users" && !needUser {
		return fmt.Errorf("subcomando desconhecido: registry %s (veja: cais help)", sub)
	}

	c, err := connect(*socket)
	if err != nil {
		return err
	}
	defer c.Close()
	ctx := context.Background()

	switch sub {
	case "users":
		users, err := c.RegistryUsers(ctx)
		if err != nil {
			return err
		}
		for _, u := range users {
			note := ""
			if secrets.IsServiceUser(u) {
				note = "  (servico do cais)"
			}
			fmt.Println(u + note)
		}
	case "add":
		user := fs.Arg(0)
		pw, err := c.AddRegistryUser(ctx, user)
		if err != nil {
			return err
		}
		host := registryHost(ctx, c)
		fmt.Printf("Usuario %s criado. Senha (aparece so agora, guarde num gerenciador):\n\n  %s\n\n", user, pw)
		fmt.Printf("No seu computador:\n  docker login %s -u %s\n", host, user)
	case "remove":
		if err := c.RemoveRegistryUser(ctx, fs.Arg(0)); err != nil {
			return err
		}
		fmt.Printf("Acesso de %s removido.\n", fs.Arg(0))
	}
	return nil
}

func registryHost(ctx context.Context, c *rpc.Client) string {
	info, err := c.LoadConfig(ctx)
	if err != nil || !info.Exists {
		return "registry.<dominio>"
	}
	return info.Config.RegistryHost()
}
