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
	"syscall"
	"time"

	"github.com/trdSystemDev/vpsctl/internal/core"
	"github.com/trdSystemDev/vpsctl/internal/rpc"
	"github.com/trdSystemDev/vpsctl/internal/ui"
)

// version e preenchido no build via -ldflags "-X main.version=..."
var version = "dev"

const usage = `vpsctl -- painel da VPS

Uso:
  vpsctl [--socket caminho]            abre a interface (TUI)
  vpsctl daemon [--socket] [--group]   roda o daemon (dono do estado e das acoes)
  vpsctl state [--socket]              imprime o estado atual em JSON
  vpsctl check [--socket]              checa o ambiente e imprime o resultado em JSON
  vpsctl version                       mostra a versao

O socket padrao e /run/vpsctl/vpsctl.sock (ou a variavel VPSCTL_SOCKET).
`

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
	case "version":
		fmt.Println("vpsctl", version)
	case "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "comando desconhecido: %s\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "vpsctl:", err)
		os.Exit(1)
	}
}

func newFlags(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	socket := fs.String("socket", rpc.DefaultSocketPath(), "caminho do socket do daemon")
	return fs, socket
}

func runDaemon(args []string) error {
	fs, socket := newFlags("daemon")
	group := fs.String("group", "", "grupo que pode usar o vpsctl (alem do root)")
	fs.Parse(args)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ln, err := rpc.Listen(*socket, *group)
	if err != nil {
		return err
	}
	c := core.New(core.Deps{})
	go c.CheckEnvironment(ctx)

	log.Printf("vpsctl daemon %s ouvindo em %s", version, *socket)
	err = rpc.Serve(ctx, ln, c)
	log.Print("vpsctl daemon encerrado")
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
		hint := "suba com: sudo systemctl start vpsctl (ou rode: vpsctl daemon)"
		if errors.Is(err, os.ErrPermission) {
			hint = "seu usuario nao tem permissao no socket: use sudo ou entre no grupo do --group do daemon"
		}
		return nil, fmt.Errorf("nao consegui falar com o daemon em %s: %v\n%s", socket, err, hint)
	}
	return c, nil
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
