package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/trdSystemDev/cais/internal/mcpremote"
	"github.com/trdSystemDev/cais/internal/remotes"
)

// quietExit e o codigo de saida de um comando que ja mostrou o proprio erro
// (o ssh do cais -r): sai com ele, sem repetir nada.
type quietExit int

func (q quietExit) Error() string { return fmt.Sprintf("saiu com %d", int(q)) }

// runSSH roda o ssh de verdade com a entrada e a saida dadas, devolvendo o
// codigo de saida dele como quietExit.
func runSSH(args []string, in io.Reader, out, errw io.Writer) error {
	cmd := exec.Command("ssh", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, errw
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return quietExit(ee.ExitCode())
	}
	return err
}

func (e *cli) remotesPath() (string, error) { return remotes.DefaultPath() }

// runRemote: cais remote add|list|remove, a lista de VPS deste computador.
func (e *cli) runRemote(args []string) error {
	sub, args := subcommand(args)
	fs, _ := e.newFlags("remote " + sub)
	if sub == "" {
		fs, _ = e.newFlags("remote")
	}
	if err := e.parse(fs, args); err != nil {
		return err
	}
	path, err := e.remotesPath()
	if err != nil {
		return err
	}
	list, err := remotes.Load(path)
	if err != nil {
		return err
	}
	switch sub {
	case "add":
		if fs.NArg() != 2 {
			return errors.New("uso: cais remote add NOME DESTINO (ex: cais remote add prod cais@prod.tailnet.ts.net)")
		}
		r := remotes.Remote{Name: fs.Arg(0), Target: fs.Arg(1)}
		list, existed, err := remotes.Add(list, r)
		if err != nil {
			return err
		}
		if err := remotes.Save(path, list); err != nil {
			return err
		}
		verb := "adicionada"
		if existed {
			verb = "atualizada"
		}
		fmt.Fprintf(e.out, "VPS %s %s (%s), em %s.\n", r.Name, verb, r.Target, path)
		// confere na hora, sem perguntar nada: o MCP vai entrar do mesmo jeito
		var out, errb bytes.Buffer
		if err := e.ssh(remotes.SSHArgs(r.Target, true, "cais", "version"), strings.NewReader(""), &out, &errb); err != nil {
			fmt.Fprintf(e.out, "Atencao: ssh %s nao respondeu sem perguntar nada (%s). O MCP precisa entrar sozinho: "+
				"a chave no ssh-agent (ou sem senha) e a chave do host ja aceita (entre uma vez com ssh %s).\n",
				r.Target, cmpOr(lastLine(errb.String()), err.Error()), r.Target)
			return nil
		}
		if v := lastLine(out.String()); v != "" {
			fmt.Fprintf(e.out, "Respondeu: %s\n", v)
		} else {
			fmt.Fprintln(e.out, "Respondeu.")
		}
		return nil
	case "list", "":
		if len(list) == 0 {
			fmt.Fprintln(e.out, "Nenhuma VPS. Adicione com: cais remote add NOME cais@vps")
			return nil
		}
		fmt.Fprint(e.out, remotes.List(list))
		return nil
	case "remove":
		if fs.NArg() != 1 {
			return errors.New("uso: cais remote remove NOME")
		}
		list, ok := remotes.Remove(list, fs.Arg(0))
		if !ok {
			return fmt.Errorf("VPS %q nao esta na lista (cais remote list)", fs.Arg(0))
		}
		if err := remotes.Save(path, list); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "VPS %s tirada da lista (nada muda nela).\n", fs.Arg(0))
		return nil
	default:
		return groupError("remote", sub)
	}
}

// runOnRemote e o cais -r NOME [comando...]: a TUI (sem comando) ou o
// comando, na VPS NOME, pelo ssh.
func (e *cli) runOnRemote(name string, args []string) error {
	path, err := e.remotesPath()
	if err != nil {
		return err
	}
	list, err := remotes.Load(path)
	if err != nil {
		return err
	}
	r, ok := remotes.Find(list, name)
	if !ok {
		if len(list) == 0 {
			return fmt.Errorf("VPS %q nao esta na lista: adicione com cais remote add %s cais@vps", name, name)
		}
		return fmt.Errorf("VPS %q nao esta na lista: as VPS sao %s", name, strings.Join(remotes.Names(list), ", "))
	}
	return e.ssh(remotes.SSHArgs(r.Target, false, append([]string{"cais"}, args...)...), e.in, e.out, e.err)
}

// runMCPRemotes e o cais mcp --remotes: um MCP so pras VPS da lista.
func (e *cli) runMCPRemotes() error {
	path, err := e.remotesPath()
	if err != nil {
		return err
	}
	list, err := remotes.Load(path)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s, closeAll, err := mcpremote.New(ctx, list, mcpremote.SSH, version)
	if err != nil {
		return err
	}
	defer closeAll()
	return s.Run(ctx, &mcp.StdioTransport{})
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}
