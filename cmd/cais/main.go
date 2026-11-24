package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/mcpserver"
	"github.com/trdSystemDev/cais/internal/rpc"
	"github.com/trdSystemDev/cais/internal/ui"
)

// version e preenchido no build via -ldflags "-X main.version=..."
var version = "dev"

// cli e o que os comandos usam do mundo de fora: o daemon, a saida e a
// entrada. O main usa os de verdade; os testes trocam por um core falso e
// buffers.
type cli struct {
	out, err io.Writer
	in       io.Reader
	// dial abre a conexao com o daemon (padrao: o socket, pelo connect)
	dial func(socket string) (daemonAPI, error)
}

// daemonAPI e o daemon como os comandos usam: a core.API e o Close.
type daemonAPI interface {
	core.API
	Close() error
}

func main() {
	e := &cli{out: os.Stdout, err: os.Stderr, in: os.Stdin, dial: func(socket string) (daemonAPI, error) {
		return connect(socket)
	}}
	os.Exit(e.run(os.Args[1:]))
}

func (e *cli) connect(socket string) (daemonAPI, error) { return e.dial(socket) }

// usageError e flag ou argumento invalido: o pacote flag ja mostrou o erro
// e a ajuda; sai com 2, como antes.
type usageError struct{ err error }

func (u *usageError) Error() string { return u.err.Error() }
func (u *usageError) Unwrap() error { return u.err }

// parse le as flags: -h devolve flag.ErrHelp (a ajuda ja saiu) e flag
// invalida vira usageError.
func (e *cli) parse(fs *flag.FlagSet, args []string) error {
	err := fs.Parse(args)
	if err != nil && !errors.Is(err, flag.ErrHelp) {
		return &usageError{err}
	}
	return err
}

// run roda o comando de args e devolve o codigo de saida.
func (e *cli) run(args []string) int {
	cmd := "tui"
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}

	var err error
	switch cmd {
	case "tui":
		err = e.runTUI(args)
	case "daemon":
		err = e.runDaemon(args)
	case "state":
		err = e.runState(args)
	case "check":
		err = e.runCheck(args)
	case "setup":
		err = e.runSetup(args)
	case "apps":
		err = e.runApps(args)
	case "app":
		err = e.runApp(args)
	case "db":
		err = e.runDB(args)
	case "bucket":
		err = e.runBucket(args)
	case "service":
		err = e.runService(args)
	case "backup":
		err = e.runBackup(args)
	case "mcp":
		err = e.runMCP(args)
	case "run":
		err = e.runRun(args)
	case "host":
		err = e.runHost(args)
	case "version":
		fmt.Fprintln(e.out, versionLine())
	case "help":
		fmt.Fprint(e.out, helpFor(strings.Join(args, " ")))
	default:
		fmt.Fprintf(e.err, "comando desconhecido: %s\n\n%s", cmd, overview())
		return 2
	}
	var usage *usageError
	var exit *exitError
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return 0
	case errors.As(err, &usage):
		return 2
	case errors.As(err, &exit):
		fmt.Fprintln(e.err, "cais:", err)
		return exit.code
	default:
		fmt.Fprintln(e.err, "cais:", err)
		return 1
	}
}

func (e *cli) newFlags(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	// -h mostra a ajuda do comando (ou do grupo, sem o subcomando)
	fs.SetOutput(e.out)
	fs.Usage = func() { fmt.Fprint(e.out, helpFor(name)) }
	socket := fs.String("socket", rpc.DefaultSocketPath(), "caminho do socket do daemon")
	return fs, socket
}

func (e *cli) runDaemon(args []string) error {
	fs, socket := e.newFlags("daemon")
	group := fs.String("group", "", "grupo que pode usar o cais (alem do root)")
	autoEvery := fs.Duration("auto-every", time.Minute, "intervalo da consulta do deploy automatico ao GitHub")
	deployTimeout := fs.Duration("deploy-timeout", core.DefaultDeployTimeout, "limite de um deploy (download, build e troca); passou, cancela e volta a versao anterior")
	logFormat := fs.String("log-format", "text", "formato do log: text (chave=valor) ou json")
	logLevel := fs.String("log-level", "info", "nivel minimo do log: debug, info, warn ou error")
	if err := e.parse(fs, args); err != nil {
		return err
	}
	logger, err := newLogger(e.err, *logFormat, *logLevel, stderrIsJournal())
	if err != nil {
		return err
	}
	// o log padrao (e o pacote log) passa a sair por ele
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ln, err := rpc.Listen(*socket, *group)
	if err != nil {
		return err
	}
	// SIGTERM (systemctl stop/restart, install.sh) desliga sem cortar nada:
	// as acoes em andamento terminam antes. Um segundo sinal encerra na hora.
	go func() {
		<-ctx.Done()
		stop()
	}()

	c := core.New(core.Deps{DeployTimeout: *deployTimeout, Version: version, Logger: logger})
	go c.CheckEnvironment(ctx)
	// seguranca da VPS (e o Docker) de novo de hora em hora
	go c.RunEnvironmentChecks(ctx, time.Hour)
	// deploy que o daemon anterior nao terminou (queda, kill, reboot): volta
	// a versao que estava no ar
	go c.RecoverApps(ctx)
	// deploy automatico: consulta o GitHub (nada de webhook, nenhuma porta)
	go c.RunAutoDeploy(ctx, *autoEvery)
	// limpeza do disco (cache de build): uma vez por dia, a primeira logo
	// depois de subir
	go c.RunMaintenance(ctx, 10*time.Minute, 24*time.Hour)
	// backup diario no horario da config (confere a cada minuto)
	go c.RunBackups(ctx, time.Minute)

	logger.Info("daemon no ar", "versao", version, "socket", *socket)
	err = rpc.Serve(ctx, ln, c)
	c.Close() // deploy automatico e backup em andamento
	logger.Info("daemon encerrado")
	return err
}

