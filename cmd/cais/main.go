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
      --github-app-id --host-s3
  cais setup secret <nome>           grava um secret informado (le do stdin), ex: github_app_key
  cais setup run                     executa o Setup e mostra o progresso

Servicos opcionais (o proxy sobe sempre):
  cais service <nome> on|off         liga ou desliga postgres, mysql ou garage e aplica

Bancos (no Postgres/MySQL da infra):
  cais db create postgres|mysql <nome> [-extensions postgis]
                                     database e usuario com senha nova (guardada no cais)
  cais db list                       databases criados pelo cais: secrets, espaco e app
  cais db delete postgres|mysql <nome> [-yes]
                                     apaga o database e o usuario (nao pode estar ligado a app)

Buckets (no Garage da infra):
  cais bucket create <nome>          bucket e uma chave so dele (guardada no cais)
  cais bucket list                   buckets criados pelo cais: secrets, espaco e app
  cais bucket delete <nome> [-yes]   apaga o bucket (vazio) e a chave (nao pode estar ligado a app)

Apps (repositorio no GitHub com o compose de producao pronto):
  cais apps                          lista os apps e o estado dos containers
  cais app create <nome> <dono/repo> [-branch main] [-compose docker-compose.prod.yml]
  cais app deploy <nome> [-ref <branch ou commit>]
                                     baixa o commit, build com a versao no ar, sobe;
                                     se o up falhar, a versao anterior volta
  cais app guide <nome>              modelo de compose: labels do Traefik, redes, secrets
  cais app env <nome> [VAR=valor...] [-unset VAR,...] [-import arquivo.env]
                                     mostra ou muda as variaveis (.env do projeto);
                                     -import junta um .env (- le do stdin)
  cais app db <nome> [postgres/<db> | off postgres/<db>]
                                     bancos da infra do app: o deploy grava os secrets
                                     postgres_<db>_user e postgres_<db>_pass em ${CAIS_SECRETS}
  cais app bucket <nome> [<bucket> | off <bucket>]
                                     buckets do app: o deploy grava os secrets
                                     s3_<bucket>_access_key e s3_<bucket>_secret_key
  cais app secrets <nome>            lista os secrets do app (so nomes)
  cais app secret <nome> <secret>    grava um secret (le sem eco, ou do stdin)
  cais app restart <nome> [service]  reinicia o app (ou um service)
  cais app stop <nome>               para o app sem apagar nada
  cais app logs <nome> <container>   ultimas linhas do log
  cais app delete <nome> [-volumes] [-yes]
                                     exclui o app (containers, imagens, codigo, variaveis,
                                     secrets); -volumes apaga tambem os dados. O database
                                     da infra fica

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
