package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/databases"
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
	fmt.Fprintln(w, "APP\tREPOSITORIO\tDEPLOY\tCONTAINERS\tNO AR\tULTIMO DEPLOY")
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
			switch {
			case a.Last.PromotedFrom != "":
				last += " promovido de " + a.Last.PromotedFrom
			case a.Last.Auto:
				last += " automatico"
			}
		}
		if a.Deploying {
			last = "deploy em andamento"
		}
		mode := "manual"
		if a.AutoDeploy {
			mode = "auto (" + cmpOr(a.Branch, "principal") + ")"
		}
		name := a.Name
		if a.PromoteTo != "" {
			name += " (homolog de " + a.PromoteTo + ")"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d/%d rodando\t%s\t%s\n", name, a.Repo, mode, running, len(a.Containers), commit, last)
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
	importEnv := fs.String("import", "", "env: arquivo .env pra juntar nas variaveis (- le do stdin)")
	volumes := fs.Bool("volumes", false, "delete: apaga tambem os volumes do compose (os dados)")
	yes := fs.Bool("yes", false, "delete: nao pede confirmacao; promote: aceita voltar a producao pra um commit mais velho")
	promoteTo := fs.String("promote-to", "", "create/set: o app e homologacao deste (a producao); set: off desfaz")
	auto := fs.String("auto", "", "create/set: deploy automatico a cada commit na branch (on ou off)")
	pos := parseInterspersed(fs, args)
	arg := func(i int) string {
		if i < len(pos) {
			return pos[i]
		}
		return ""
	}
	need := map[string][2]int{ // minimo e maximo de argumentos
		"create": {2, 2}, "deploy": {1, 1}, "promote": {1, 1}, "set": {1, 1}, "guide": {1, 1}, "env": {1, -1},
		"secrets": {1, 1}, "secret": {2, 2}, "restart": {1, 2}, "stop": {1, 1}, "logs": {2, 2}, "delete": {1, 1}, "db": {1, 3}, "bucket": {1, 3},
	}
	lim, ok := need[sub]
	if !ok {
		return groupError("app", sub)
	}
	if n := len(pos); n < lim[0] || (lim[1] >= 0 && n > lim[1]) {
		return fmt.Errorf("argumentos errados\nuso: %s (detalhes: cais app %s -h)", usageOf("app "+sub), sub)
	}
	name := arg(0)
	autoOn, err := onOff(*auto)
	if err != nil {
		return err
	}

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
		a, err := c.CreateApp(ctx, apps.App{Name: name, Repo: repo.String(), Branch: *branch, Compose: *composeFile,
			PromoteTo: *promoteTo, AutoDeploy: autoOn != nil && *autoOn})
		if err != nil {
			return err
		}
		fmt.Printf("App %s criado (github.com/%s, compose %s).\n", a.Name, a.Repo, a.ComposeFile())
		if a.PromoteTo != "" {
			fmt.Printf("Homologacao de %s: valide aqui e promova com: cais app promote %s\n", a.PromoteTo, a.Name)
		}
		if a.AutoDeploy {
			fmt.Printf("Deploy automatico a cada commit na branch %s.\n", cmpOr(a.Branch, "principal"))
		}
		fmt.Println()
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
	case "promote":
		return appPromote(ctx, c, name, *yes)
	case "set":
		return appSet(ctx, c, name, fs, *promoteTo, autoOn)
	case "guide":
		text, err := c.AppGuide(ctx, name)
		if err != nil {
			return err
		}
		fmt.Print(text)
	case "env":
		return appEnv(ctx, c, name, pos[1:], splitList(*unset), *importEnv)
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
			if s.Infra != "" {
				extra = "  credencial de " + s.Infra + " (gravada pelo cais)"
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
		return appDB(ctx, c, name, pos[1:])
	case "bucket":
		return appBucket(ctx, c, name, pos[1:])
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

// onOff le o valor de -auto (vazio: nao mexe).
func onOff(v string) (*bool, error) {
	switch v {
	case "":
		return nil, nil
	case "on":
		t := true
		return &t, nil
	case "off":
		f := false
		return &f, nil
	}
	return nil, fmt.Errorf("-auto e on ou off, nao %q", v)
}

// appPromote mostra o que a promocao faz e sobe na producao o commit
// validado na homologacao.
func appPromote(ctx context.Context, c *rpc.Client, name string, yes bool) error {
	p, err := c.CheckPromotion(ctx, name)
	if err != nil {
		return err
	}
	switch p.State {
	case apps.PromoteBlocked:
		return errors.New(p.Reason)
	case apps.PromoteSame:
		fmt.Printf("Nada a promover: %s.\n", p.Reason)
		return nil
	case apps.PromoteOlder:
		if !yes {
			return fmt.Errorf("%s.\nPra promover mesmo assim: cais app promote %s -yes", p.Reason, name)
		}
	}
	fmt.Printf("Promovendo %s (validado em %s) pra %s: build com a versao atual no ar e up...\n", short(p.Deploy), name, p.To)
	res, err := c.PromoteApp(ctx, name, yes)
	for _, w := range res.Warnings {
		fmt.Printf("  aviso: %s\n", w)
	}
	if err != nil {
		return err
	}
	fmt.Printf("%s no ar no commit %s.\n", p.To, short(res.Commit))
	return nil
}

// appSet troca branch, compose, producao e deploy automatico do app; so o
// que veio nas flags muda.
func appSet(ctx context.Context, c *rpc.Client, name string, fs *flag.FlagSet, promoteTo string, auto *bool) error {
	list, err := c.CheckApps(ctx)
	if err != nil {
		return err
	}
	var a *apps.App
	for i := range list.List {
		if list.List[i].Name == name {
			a = &list.List[i].App
		}
	}
	if a == nil {
		return fmt.Errorf("app %s nao existe", name)
	}
	changed := false
	fs.Visit(func(f *flag.Flag) {
		v := f.Value.String()
		switch f.Name {
		case "branch":
			a.Branch, changed = v, true
		case "compose":
			a.Compose, changed = v, true
		case "promote-to":
			a.PromoteTo, changed = v, true
			if v == "off" {
				a.PromoteTo = ""
			}
		case "auto":
			a.AutoDeploy, changed = *auto, true
		}
	})
	if !changed {
		return errors.New("nada a mudar: use -branch, -compose, -promote-to ou -auto")
	}
	if err := c.UpdateApp(ctx, *a); err != nil {
		return err
	}
	mode := "manual"
	if a.AutoDeploy {
		mode = "automatico a cada commit na " + cmpOr(a.Branch, "branch principal")
	}
	fmt.Printf("%s: branch %s, compose %s, deploy %s.\n", a.Name, cmpOr(a.Branch, "principal"), a.ComposeFile(), mode)
	if a.PromoteTo != "" {
		fmt.Printf("Homologacao de %s (cais app promote %s).\n", a.PromoteTo, a.Name)
	}
	return nil
}

// appEnv mostra ou muda as variaveis do app. Sem mudancas, so mostra.
func appEnv(ctx context.Context, c *rpc.Client, name string, set, unset []string, importFile string) error {
	env, err := c.AppEnv(ctx, name)
	if err != nil {
		return err
	}
	if importFile != "" {
		var data []byte
		if importFile == "-" {
			data, err = io.ReadAll(os.Stdin)
		} else {
			data, err = os.ReadFile(importFile)
		}
		if err != nil {
			return err
		}
		imported, err := apps.ParseEnv(data)
		if err != nil {
			return fmt.Errorf("%s: %w", importFile, err)
		}
		maps.Copy(env, imported)
	}
	if len(set) == 0 && len(unset) == 0 && importFile == "" {
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
// cais app db <app> | <engine/nome> | off <engine/nome>
func appDB(ctx context.Context, c *rpc.Client, app string, args []string) error {
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
				fmt.Printf("  %s  (secrets %s)\n", l.ID(), strings.Join(l.Secrets(), " e "))
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
		if err := c.LinkAppDatabase(ctx, app, engine, name); err != nil {
			return err
		}
		l := apps.DBLink{Engine: engine, Name: name}
		fmt.Printf("%s ligado ao banco %s: secrets %s em ${CAIS_SECRETS}, valem no proximo deploy.\n", app, args[0], strings.Join(l.Secrets(), " e "))
	default:
		return errors.New("uso: cais app db <app> [engine/nome | off engine/nome]")
	}
	return nil
}

// appBucket mostra, liga ou desliga os buckets do Garage do app:
// cais app bucket <app> | <bucket> | off <bucket>
func appBucket(ctx context.Context, c *rpc.Client, app string, args []string) error {
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
			if len(a.Buckets) == 0 {
				fmt.Printf("%s nao usa bucket do Garage. Ligue com: cais app bucket %s <bucket>\n", app, app)
			}
			for _, l := range a.Buckets {
				fmt.Printf("  %s  (secrets %s)\n", l.Name, strings.Join(l.Secrets(), " e "))
			}
			return nil
		}
		return fmt.Errorf("app %s nao existe", app)
	case args[0] == "off" && len(args) == 2:
		if err := c.UnlinkAppBucket(ctx, app, args[1]); err != nil {
			return err
		}
		fmt.Printf("%s desligado do bucket %s (o bucket e os arquivos ficam); vale no proximo deploy.\n", app, args[1])
	case len(args) == 1:
		if err := c.LinkAppBucket(ctx, app, args[0]); err != nil {
			return err
		}
		l := apps.BucketLink{Name: args[0]}
		fmt.Printf("%s ligado ao bucket %s: secrets %s em ${CAIS_SECRETS}, valem no proximo deploy.\n", app, args[0], strings.Join(l.Secrets(), " e "))
	default:
		return errors.New("uso: cais app bucket <app> [<bucket> | off <bucket>]")
	}
	return nil
}

// confirmName pede o nome digitado antes de apagar algo (pulado com -yes).
func confirmName(yes bool, name, warning string) error {
	if yes {
		return nil
	}
	fmt.Printf("%s\nDigite %s pra confirmar: ", warning, name)
	var typed string
	fmt.Scanln(&typed)
	if strings.TrimSpace(typed) != name {
		return errors.New("nome diferente: nada foi apagado")
	}
	return nil
}

// runBucket cria, lista e exclui buckets no Garage da infra.
func runBucket(args []string) error {
	sub, args := subcommand(args)
	fs, socket := newFlags("bucket " + sub)
	yes := fs.Bool("yes", false, "delete: nao pede confirmacao")
	pos := parseInterspersed(fs, args)
	if sub != "create" && sub != "list" && sub != "delete" {
		return groupError("bucket", sub)
	}
	c, err := connect(*socket)
	if err != nil {
		return err
	}
	defer c.Close()
	ctx := context.Background()

	switch sub {
	case "create":
		if len(pos) != 1 {
			return errors.New("uso: cais bucket create <nome>")
		}
		info, err := c.CreateBucket(ctx, pos[0])
		if err != nil {
			return err
		}
		fmt.Printf("Bucket %s criado (endpoint %s, regiao %s, path-style, rede %s).\n", info.Name, info.Endpoint, info.Region, info.Network)
		fmt.Printf("Ligue ao app com: cais app bucket <app> %s (o app recebe os secrets %s).\n", info.Name, strings.Join(info.Secrets, " e "))
	case "delete":
		if len(pos) != 1 {
			return errors.New("uso: cais bucket delete <nome> [-yes]")
		}
		if err := confirmName(*yes, pos[0], "Excluir o bucket "+pos[0]+" apaga o bucket e a chave dele (o Garage so exclui bucket vazio). Nao tem volta."); err != nil {
			return err
		}
		if err := c.DeleteBucket(ctx, pos[0]); err != nil {
			return err
		}
		fmt.Printf("Bucket %s excluido.\n", pos[0])
	case "list":
		list, err := c.Buckets(ctx)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Println("Nenhum bucket criado pelo cais. Crie com: cais bucket create <nome>")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "BUCKET\tSECRETS\tESPACO\tAPP")
		for _, b := range list {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", b.Name, strings.Join(b.Secrets, ", "), cmpOr(b.Size, "?"), cmpOr(strings.Join(b.Apps, ", "), "-"))
		}
		w.Flush()
	}
	return nil
}

