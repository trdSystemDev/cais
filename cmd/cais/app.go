package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/github"
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
		fmt.Println("Nenhum app. Crie com: cais app create <nome> <dono/repo>")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "APP\tREPOSITORIO\tCONTAINERS\tNO AR\tULTIMO DEPLOY")
	for _, a := range list.List {
		running := 0
		for _, ct := range a.Containers {
			if ct.State == "running" {
				running++
			}
		}
		commit := "-"
		if a.Commit != "" {
			commit = short(a.Commit)
		}
		last := "nunca"
		if a.Last != nil {
			last = a.Last.At.Local().Format("02/01 15:04") + " " + short(a.Last.Commit)
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
		fmt.Fprintf(w, "%s\t%s\t%d/%d rodando\t%s\t%s\n", a.Name, a.Repo, running, len(a.Containers), commit, last)
	}
	return w.Flush()
}

func short(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

func runApp(args []string) error {
	sub, args := subcommand(args)
	fs, socket := newFlags("app " + sub)
	branch := fs.String("branch", "", "branch do deploy (padrao: a principal do repositorio)")
	composeFile := fs.String("compose", "", "compose de producao, a partir da raiz (padrao docker-compose.yml)")
	ref := fs.String("ref", "", "branch ou commit do deploy (padrao: o mais novo da branch do app)")
	unset := fs.String("unset", "", "variaveis a remover, separadas por virgula")
	volumes := fs.Bool("volumes", false, "delete: apaga tambem os volumes do compose (os dados)")
	yes := fs.Bool("yes", false, "delete: nao pede confirmacao")
	dbEnv := fs.String("env", "", "db: variavel da URL do banco (padrao DATABASE_URL)")
	pos := parseInterspersed(fs, args)
	arg := func(i int) string {
		if i < len(pos) {
			return pos[i]
		}
		return ""
	}
	need := map[string][2]int{ // minimo e maximo de argumentos
		"create": {2, 2}, "deploy": {1, 1}, "guide": {1, 1}, "env": {1, -1},
		"secrets": {1, 1}, "secret": {2, 2}, "restart": {1, 2}, "stop": {1, 1}, "logs": {2, 2}, "delete": {1, 1}, "db": {1, 3},
	}
	lim, ok := need[sub]
	if !ok {
		return fmt.Errorf("subcomando desconhecido: app %s (veja: cais help)", sub)
	}
	if n := len(pos); n < lim[0] || (lim[1] >= 0 && n > lim[1]) {
		return fmt.Errorf("argumentos errados pra app %s (veja: cais help)", sub)
	}
	name := arg(0)

	c, err := connect(*socket)
	if err != nil {
		return err
	}
	defer c.Close()
	ctx := context.Background()

	switch sub {
	case "create":
		repo, err := github.ParseRepo(arg(1))
		if err != nil {
			return err
		}
		a, err := c.CreateApp(ctx, apps.App{Name: name, Repo: repo.String(), Branch: *branch, Compose: *composeFile})
		if err != nil {
			return err
		}
		fmt.Printf("App %s criado (github.com/%s, compose %s).\n\n", a.Name, a.Repo, a.ComposeFile())
		fmt.Printf("Modelo de compose com labels, redes e secrets: cais app guide %s\n", a.Name)
		fmt.Printf("Variaveis: cais app env %s VAR=valor ...   Secrets: cais app secret %s <nome>\n", a.Name, a.Name)
		fmt.Printf("Deploy: cais app deploy %s\n", a.Name)
	case "deploy":
		fmt.Printf("Deploy de %s: baixando o codigo, build (a versao atual continua no ar) e up...\n", name)
		res, err := c.DeployApp(ctx, name, *ref)
		for _, w := range res.Warnings {
			fmt.Printf("  aviso: %s\n", w)
		}
		if err != nil {
			return err
		}
		fmt.Printf("%s no ar no commit %s.\n", name, short(res.Commit))
	case "guide":
		text, err := c.AppGuide(ctx, name)
		if err != nil {
			return err
		}
		fmt.Print(text)
	case "env":
		return appEnv(ctx, c, name, pos[1:], splitList(*unset))
	case "secrets":
		list, err := c.AppSecrets(ctx, name)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Printf("%s sem secrets.\n", name)
		}
		for _, s := range list {
			extra := ""
			if s.Database != "" {
				extra = "  URL do banco " + s.Database + " (gerenciado pelo cais)"
			}
			fmt.Printf("  %s  (${CAIS_SECRETS}/%s)%s\n", s.Name, s.Name, extra)
		}
	case "secret":
		value, err := readSecret("Valor de " + arg(1) + ": ")
		if err != nil {
			return err
		}
		if err := c.SetAppSecret(ctx, name, arg(1), value); err != nil {
			return err
		}
		fmt.Printf("%s gravado em ${CAIS_SECRETS}/%s (vale no proximo deploy).\n", arg(1), arg(1))
	case "restart":
		if err := c.RestartApp(ctx, name, arg(1)); err != nil {
			return err
		}
		fmt.Printf("%s reiniciado.\n", name)
	case "stop":
		if err := c.StopApp(ctx, name); err != nil {
			return err
		}
		fmt.Printf("%s parado. Volta com: cais app restart %s\n", name, name)
	case "db":
		return appDB(ctx, c, name, pos[1:], *dbEnv)
	case "delete":
		what := "containers, imagens, codigo, variaveis e secrets"
		if *volumes {
			what += " e os VOLUMES (dados)"
		}
		if !*yes {
			fmt.Printf("Excluir %s apaga %s. O database da infra fica.\nDigite o nome do app pra confirmar: ", name, what)
			var typed string
			fmt.Scanln(&typed)
			if strings.TrimSpace(typed) != name {
				return errors.New("nome diferente: nada foi apagado")
			}
		}
		if err := c.DeleteApp(ctx, name, *volumes); err != nil {
			return err
		}
		fmt.Printf("%s excluido.\n", name)
	case "logs":
		out, err := c.AppLogs(ctx, name, arg(1))
		if err != nil {
			return err
		}
		fmt.Print(out)
	}
	return nil
}

