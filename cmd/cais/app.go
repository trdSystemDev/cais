package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/docker"
	"github.com/trdSystemDev/cais/internal/github"
	"github.com/trdSystemDev/cais/internal/ptyio"
)

func (e *cli) runApps(args []string) error {
	fs, socket := e.newFlags("apps")
	if err := e.parse(fs, args); err != nil {
		return err
	}
	c, err := e.connect(*socket)
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
		fmt.Fprintln(e.out, "Nenhum app. Crie com: cais app create <nome> <dono/repo>")
		return nil
	}
	w := tabwriter.NewWriter(e.out, 0, 0, 2, ' ', 0)
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
				if at, ok := a.AutoRetryAt(); ok && a.AutoDeploy {
					last += ", tenta de novo as " + at.Local().Format("15:04")
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
		if a.BuildQueued {
			last = "deploy na fila de build"
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

func (e *cli) runApp(args []string) error {
	sub, args := subcommand(args)
	fs, socket := e.newFlags("app " + sub)
	branch := fs.String("branch", "", "branch do deploy (padrao: a principal do repositorio)")
	composeFile := fs.String("compose", "", "compose de producao, a partir da raiz (padrao docker-compose.yml)")
	ref := fs.String("ref", "", "branch ou commit do deploy (padrao: o mais novo da branch do app)")
	unset := fs.String("unset", "", "variaveis a remover, separadas por virgula")
	importEnv := fs.String("import", "", "env: arquivo .env pra juntar nas variaveis (- le do stdin)")
	show := fs.Bool("show", false, "env: mostra os valores (sem ela, so os nomes)")
	volumes := fs.Bool("volumes", false, "delete: apaga tambem os volumes do compose (os dados)")
	yes := fs.Bool("yes", false, "delete: nao pede confirmacao; promote: aceita voltar a producao pra um commit mais velho")
	promoteTo := fs.String("promote-to", "", "create/set: o app e homologacao deste (a producao); set: off desfaz")
	auto := fs.String("auto", "", "create/set: deploy automatico a cada commit na branch (on ou off)")
	pos, err := e.parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	arg := func(i int) string {
		if i < len(pos) {
			return pos[i]
		}
		return ""
	}
	need := map[string][2]int{ // minimo e maximo de argumentos
		"create": {2, 2}, "deploy": {1, 1}, "rollback": {1, 2}, "history": {1, 1}, "promote": {1, 1}, "set": {1, 1}, "guide": {1, 1}, "env": {1, -1},
		"restart": {1, 2}, "stop": {1, 1}, "logs": {2, 2}, "shell": {1, 2}, "delete": {1, 1}, "db": {1, 3}, "bucket": {1, 3},
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

	c, err := e.connect(*socket)
	if err != nil {
		return err
	}
	defer c.Close()
	ctx := context.Background()

	switch sub {
	case "create":
		return e.appCreate(ctx, c, name, arg(1), apps.App{Branch: *branch, Compose: *composeFile, PromoteTo: *promoteTo, AutoDeploy: autoOn != nil && *autoOn})
	case "deploy":
		return e.appDeploy(ctx, c, name, *ref)
	case "rollback":
		return e.appRollback(ctx, c, name, arg(1))
	case "history":
		return e.appHistory(ctx, c, name)
	case "promote":
		return e.appPromote(ctx, c, name, *yes)
	case "set":
		return e.appSet(ctx, c, name, fs, *promoteTo, autoOn)
	case "guide":
		text, err := c.AppGuide(ctx, name)
		if err != nil {
			return err
		}
		fmt.Fprint(e.out, text)
	case "env":
		return e.appEnv(ctx, c, name, pos[1:], splitList(*unset), *importEnv, *show)
	case "restart":
		if err := c.RestartApp(ctx, name, arg(1)); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "%s reiniciado.\n", name)
	case "stop":
		if err := c.StopApp(ctx, name); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "%s parado. Volta com: cais app restart %s\n", name, name)
	case "db":
		return e.appDB(ctx, c, name, pos[1:])
	case "bucket":
		return e.appBucket(ctx, c, name, pos[1:])
	case "delete":
		return e.appDelete(ctx, c, name, *volumes, *yes)
	case "shell":
		return e.appShell(ctx, c, name, arg(1))
	case "logs":
		out, err := c.AppLogs(ctx, name, arg(1), docker.LogsOptions{})
		if err != nil {
			return err
		}
		fmt.Fprint(e.out, out)
	}
	return nil
}

// appCreate cadastra o app e mostra os proximos passos.
func (e *cli) appCreate(ctx context.Context, c daemonAPI, name, repoArg string, base apps.App) error {
	repo, err := github.ParseRepo(repoArg)
	if err != nil {
		return err
	}
	base.Name, base.Repo = name, repo.String()
	a, err := c.CreateApp(ctx, base)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.out, "App %s criado (github.com/%s, compose %s).\n", a.Name, a.Repo, a.ComposeFile())
	if a.PromoteTo != "" {
		fmt.Fprintf(e.out, "Homologacao de %s: valide aqui e promova com: cais app promote %s\n", a.PromoteTo, a.Name)
	}
	if a.AutoDeploy {
		fmt.Fprintf(e.out, "Deploy automatico a cada commit na branch %s.\n", cmpOr(a.Branch, "principal"))
	}
	fmt.Fprintln(e.out)
	fmt.Fprintf(e.out, "Modelo de compose com labels, redes e o cais run: cais app guide %s\n", a.Name)
	fmt.Fprintf(e.out, "Variaveis: cais app env %s VAR=valor ...   Senha sem eco: cais app env %s VAR\n", a.Name, a.Name)
	fmt.Fprintf(e.out, "Deploy: cais app deploy %s\n", a.Name)
	return nil
}

