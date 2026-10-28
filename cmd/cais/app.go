package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/compose"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/detect"
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
	dbServices := fs.String("services", "", "services que usam o banco (padrao: todos)")
	dbExtensions := fs.String("extensions", "", "extensoes do Postgres, separadas por virgula (ex: postgis)")
	tag := fs.String("tag", "", "deploy da imagem nessa tag, com a spec do app")
	yes := fs.Bool("yes", false, "aplica o plano sem perguntar")
	unset := fs.String("unset", "", "variaveis a remover, separadas por virgula")
	apply := fs.Bool("apply", false, "refaz o deploy da tag no ar pra mudanca valer agora")
	pos := parseInterspersed(fs, args)
	arg := func(i int) string {
		if i < len(pos) {
			return pos[i]
		}
		return ""
	}
	need := map[string][2]int{ // minimo e maximo de argumentos
		"create": {1, 1}, "routes": {1, -1}, "deploy": {1, 1},
		"restart": {1, 2}, "stop": {1, 1}, "logs": {2, 2}, "db": {2, -1},
		"init": {2, 2}, "secrets": {1, 1}, "secret": {2, 2}, "env": {1, -1},
		"import": {1, 1}, "ci": {1, 1},
	}
	lim, ok := need[sub]
	if !ok {
		return fmt.Errorf("subcomando desconhecido: app %s (veja: cais help)", sub)
	}
	if n := len(pos); n < lim[0] || (lim[1] >= 0 && n > lim[1]) {
		return fmt.Errorf("argumentos errados pra app %s (veja: cais help)", sub)
	}
	name := arg(0)

	// o compose vem do stdin; le antes de conectar pra falhar cedo
	var composeData []byte
	if sub == "deploy" && *tag == "" {
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
		if len(pos) == 1 {
			return printRoutes(ctx, c, name)
		}
		routes, err := parseRoutes(pos[1:])
		if err != nil {
			return err
		}
		if err := c.SetAppRoutes(ctx, name, routes); err != nil {
			return err
		}
		fmt.Printf("Rotas de %s gravadas.\n", name)
		return printRoutes(ctx, c, name)
	case "init":
		return appInit(ctx, c, name, arg(1), *yes)
	case "import":
		return appImport(ctx, c, name, *yes)
	case "ci":
		fmt.Printf("Abrindo o PR com o .woodpecker.yml em %s...\n", name)
		link, err := c.ProposeAppCI(ctx, name)
		if err != nil {
			return err
		}
		fmt.Printf("PR: %s\nRevise e faca o merge: cada push na branch principal passa a fazer build e deploy.\n", link)
	case "env":
		return appEnv(ctx, c, name, pos[1:], splitList(*unset), *apply)
	case "secrets":
		list, err := c.AppSecrets(ctx, name)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Printf("%s sem secrets.\n", name)
		}
		for _, s := range list {
			state := "ok"
			switch {
			case s.Managed:
				state = "do cais"
			case !s.Set:
				state = "FALTA: cais app secret " + name + " " + s.Name
			}
			fmt.Printf("  %-24s %s\n", s.Name, state)
		}
	case "secret":
		value, err := readSecret("Valor de " + arg(1) + ": ")
		if err != nil {
			return err
		}
		if err := c.SetAppSecret(ctx, name, arg(1), value); err != nil {
			return err
		}
		fmt.Printf("%s gravado. O app le em /run/secrets/%s.\n", arg(1), arg(1))
	case "deploy":
		fmt.Printf("Deploy de %s (esperando os healthchecks)...\n", name)
		var res apps.DeployResult
		var err error
		if *tag != "" {
			res, err = c.DeployAppTag(ctx, name, *tag)
		} else {
			res, err = c.DeployApp(ctx, name, string(composeData))
		}
		if err != nil {
			return err
		}
		fmt.Printf("%s no ar: %s\n", name, strings.Join(res.Images, ", "))
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
		engine := arg(1)
		if engine == "off" {
			if err := c.RemoveAppDatabase(ctx, name); err != nil {
				return err
			}
			fmt.Printf("Banco desligado de %s. O database e os dados continuam la.\n", name)
			return nil
		}
		env, err := parseEnvMap(pos[2:])
		if err != nil {
			return err
		}
		if err := c.SetAppDatabase(ctx, name, apps.Database{Engine: engine, Services: splitList(*dbServices), Env: env, Extensions: splitList(*dbExtensions)}); err != nil {
			return err
		}
		fmt.Printf("Database %s criado no %s e ligado ao app.\n", strings.ReplaceAll(name, "-", "_"), engine)
		fmt.Println("O app nao precisa declarar o banco no compose: a conexao entra pelo cais.")
	case "logs":
		out, err := c.AppLogs(ctx, name, arg(1))
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