// appEnv mostra ou muda as variaveis do app. Sem mudancas, so mostra.
func appEnv(ctx context.Context, c *rpc.Client, name string, set, unset []string) error {
	env, err := c.AppEnv(ctx, name)
	if err != nil {
		return err
	}
	if len(set) == 0 && len(unset) == 0 {
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) == 0 {
			fmt.Printf("%s sem variaveis.\n", name)
		}
		for _, k := range keys {
			fmt.Printf("  %s=%s\n", k, env[k])
		}
		return nil
	}
	for _, kv := range set {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return fmt.Errorf("%q: use VAR=valor", kv)
		}
		env[k] = v
	}
	for _, k := range unset {
		delete(env, k)
	}
	if err := c.SetAppEnv(ctx, name, env); err != nil {
		return err
	}
	fmt.Printf("Variaveis de %s gravadas; valem no proximo deploy.\n", name)
	return nil
}

// appDB mostra, liga ou desliga os bancos da infra do app:
// cais app db <app> | <engine/nome> [-env VAR] | off <engine/nome>
func appDB(ctx context.Context, c *rpc.Client, app string, args []string, env string) error {
	parse := func(s string) (string, string, error) {
		engine, name, ok := strings.Cut(s, "/")
		if !ok || engine == "" || name == "" {
			return "", "", fmt.Errorf("%q: use engine/nome, ex: postgres/%s", s, app)
		}
		return engine, name, nil
	}
	switch {
	case len(args) == 0:
		list, err := c.CheckApps(ctx)
		if err != nil {
			return err
		}
		for _, a := range list.List {
			if a.Name != app {
				continue
			}
			if len(a.Databases) == 0 {
				fmt.Printf("%s nao usa banco da infra. Ligue com: cais app db %s postgres/<database>\n", app, app)
			}
			for _, l := range a.Databases {
				fmt.Printf("  %-22s %s  (e ${CAIS_SECRETS}/%s)\n", l.ID(), l.Env, l.File())
			}
			return nil
		}
		return fmt.Errorf("app %s nao existe", app)
	case args[0] == "off" && len(args) == 2:
		engine, name, err := parse(args[1])
		if err != nil {
			return err
		}
		if err := c.UnlinkAppDatabase(ctx, app, engine, name); err != nil {
			return err
		}
		fmt.Printf("%s desligado do banco %s (o database e os dados ficam); vale no proximo deploy.\n", app, args[1])
	case len(args) == 1:
		engine, name, err := parse(args[0])
		if err != nil {
			return err
		}
		if err := c.LinkAppDatabase(ctx, app, engine, name, env); err != nil {
			return err
		}
		if env == "" {
			env = apps.DefaultDBEnv
		}
		fmt.Printf("%s ligado ao banco %s: a URL vai em %s e em ${CAIS_SECRETS}/%s no proximo deploy.\n", app, args[0], env, strings.ToLower(env))
	default:
		return errors.New("uso: cais app db <app> [engine/nome [-env VAR] | off engine/nome]")
	}
	return nil
}