// appDeploy faz o deploy e mostra os avisos do compose.
func (e *cli) appDeploy(ctx context.Context, c daemonAPI, name, ref string) error {
	fmt.Fprintf(e.out, "Deploy de %s: baixando o codigo, build (a versao atual continua no ar) e up...\n", name)
	res, err := c.DeployApp(ctx, name, ref)
	for _, w := range res.Warnings {
		fmt.Fprintf(e.out, "  aviso: %s\n", w)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(e.out, "%s no ar no commit %s.\n", name, short(res.Commit))
	return nil
}

// appRollback volta pra um deploy anterior.
func (e *cli) appRollback(ctx context.Context, c daemonAPI, name, commit string) error {
	fmt.Fprintf(e.out, "Voltando %s pra %s (imagens guardadas quando existem, sem queda)...\n", name, cmpOr(commit, "o deploy anterior"))
	res, err := c.RollbackApp(ctx, name, commit)
	if err != nil {
		return err
	}
	how := "com as imagens guardadas"
	if !res.Images {
		how = "com build (as imagens desse commit nao estavam guardadas)"
	}
	fmt.Fprintf(e.out, "%s no ar no commit %s, %s.\n", name, short(res.Commit), how)
	fmt.Fprintln(e.out, "As variaveis sao as de agora. O deploy automatico (se ligado) espera um commit novo na branch.")
	return nil
}

// appDelete exclui o app, pedindo o nome antes (sem -yes).
func (e *cli) appDelete(ctx context.Context, c daemonAPI, name string, volumes, yes bool) error {
	what := "containers, imagens, codigo e variaveis"
	if volumes {
		what += " e os VOLUMES (dados)"
	}
	if !yes {
		fmt.Fprintf(e.out, "Excluir %s apaga %s. O database da infra fica.\nDigite o nome do app pra confirmar: ", name, what)
		var typed string
		fmt.Fscanln(e.in, &typed)
		if strings.TrimSpace(typed) != name {
			return errors.New("nome diferente: nada foi apagado")
		}
	}
	if err := c.DeleteApp(ctx, name, volumes); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "%s excluido.\n", name)
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
func (e *cli) appPromote(ctx context.Context, c daemonAPI, name string, yes bool) error {
	p, err := c.CheckPromotion(ctx, name)
	if err != nil {
		return err
	}
	switch p.State {
	case apps.PromoteBlocked:
		return errors.New(p.Reason)
	case apps.PromoteSame:
		fmt.Fprintf(e.out, "Nada a promover: %s.\n", p.Reason)
		return nil
	case apps.PromoteOlder:
		if !yes {
			return fmt.Errorf("%s.\nPra promover mesmo assim: cais app promote %s -yes", p.Reason, name)
		}
	}
	fmt.Fprintf(e.out, "Promovendo %s (validado em %s) pra %s: build com a versao atual no ar e up...\n", short(p.Deploy), name, p.To)
	res, err := c.PromoteApp(ctx, name, yes)
	for _, w := range res.Warnings {
		fmt.Fprintf(e.out, "  aviso: %s\n", w)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(e.out, "%s no ar no commit %s.\n", p.To, short(res.Commit))
	return nil
}

// appSet troca branch, compose, producao e deploy automatico do app; so o
// que veio nas flags muda.
func (e *cli) appSet(ctx context.Context, c daemonAPI, name string, fs *flag.FlagSet, promoteTo string, auto *bool) error {
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
	fmt.Fprintf(e.out, "%s: branch %s, compose %s, deploy %s.\n", a.Name, cmpOr(a.Branch, "principal"), a.ComposeFile(), mode)
	if a.PromoteTo != "" {
		fmt.Fprintf(e.out, "Homologacao de %s (cais app promote %s).\n", a.PromoteTo, a.Name)
	}
	return nil
}

// appEnv mostra ou muda as variaveis do app. Sem mudancas, so mostra.
func (e *cli) appEnv(ctx context.Context, c daemonAPI, name string, set, unset []string, importFile string, show bool) error {
	env, err := c.AppEnv(ctx, name)
	if err != nil {
		return err
	}
	if env == nil { // app sem variaveis pode chegar como null no JSON
		env = map[string]string{}
	}
	if importFile != "" {
		var data []byte
		if importFile == "-" {
			data, err = io.ReadAll(e.in)
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
		return e.listEnv(ctx, c, name, env, show)
	}
	// VAR=valor grava; VAR sozinho (uma so) le o valor sem eco ou do stdin:
	// senha fora do historico do shell, valor com varias linhas
	asked := false
	for _, kv := range set {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			if asked || importFile == "-" {
				return fmt.Errorf("%q: so uma variavel sem =, que tem o valor lido sem eco ou do stdin", kv)
			}
			asked = true
			if v, err = e.readSecret("Valor de " + k + ": "); err != nil {
				return err
			}
		}
		if k == "" {
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
	fmt.Fprintf(e.out, "Variaveis de %s gravadas; valem no proximo deploy.\n", name)
	return nil
}

// listEnv mostra os nomes das variaveis do app (com show, os valores) e as
// que os bancos e buckets ligados entregam no deploy.
func (e *cli) listEnv(ctx context.Context, c daemonAPI, name string, env map[string]string, show bool) error {
	if len(env) == 0 {
		fmt.Fprintf(e.out, "%s sem variaveis.\n", name)
	}
	for _, k := range slices.Sorted(maps.Keys(env)) {
		if show {
			fmt.Fprintf(e.out, "  %s=%s\n", k, env[k])
		} else {
			fmt.Fprintf(e.out, "  %s\n", k)
		}
	}
	if list, err := c.CheckApps(ctx); err == nil {
		for _, a := range list.List {
			if infra := apps.InfraVars(a.App); a.Name == name && len(infra) > 0 {
				fmt.Fprintf(e.out, "Dos bancos e buckets ligados (no deploy): %s\n", strings.Join(infra, ", "))
			}
		}
	}
	if !show && len(env) > 0 {
		fmt.Fprintln(e.out, "Valores: cais app env "+name+" -show")
	}
	return nil
}

// appDB mostra, liga ou desliga os bancos da infra do app:
// cais app db <app> | <engine/nome> | off <engine/nome>
func (e *cli) appDB(ctx context.Context, c daemonAPI, app string, args []string) error {
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
				fmt.Fprintf(e.out, "%s nao usa banco da infra. Ligue com: cais app db %s postgres/<database>\n", app, app)
			}
			for _, l := range a.Databases {
				fmt.Fprintf(e.out, "  %s  (variaveis %s)\n", l.ID(), strings.Join(l.Vars(), ", "))
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
		fmt.Fprintf(e.out, "%s desligado do banco %s (o database e os dados ficam); vale no proximo deploy.\n", app, args[1])
	case len(args) == 1:
		engine, name, err := parse(args[0])
		if err != nil {
			return err
		}
		if err := c.LinkAppDatabase(ctx, app, engine, name); err != nil {
			return err
		}
		l := apps.DBLink{Engine: engine, Name: name}
		fmt.Fprintf(e.out, "%s ligado ao banco %s: variaveis %s, valem no proximo deploy.\n", app, args[0], strings.Join(l.Vars(), ", "))
	default:
		return errors.New("uso: cais app db <app> [engine/nome | off engine/nome]")
	}
	return nil
}

// appBucket mostra, liga ou desliga os buckets do Garage do app:
// cais app bucket <app> | <bucket> | off <bucket>
func (e *cli) appBucket(ctx context.Context, c daemonAPI, app string, args []string) error {
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
				fmt.Fprintf(e.out, "%s nao usa bucket do Garage. Ligue com: cais app bucket %s <bucket>\n", app, app)
			}
			for _, l := range a.Buckets {
				fmt.Fprintf(e.out, "  %s  (variaveis %s)\n", l.Name, strings.Join(l.Vars(), ", "))
			}
			return nil
		}
		return fmt.Errorf("app %s nao existe", app)
	case args[0] == "off" && len(args) == 2:
		if err := c.UnlinkAppBucket(ctx, app, args[1]); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "%s desligado do bucket %s (o bucket e os arquivos ficam); vale no proximo deploy.\n", app, args[1])
	case len(args) == 1:
		if err := c.LinkAppBucket(ctx, app, args[0]); err != nil {
			return err
		}
		l := apps.BucketLink{Name: args[0]}
		fmt.Fprintf(e.out, "%s ligado ao bucket %s: variaveis %s, valem no proximo deploy.\n", app, args[0], strings.Join(l.Vars(), ", "))
	default:
		return errors.New("uso: cais app bucket <app> [<bucket> | off <bucket>]")
	}
	return nil
}

