package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
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

func main() {
	cmd, args := "tui", os.Args[1:]
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}

	var err error
	switch cmd {
	case "tui":
		err = runTUI(args)
	case "daemon":
		err = runDaemon(args)
	case "state":
		err = runState(args)
	case "check":
		err = runCheck(args)
	case "setup":
		err = runSetup(args)
	case "apps":
		err = runApps(args)
	case "app":
		err = runApp(args)
	case "db":
		err = runDB(args)
	case "bucket":
		err = runBucket(args)
	case "service":
		err = runService(args)
	case "backup":
		err = runBackup(args)
	case "mcp":
		err = runMCP(args)
	case "version":
		fmt.Println("cais", version)
	case "help":
		fmt.Print(helpFor(strings.Join(args, " ")))
	default:
		fmt.Fprintf(os.Stderr, "comando desconhecido: %s\n\n%s", cmd, overview())
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "cais:", err)
		os.Exit(1)
	}
}

func newFlags(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	// -h mostra a ajuda do comando (ou do grupo, sem o subcomando)
	fs.SetOutput(os.Stdout)
	fs.Usage = func() { fmt.Print(helpFor(name)) }
	socket := fs.String("socket", rpc.DefaultSocketPath(), "caminho do socket do daemon")
	return fs, socket
}

func runDaemon(args []string) error {
	fs, socket := newFlags("daemon")
	group := fs.String("group", "", "grupo que pode usar o cais (alem do root)")
	autoEvery := fs.Duration("auto-every", time.Minute, "intervalo da consulta do deploy automatico ao GitHub")
	fs.Parse(args)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ln, err := rpc.Listen(*socket, *group)
	if err != nil {
		return err
	}
	c := core.New(core.Deps{})
	go c.CheckEnvironment(ctx)
	// deploy automatico: consulta o GitHub (nada de webhook, nenhuma porta)
	go c.RunAutoDeploy(ctx, *autoEvery)
	// limpeza do disco (cache de build): uma vez por dia, a primeira logo
	// depois de subir
	go c.RunMaintenance(ctx, 10*time.Minute, 24*time.Hour)
	// backup diario no horario da config (confere a cada minuto)
	go c.RunBackups(ctx, time.Minute)

	log.Printf("cais daemon %s ouvindo em %s", version, *socket)
	err = rpc.Serve(ctx, ln, c)
	log.Print("cais daemon encerrado")
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
func runMCP(args []string) error {
	fs, socket := newFlags("mcp")
	fs.Parse(args)
	c, err := connect(*socket)
	if err != nil {
		return err
	}
	defer c.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return mcpserver.New(c, version).Run(ctx, &mcp.StdioTransport{})
}

func runTUI(args []string) error {
	fs, socket := newFlags("tui")
	fs.Parse(args)

	c, err := connect(*socket)
	if err != nil {
		return err
	}
	defer c.Close()
	return ui.New(c).Run()
}

func runState(args []string) error {
	fs, socket := newFlags("state")
	fs.Parse(args)

	c, err := connect(*socket)
	if err != nil {
		return err
	}
	defer c.Close()
	st, err := c.Snapshot(context.Background())
	if err != nil {
		return err
	}
	return printJSON(st)
}

func runCheck(args []string) error {
	fs, socket := newFlags("check")
	fs.Parse(args)

	c, err := connect(*socket)
	if err != nil {
		return err
	}
	defer c.Close()
	env, err := c.CheckEnvironment(context.Background())
	if err != nil {
		return err
	}
	return printJSON(env)
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