// parseEnvMap le VAR=valor (o que a variavel recebe: host, url...).
func parseEnvMap(args []string) (map[string]string, error) {
	if len(args) == 0 {
		return nil, nil
	}
	env := map[string]string{}
	for _, a := range args {
		k, v, ok := strings.Cut(a, "=")
		if !ok || k == "" || v == "" {
			return nil, fmt.Errorf("%q: use VAR=valor (ex: DATABASE_URL=url)", a)
		}
		env[k] = v
	}
	return env, nil
}

// appImport le o repositorio pelo GitHub App (no daemon), mostra o plano e
// aplica com confirmacao; depois oferece o PR com o pipeline.
func appImport(ctx context.Context, c *rpc.Client, link string, yes bool) error {
	fmt.Printf("Lendo %s pelo GitHub App...\n\n", link)
	plan, err := c.PlanAppFromGitHub(ctx, link)
	if err != nil {
		return err
	}
	printAppPlan(plan)
	if !confirm(yes, "\nAplicar esse plano? [s/N] ") {
		fmt.Println("Nada foi mudado.")
		return nil
	}
	if err := c.ApplyApp(ctx, plan); err != nil {
		return err
	}
	fmt.Printf("\n%s configurado (repositorio %s).\n", plan.App, plan.Repo)
	for _, s := range plan.Spec.Secrets {
		fmt.Printf("  falta o secret: cais app secret %s %s\n", plan.App, s)
	}
	if !yes && confirm(false, "\nAbrir um PR no repositorio com o .woodpecker.yml? [s/N] ") {
		pr, err := c.ProposeAppCI(ctx, plan.App)
		if err != nil {
			return err
		}
		fmt.Printf("PR: %s\n", pr)
	} else {
		fmt.Printf("Pipeline: cais app ci %s (abre o PR) ou cais ci pipeline > .woodpecker.yml\n", plan.App)
	}
	return nil
}

func confirm(yes bool, prompt string) bool {
	if yes {
		return true
	}
	fmt.Print(prompt)
	var answer string
	fmt.Scanln(&answer)
	return answer == "s" || answer == "S"
}

// appInit le da pasta so os arquivos da analise, pede o plano ao daemon,
// mostra e aplica com confirmacao.
func appInit(ctx context.Context, c *rpc.Client, name, dir string, yes bool) error {
	files, err := detect.Collect(readLocal(dir))
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("nenhum arquivo do app em %s (Dockerfile, .env.example, package.json...)", dir)
	}

	plan, err := c.PlanApp(ctx, name, files)
	if err != nil {
		return err
	}
	printAppPlan(plan)
	if !plan.Analysis.Supported() {
		fmt.Println("\nA analise nao conseguiu tudo (veja os avisos). Ajuste depois com cais app spec/routes/db.")
	}
	if !yes {
		fmt.Print("\nAplicar esse plano? [s/N] ")
		var answer string
		fmt.Scanln(&answer)
		if answer != "s" && answer != "S" {
			fmt.Println("Nada foi mudado.")
			return nil
		}
	}
	if err := c.ApplyApp(ctx, plan); err != nil {
		return err
	}
	fmt.Printf("\n%s configurado.", name)
	if len(plan.Spec.Secrets) > 0 {
		fmt.Printf(" Informe os secrets antes do primeiro deploy:\n")
		for _, s := range plan.Spec.Secrets {
			fmt.Printf("  cais app secret %s %s\n", name, s)
		}
	} else {
		fmt.Println()
	}
	fmt.Printf("Depois do build da imagem: cais app deploy %s --tag <tag>\n", name)
	return nil
}