// runDB cria e lista bancos na infra.
func runDB(args []string) error {
	sub, args := subcommand(args)
	fs, socket := newFlags("db " + sub)
	exts := fs.String("extensions", "", "extensoes do Postgres, separadas por virgula (ex: postgis)")
	app := fs.String("app", "", "liga o database a este app (a URL vai em -env no deploy)")
	env := fs.String("env", "", "variavel da URL no app (padrao DATABASE_URL)")
	pos := parseInterspersed(fs, args)
	c, err := connect(*socket)
	if err != nil {
		return err
	}
	defer c.Close()
	ctx := context.Background()

	switch sub {
	case "create":
		if len(pos) != 2 {
			return errors.New("uso: cais db create postgres|mysql <nome> [-extensions postgis]")
		}
		info, err := c.CreateDatabase(ctx, core.DatabaseRequest{Engine: pos[0], Name: pos[1], Extensions: splitList(*exts), App: *app, Env: *env})
		if err != nil {
			return err
		}
		for _, u := range info.Apps {
			defer fmt.Printf("Ligado ao app %s: a URL vai em %s no proximo deploy.\n", u.App, u.Env)
		}
		fmt.Printf("Database %s criado no %s.\n\n", info.Name, info.Engine)
		printDB(info)
		fmt.Printf("\nO app chega no banco pela rede %s (external no compose).\n", info.Network)
	case "list":
		list, err := c.Databases(ctx)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Println("Nenhum database criado pelo cais. Crie com: cais db create postgres <nome>")
		}
		for i, info := range list {
			if i > 0 {
				fmt.Println()
			}
			printDB(info)
		}
	default:
		return fmt.Errorf("subcomando desconhecido: db %s (veja: cais help)", sub)
	}
	return nil
}

func printDB(info core.DatabaseInfo) {
	defer func() {
		for _, u := range info.Apps {
			fmt.Printf("  app       %s (%s)\n", u.App, u.Env)
		}
	}()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "  %s\t%s\n", "engine", info.Engine)
	fmt.Fprintf(w, "  %s\t%s\n", "database", info.Name)
	fmt.Fprintf(w, "  %s\t%s\n", "usuario", info.User)
	fmt.Fprintf(w, "  %s\t%s\n", "senha", info.Password)
	fmt.Fprintf(w, "  %s\t%s:%d (rede %s)\n", "host", info.Host, info.Port, info.Network)
	fmt.Fprintf(w, "  %s\t%s\n", "url", info.URL)
	w.Flush()
}

// parseInterspersed aceita flags em qualquer posicao (cais app deploy x
// -ref main): o pacote flag sozinho para no primeiro argumento posicional.
func parseInterspersed(fs *flag.FlagSet, args []string) []string {
	var pos []string
	for {
		fs.Parse(args)
		if fs.NArg() == 0 {
			return pos
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}
