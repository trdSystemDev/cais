package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/ci"
	"github.com/trdSystemDev/cais/internal/rpc"
)

// runDeploy e o forced command da chave do CI: o pipeline faz
// ssh cais-deploy@vps "<app> <tag>" e o sshd roda isto com o pedido em
// SSH_ORIGINAL_COMMAND. Nada passa por shell: so app e tag, conferidos
// pelo daemon, que so aceita deploy por tag no socket de deploy.
func runDeploy(args []string) error {
	fs := flag.NewFlagSet("deploy", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	socket := fs.String("socket", rpc.DeploySocketPath(), "caminho do socket de deploy")
	fs.Parse(args)

	fields := fs.Args()
	if cmd, ok := os.LookupEnv("SSH_ORIGINAL_COMMAND"); ok {
		fields = strings.Fields(cmd)
	}
	if len(fields) != 2 {
		return errors.New("uso: cais deploy <app> <tag> (no CI: ssh cais-deploy@vps \"<app> <tag>\")")
	}
	// o CI manda o nome do repositorio como esta (TRDSmartPodCast): o app
	// tem o nome convertido, igual ao do import
	app, tag := apps.NameFromRepo(fields[0]), fields[1]

	if err := rpc.CheckSocketPath(*socket); err != nil {
		return err
	}
	dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := rpc.Dial(dctx, *socket)
	if err != nil {
		return fmt.Errorf("nao consegui falar com o socket de deploy em %s: %v\no daemon precisa rodar com --deploy-user", *socket, err)
	}
	defer c.Close()

	fmt.Printf("cais: deploy de %s:%s (esperando os healthchecks)...\n", app, tag)
	res, err := c.DeployAppTag(context.Background(), app, tag)
	if err != nil {
		return err
	}
	fmt.Printf("cais: %s no ar: %s\n", app, strings.Join(res.Images, ", "))
	return nil
}

func runCI(args []string) error {
	sub, args := subcommand(args)
	switch sub {
	case "pipeline":
		// estatico: nao precisa do daemon
		_, err := os.Stdout.Write(ci.Pipeline())
		return err
	case "setup":
		return ciSetup(args)
	default:
		return fmt.Errorf("subcomando desconhecido: ci %s (veja: cais help)", sub)
	}
}

func ciSetup(args []string) error {
	fs, socket := newFlags("ci setup")
	fs.Parse(args)
	c, err := connect(*socket)
	if err != nil {
		return err
	}
	defer c.Close()
	info, err := c.CISetup(context.Background())
	if err != nil {
		return err
	}

	u := info.DeployUser
	fmt.Printf(`Ligar o Woodpecker ao cais

1) Usuario %[1]s na VPS (uma vez, como root). A chave so roda o cais deploy:
   sem shell interativo, sem tunel, so o pedido de deploy de uma tag.

   useradd --system --create-home --shell /bin/sh %[1]s
   install -d -m 700 -o %[1]s -g %[1]s ~%[1]s/.ssh
   cat > ~%[1]s/.ssh/authorized_keys <<'CHAVE'
%[2]s
CHAVE
   chown %[1]s:%[1]s ~%[1]s/.ssh/authorized_keys && chmod 600 ~%[1]s/.ssh/authorized_keys

2) Daemon com o socket de deploy: no ExecStart do cais.service
   ExecStart=/usr/local/bin/cais daemon --deploy-user %[1]s
   systemctl daemon-reload && systemctl restart cais

3) Secrets no Woodpecker (na organizacao, valem pra todos os repositorios):
`, u, info.AuthorizedKeys)
	for _, s := range info.Secrets {
		fmt.Printf("\n   %s  (%s)\n", s.Name, s.Desc)
		for _, line := range strings.Split(strings.TrimRight(s.Value, "\n"), "\n") {
			fmt.Printf("      %s\n", line)
		}
	}
	fmt.Printf(`
4) Em cada repositorio: o app no cais com o mesmo nome do repositorio e
   cais ci pipeline > .woodpecker.yml
   Cada push na main faz o build, publica %s/<app>:<commit> e o deploy.
`, info.RegistryHost)
	return nil
}