func printAppPlan(p apps.Plan) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Printf("Plano pra %s\n\n", p.App)
	fmt.Fprintf(w, "  imagem\t%s:<tag do build>\n", p.Spec.Image)
	if b := p.Spec.Build; b.Context != "" || b.Dockerfile != "" {
		fmt.Fprintf(w, "  build\tpasta %s, %s\n", orDefault(b.Context, "raiz"), orDefault(b.Dockerfile, "Dockerfile"))
	}
	argKeys := make([]string, 0, len(p.Spec.Build.Args))
	for k := range p.Spec.Build.Args {
		argKeys = append(argKeys, k)
	}
	sort.Strings(argKeys)
	for _, k := range argKeys {
		fmt.Fprintf(w, "  build arg\t%s=%s (publico: fica na imagem)\n", k, p.Spec.Build.Args[k])
	}
	if len(p.Spec.Command) > 0 {
		fmt.Fprintf(w, "  comando\t%s\n", strings.Join(p.Spec.Command, " "))
	}
	if p.Spec.Port > 0 {
		fmt.Fprintf(w, "  porta\t%d (%s)\n", p.Spec.Port, p.Analysis.PortSource)
	} else {
		fmt.Fprintf(w, "  porta\tnao descoberta\n")
	}
	for _, r := range p.Routes {
		fmt.Fprintf(w, "  rota\thttps://%s%s -> %s:%d\n", r.Host, r.Path, r.Service, r.Port)
	}
	for _, v := range p.Spec.Volumes {
		fmt.Fprintf(w, "  volume\t%s (persiste entre deploys)\n", v)
	}
	if p.Database != nil {
		fmt.Fprintf(w, "  banco\t%s (%s)\n", p.Database.Engine, p.Analysis.Database.Why)
		if len(p.Database.Extensions) > 0 {
			fmt.Fprintf(w, "  \t  extensoes: %s\n", strings.Join(p.Database.Extensions, ", "))
		}
		keys := make([]string, 0, len(p.Database.Env))
		for k := range p.Database.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "  \t  %s = %s\n", k, p.Database.Env[k])
		}
	} else {
		fmt.Fprintf(w, "  banco\tnenhum (ligue depois com cais app db, se precisar)\n")
	}
	keys := make([]string, 0, len(p.Spec.Env))
	for k := range p.Spec.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(w, "  env\t%s=%s\n", k, p.Spec.Env[k])
	}
	for _, s := range p.Spec.Secrets {
		fmt.Fprintf(w, "  secret\t%s (/run/secrets/%s)\n", s, s)
	}
	w.Flush()
	if len(p.Analysis.Notes) > 0 {
		fmt.Println("\nAvisos:")
		for _, n := range p.Analysis.Notes {
			fmt.Printf("  - %s\n", n)
		}
	}
}

// parseInterspersed aceita flags em qualquer posicao (cais app deploy x
// --tag v1): o pacote flag sozinho para no primeiro argumento posicional.
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

// appEnv mostra ou muda as variaveis do app. Sem mudancas, so mostra.
func appEnv(ctx context.Context, c *rpc.Client, name string, set, unset []string, apply bool) error {
	list, err := c.CheckApps(ctx)
	if err != nil {
		return err
	}
	var a *core.AppStatus
	for i := range list.List {
		if list.List[i].Name == name {
			a = &list.List[i]
		}
	}
	if a == nil {
		return fmt.Errorf("app %s nao encontrado", name)
	}
	if a.Spec == nil {
		return fmt.Errorf("app %s sem spec (modo compose): as variaveis ficam no compose do app", name)
	}
	env := map[string]string{}
	for k, v := range a.Spec.Env {
		env[k] = v
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
	if apply {
		fmt.Printf("Aplicando em %s (deploy da tag %s)...\n", name, a.Tag)
	}
	if err := c.SetAppEnv(ctx, name, env, apply); err != nil {
		return err
	}
	if apply && a.Tag != "" {
		fmt.Printf("Variaveis de %s gravadas e no ar.\n", name)
	} else {
		fmt.Printf("Variaveis de %s gravadas; valem no proximo deploy (ou use -apply).\n", name)
	}
	return nil
}

// readLocal le arquivos da analise de uma pasta local.
func readLocal(dir string) func(string) ([]byte, bool, error) {
	return func(p string) ([]byte, bool, error) {
		data, err := os.ReadFile(filepath.Join(dir, p))
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return data, err == nil, err
	}
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
