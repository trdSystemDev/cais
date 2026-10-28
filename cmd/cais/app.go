package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/compose"
	"github.com/trdSystemDev/cais/internal/rpc"
)

func runApps(args []string) error {
	fs, socket := newFlags("apps")
	fs.Parse(args)
	c, err := connect(*socket)
	if err != nil {
		return err
	}
	defer c.Close()

	list, err := c.CheckApps(context.Background())
	if err != nil {
		return err
	}
	if list.Error != "" {
		return errors.New(list.Error)
	}
	if len(list.List) == 0 {
		fmt.Println("Nenhum app. Crie com: cais app create <nome>")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "APP\tCONTAINERS\tROTAS\tULTIMO DEPLOY")
	for _, a := range list.List {
		running := 0
		for _, ct := range a.Containers {
			if ct.State == "running" {
				running++
			}
		}
		var hosts []string
		for _, r := range a.Routes {
			hosts = append(hosts, r.Host+r.Path)
		}
		last := "nunca"
		if a.Last != nil {
			last = a.Last.At.Local().Format("02/01 15:04")
			if a.Last.Error != "" {
				last += " (falhou"
				if a.Last.RolledBack {
					last += ", anterior no ar"
				}
				last += ")"
			}
		}
		if a.Deploying {
			last = "deploy em andamento"
		}
		fmt.Fprintf(w, "%s\t%d/%d rodando\t%s\t%s\n", a.Name, running, len(a.Containers), strings.Join(hosts, " "), last)
	}
	return w.Flush()
}

func runApp(args []string) error {
	sub, args := subcommand(args)
	fs, socket := newFlags("app " + sub)
	fs.Parse(args)
	need := map[string][2]int{ // minimo e maximo de argumentos
		"create": {1, 1}, "routes": {1, -1}, "deploy": {1, 1},
		"restart": {1, 2}, "stop": {1, 1}, "logs": {2, 2},
	}
	lim, ok := need[sub]
	if !ok {
		return fmt.Errorf("subcomando desconhecido: app %s (veja: cais help)", sub)
	}
	if n := fs.NArg(); n < lim[0] || (lim[1] >= 0 && n > lim[1]) {
		return fmt.Errorf("argumentos errados pra app %s (veja: cais help)", sub)
	}
	name := fs.Arg(0)

	// o compose vem do stdin; le antes de conectar pra falhar cedo
	var composeData []byte
	if sub == "deploy" {
		var err error
		if composeData, err = io.ReadAll(io.LimitReader(os.Stdin, compose.MaxSize+1)); err != nil {
			return err
		}
		if len(composeData) == 0 {
			return errors.New("mande o docker-compose.yml pelo stdin: cais app deploy <nome> < docker-compose.yml")
		}
	}

	c, err := connect(*socket)
	if err != nil {
		return err
	}
	defer c.Close()
	ctx := context.Background()

	switch sub {
	case "create":
		if _, err := c.CreateApp(ctx, name); err != nil {
			return err
		}
		fmt.Printf("App %s criado. Proximos passos:\n  cais app routes %s %s.<dominio>=web:3000\n  cais app deploy %s < docker-compose.yml\n", name, name, name, name)
	case "routes":
		if fs.NArg() == 1 {
			return printRoutes(ctx, c, name)
		}
		routes, err := parseRoutes(fs.Args()[1:])
		if err != nil {
			return err
		}
		if err := c.SetAppRoutes(ctx, name, routes); err != nil {
			return err
		}
		fmt.Printf("Rotas de %s gravadas.\n", name)
		return printRoutes(ctx, c, name)
	case "deploy":
		fmt.Printf("Deploy de %s (esperando os healthchecks)...\n", name)
		res, err := c.DeployApp(ctx, name, string(composeData))
		if err != nil {
			return err
		}
		fmt.Printf("%s no ar: %s\n", name, strings.Join(res.Images, ", "))
	case "restart":
		if err := c.RestartApp(ctx, name, fs.Arg(1)); err != nil {
			return err
		}
		fmt.Printf("%s reiniciado.\n", name)
	case "stop":
		if err := c.StopApp(ctx, name); err != nil {
			return err
		}
		fmt.Printf("%s parado. Volta com: cais app restart %s\n", name, name)
	case "logs":
		out, err := c.AppLogs(ctx, name, fs.Arg(1))
		if err != nil {
			return err
		}
		fmt.Print(out)
	}
	return nil
}

func printRoutes(ctx context.Context, c *rpc.Client, name string) error {
	list, err := c.CheckApps(ctx)
	if err != nil {
		return err
	}
	for _, a := range list.List {
		if a.Name != name {
			continue
		}
		if len(a.Routes) == 0 {
			fmt.Printf("%s sem rotas (nao responde em nenhum dominio).\n", name)
			return nil
		}
		for _, r := range a.Routes {
			fmt.Printf("  https://%s%s -> %s:%d\n", r.Host, r.Path, r.Service, r.Port)
		}
		return nil
	}
	return fmt.Errorf("app %s nao encontrado", name)
}

// parseRoutes le rotas no formato dominio[/caminho]=service:porta. As
// regras (dominio unico, caminho, porta) sao conferidas no daemon.
func parseRoutes(args []string) ([]apps.Route, error) {
	var routes []apps.Route
	for _, arg := range args {
		left, right, ok := strings.Cut(arg, "=")
		svc, portStr, ok2 := strings.Cut(right, ":")
		port, err := strconv.Atoi(portStr)
		if !ok || !ok2 || err != nil || left == "" || svc == "" {
			return nil, fmt.Errorf("rota %q: use dominio[/caminho]=service:porta (ex: app.exemplo.com.br/api=api:8080)", arg)
		}
		r := apps.Route{Host: left, Service: svc, Port: port}
		if i := strings.Index(left, "/"); i >= 0 {
			r.Host, r.Path = left[:i], left[i:]
		}
		routes = append(routes, r)
	}
	return routes, nil
}