// confirmName pede o nome digitado antes de apagar algo (pulado com -yes).
func (e *cli) confirmName(yes bool, name, warning string) error {
	if yes {
		return nil
	}
	fmt.Fprintf(e.out, "%s\nDigite %s pra confirmar: ", warning, name)
	var typed string
	fmt.Fscanln(e.in, &typed)
	if strings.TrimSpace(typed) != name {
		return errors.New("nome diferente: nada foi apagado")
	}
	return nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// parseInterspersed aceita flags em qualquer posicao (cais app deploy x
// -ref main): o pacote flag sozinho para no primeiro argumento posicional.
func (e *cli) parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := e.parse(fs, args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// appHistory lista os ultimos deploys do app.
func (e *cli) appHistory(ctx context.Context, c daemonAPI, name string) error {
	st, err := c.CheckApps(ctx)
	if err != nil {
		return err
	}
	for _, a := range st.List {
		if a.Name != name {
			continue
		}
		if len(a.History) == 0 {
			fmt.Fprintf(e.out, "%s ainda nao tem deploy.\n", name)
			return nil
		}
		w := tabwriter.NewWriter(e.out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "QUANDO\tCOMMIT\tORIGEM\tRESULTADO")
		for _, d := range a.History {
			origin := "manual"
			switch {
			case d.Rollback:
				origin = "rollback"
			case d.Auto:
				origin = "automatico"
			case d.PromotedFrom != "":
				origin = "promovido de " + d.PromotedFrom
			}
			result := "ok"
			if d.Images {
				result = "ok (imagens guardadas)"
			}
			if !d.OK() {
				result = "falhou: " + firstLineOf(d.Error)
				if d.RolledBack {
					result += " (a anterior voltou)"
				}
			}
			mark := ""
			if d.OK() && d.Commit == a.Commit {
				mark = " <- no ar"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s%s\n", d.At.Local().Format("02/01/2006 15:04"), short(d.Commit), origin, result, mark)
		}
		w.Flush()
		if a.AutoHold != "" {
			fmt.Fprintf(e.out, "\nDeploy automatico esperando um commit novo (depois do rollback; %s nao sobe sozinho).\n", short(a.AutoHold))
		}
		fmt.Fprintf(e.out, "\nVoltar: cais app rollback %s [commit]\n", name)
		return nil
	}
	return fmt.Errorf("app %q nao encontrado", name)
}

func firstLineOf(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// appShell abre o shell de um container do app neste terminal (o daemon
// abre o docker exec -it e manda o terminal pelo socket).
func (e *cli) appShell(ctx context.Context, c daemonAPI, name, service string) error {
	return e.openTerminal(ctx, c, core.ShellRequest{App: name, Service: service}, "Shell em "+cmpOr(service, name),
		"ssh -t cais@vps app shell "+name)
}

// openTerminal abre o terminal pedido (shell de app ou console de banco)
// neste terminal, ate ele sair.
func (e *cli) openTerminal(ctx context.Context, c daemonAPI, req core.ShellRequest, title, viaSSH string) error {
	if !ptyio.IsTerminal(e.in) {
		return errors.New("precisa de um terminal; pelo ssh, use -t: " + viaSSH)
	}
	opener, ok := c.(core.ShellOpener)
	if !ok {
		return errors.New("este cliente nao abre terminal")
	}
	req.Term = os.Getenv("TERM")
	req.Rows, req.Cols = ptyio.Size(e.out)
	f, err := opener.OpenShell(ctx, req)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.err, "%s (exit ou ctrl+d sai).\r\n", title)
	return ptyio.Attach(f, e.in, e.out)
}
