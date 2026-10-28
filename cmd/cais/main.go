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

	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/rpc"
	"github.com/trdSystemDev/cais/internal/ui"
)

// version e preenchido no build via -ldflags "-X main.version=..."
var version = "dev"

const usage = `cais -- painel da VPS

Uso:
  cais [--socket caminho]            abre a interface (TUI)
  cais daemon [--socket] [--group]   roda o daemon (dono do estado e das acoes)
  cais state [--socket]              imprime o estado atual em JSON
  cais check [--socket]              checa o ambiente e imprime o resultado em JSON
  cais version                       mostra a versao

Setup da VPS:
  cais setup                         mostra o plano: o que falta e o que vai mudar
  cais setup config [opcoes]         grava o /etc/cais/config.yaml (so o que for passado)
      --domain --email --tls letsencrypt|selfsigned --http-port --https-port
      --github-client --github-admin --host-ci --host-registry --host-s3
  cais setup secret <nome>           grava um secret informado (le do stdin)
  cais setup run                     executa o Setup e mostra o progresso

Servicos opcionais (proxy, registry e woodpecker sobem sempre):
  cais service <nome> on|off         liga ou desliga postgres, mysql ou garage e aplica

Apps:
  cais apps                          lista os apps e o estado dos containers
  cais app create <nome>             cria um app (o nome do repositorio no GitHub)
  cais app routes <nome> [rota...]   mostra ou troca as rotas; rota: dominio[/caminho]=service:porta
                                     ex: app.exemplo.com.br=web:3000 app.exemplo.com.br/api=api:8080
  cais app deploy <nome> < docker-compose.yml
                                     valida e sobe; se falhar, a versao anterior volta
  cais app restart <nome> [service]  reinicia o app (ou um service)
  cais app stop <nome>               para o app sem apagar nada
  cais app logs <nome> <container>   ultimas linhas do log

Registry de imagens:
  cais registry users                lista quem pode entrar
  cais registry add <usuario>        cria o acesso e mostra a senha (uma vez so)
  cais registry remove <usuario>     tira o acesso

O socket padrao e /run/cais/cais.sock (ou a variavel CAIS_SOCKET).
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
	case "setup":
		err = runSetup(args)
	case "registry":
		err = runRegistry(args)
	case "apps":
		err = runApps(args)
	case "app":
		err = runApp(args)
	case "service":
		err = runService(args)
	case "version":
		fmt.Println("cais", version)
	case "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "comando desconhecido: %s\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "cais:", err)
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
	group := fs.String("group", "", "grupo que pode usar o cais (alem do root)")
	fs.Parse(args)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ln, err := rpc.Listen(*socket, *group)
	if err != nil {
		return err
	}
	c := core.New(core.Deps{})
	go c.CheckEnvironment(ctx)

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