// connect abre o cliente do daemon com uma mensagem de erro que diz o
// que fazer, ja que "connection refused" sozinho nao ajuda ninguem.
func connect(socket string) (*rpc.Client, error) {
	if err := rpc.CheckSocketPath(socket); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err := rpc.Dial(ctx, socket)
	if err != nil {
		hint := "suba com: sudo systemctl start cais (ou rode: cais daemon)"
		if errors.Is(err, os.ErrPermission) {
			hint = "seu usuario nao tem permissao no socket: use sudo ou entre no grupo do --group do daemon"
		}
		return nil, fmt.Errorf("nao consegui falar com o daemon em %s: %v\n%s", socket, err, hint)
	}
	return c, nil
}

// runMCP e o servidor MCP pelo stdin/stdout (o cliente roda: ssh vps cais mcp).
// O stdout e so do protocolo: avisos e erros vao pro stderr.
func (e *cli) runMCP(args []string) error {
	fs, socket := e.newFlags("mcp")
	if err := e.parse(fs, args); err != nil {
		return err
	}
	c, err := e.connect(*socket)
	if err != nil {
		return err
	}
	defer c.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return mcpserver.New(c, version).Run(ctx, &mcp.StdioTransport{})
}

func (e *cli) runTUI(args []string) error {
	fs, socket := e.newFlags("tui")
	if err := e.parse(fs, args); err != nil {
		return err
	}

	c, err := e.connect(*socket)
	if err != nil {
		return err
	}
	defer c.Close()
	return ui.New(c).Run()
}

func (e *cli) runState(args []string) error {
	fs, socket := e.newFlags("state")
	if err := e.parse(fs, args); err != nil {
		return err
	}

	c, err := e.connect(*socket)
	if err != nil {
		return err
	}
	defer c.Close()
	st, err := c.Snapshot(context.Background())
	if err != nil {
		return err
	}
	return e.printJSON(st)
}

func (e *cli) runCheck(args []string) error {
	fs, socket := e.newFlags("check")
	if err := e.parse(fs, args); err != nil {
		return err
	}

	c, err := e.connect(*socket)
	if err != nil {
		return err
	}
	defer c.Close()
	env, err := c.CheckEnvironment(context.Background())
	if err != nil {
		return err
	}
	return e.printJSON(env)
}

func (e *cli) printJSON(v any) error {
	enc := json.NewEncoder(e.out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// newLogger monta o log do daemon. Com o journald (journal), o horario sai
// da linha: o journal ja registra o dele.
func newLogger(w io.Writer, format, level string, journal bool) (*slog.Logger, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("-log-level %q: use debug, info, warn ou error", level)
	}
	opts := &slog.HandlerOptions{Level: lvl}
	if journal {
		opts.ReplaceAttr = func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		}
	}
	switch format {
	case "text":
		return slog.New(slog.NewTextHandler(w, opts)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	}
	return nil, fmt.Errorf("-log-format %q: use text ou json", format)
}

// versionLine e a linha do cais version: a versao do release e, quando o
// binario tem, o commit e o Go que o compilaram. Uma linha so: o install.sh
// compara a do binario novo com a do instalado.
func versionLine() string {
	line := "cais " + version
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return line
	}
	var extra []string
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && len(s.Value) >= 7 {
			extra = append(extra, "commit "+s.Value[:7])
		}
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.modified" && s.Value == "true" && len(extra) > 0 {
			extra[0] += "+mudancas"
		}
	}
	if info.GoVersion != "" {
		extra = append(extra, info.GoVersion)
	}
	if len(extra) == 0 {
		return line
	}
	return line + " (" + strings.Join(extra, ", ") + ")"
}