// runDB cria e lista bancos na infra.
func runDB(args []string) error {
	sub, args := subcommand(args)
	fs, socket := newFlags("db " + sub)
	exts := fs.String("extensions", "", "extensoes do Postgres, separadas por virgula (ex: postgis)")
	yes := fs.Bool("yes", false, "delete: nao pede confirmacao")
	pos := parseInterspersed(fs, args)
	if sub != "create" && sub != "list" && sub != "delete" {
		return groupError("db", sub)
	}
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
		info, err := c.CreateDatabase(ctx, core.DatabaseRequest{Engine: pos[0], Name: pos[1], Extensions: splitList(*exts)})
		if err != nil {
			return err
		}
		fmt.Printf("Database %s criado no %s (host %s:%d, rede %s).\n", info.Name, info.Engine, info.Host, info.Port, info.Network)
		fmt.Printf("Ligue ao app com: cais app db <app> %s/%s (o app recebe os secrets %s).\n", info.Engine, info.Name, strings.Join(info.Secrets, " e "))
	case "delete":
		if len(pos) != 2 {
			return errors.New("uso: cais db delete postgres|mysql <nome> [-yes]")
		}
		if err := confirmName(*yes, pos[1], "Excluir o database "+pos[0]+"/"+pos[1]+" apaga todos os dados dele e o usuario. Nao tem volta."); err != nil {
			return err
		}
		if err := c.DeleteDatabase(ctx, pos[0], pos[1]); err != nil {
			return err
		}
		fmt.Printf("Database %s/%s excluido.\n", pos[0], pos[1])
	case "list":
		list, err := c.Databases(ctx)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Println("Nenhum database criado pelo cais. Crie com: cais db create postgres <nome>")
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "DATABASE\tSECRETS\tESPACO\tAPP")
		for _, d := range list {
			fmt.Fprintf(w, "%s/%s\t%s\t%s\t%s\n", d.Engine, d.Name, strings.Join(d.Secrets, ", "), dbSize(d), cmpOr(strings.Join(d.Apps, ", "), "-"))
		}
		w.Flush()
	}
	return nil
}

// dbSize e o espaco ocupado, ou ? se a instancia nao respondeu.
func dbSize(d core.DatabaseInfo) string {
	if d.Size == nil {
		return "?"
	}
	return databases.HumanSize(*d.Size)
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
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
