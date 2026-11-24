package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/databases"
)

// runBucket cria, lista e exclui buckets no Garage da infra.
func (e *cli) runBucket(args []string) error {
	sub, args := subcommand(args)
	fs, socket := e.newFlags("bucket " + sub)
	yes := fs.Bool("yes", false, "delete: nao pede confirmacao")
	pos, err := e.parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if sub != "create" && sub != "list" && sub != "delete" && sub != "console" {
		return groupError("bucket", sub)
	}
	c, err := e.connect(*socket)
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
		fmt.Fprintf(e.out, "Bucket %s criado (endpoint %s, regiao %s, path-style, rede %s).\n", info.Name, info.Endpoint, info.Region, info.Network)
		fmt.Fprintf(e.out, "Ligue ao app com: cais app bucket <app> %s (o app recebe as variaveis %s).\n", info.Name, strings.Join(info.Vars, ", "))
		fmt.Fprintln(e.out, "Atencao: os arquivos dos buckets nao entram no backup do cais.")
	case "delete":
		if len(pos) != 1 {
			return errors.New("uso: cais bucket delete <nome> [-yes]")
		}
		if err := e.confirmName(*yes, pos[0], "Excluir o bucket "+pos[0]+" apaga o bucket e a chave dele (o Garage so exclui bucket vazio). Nao tem volta."); err != nil {
			return err
		}
		if err := c.DeleteBucket(ctx, pos[0]); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "Bucket %s excluido.\n", pos[0])
	case "list":
		list, err := c.Buckets(ctx)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Fprintln(e.out, "Nenhum bucket criado pelo cais. Crie com: cais bucket create <nome>")
			return nil
		}
		w := tabwriter.NewWriter(e.out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "BUCKET\tSECRETS\tESPACO\tAPP")
		for _, b := range list {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", b.Name, strings.Join(b.Vars, ", "), cmpOr(b.Size, "?"), cmpOr(strings.Join(b.Apps, ", "), "-"))
		}
		w.Flush()
		fmt.Fprintln(e.out, "\nAtencao: os arquivos dos buckets nao entram no backup do cais.")
	}
	return nil
}

// runDB cria e lista bancos na infra.
func (e *cli) runDB(args []string) error {
	sub, args := subcommand(args)
	fs, socket := e.newFlags("db " + sub)
	exts := fs.String("extensions", "", "extensoes do Postgres, separadas por virgula (ex: postgis)")
	yes := fs.Bool("yes", false, "delete: nao pede confirmacao")
	imp := fs.Bool("import", false, "create: importa de outro servidor (pede a string de conexao)")
	pos, err := e.parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if sub != "create" && sub != "list" && sub != "delete" {
		return groupError("db", sub)
	}
	// a string de conexao tem a senha da origem: nunca por argumento
	var source string
	if sub == "create" && *imp {
		var err error
		if source, err = e.readSecret("String de conexao do banco de origem: "); err != nil {
			return err
		}
		if source == "" {
			return errors.New("string de conexao vazia")
		}
	}
	c, err := e.connect(*socket)
	if err != nil {
		return err
	}
	defer c.Close()
	ctx := context.Background()

	switch sub {
	case "console":
		if len(pos) < 1 || len(pos) > 2 {
			return errors.New("uso: cais db console postgres|mysql [database]")
		}
		var db string
		if len(pos) == 2 {
			db = pos[1]
		}
		return e.openTerminal(ctx, c, core.ShellRequest{Engine: pos[0], Database: db}, map[string]string{"postgres": "psql", "mysql": "mysql"}[pos[0]],
			"ssh -t cais@vps db console "+pos[0])
	case "create":
		if len(pos) != 2 {
			return errors.New("uso: cais db create postgres|mysql <nome> [-extensions postgis] [-import]")
		}
		req := core.DatabaseRequest{Engine: pos[0], Name: pos[1], Extensions: splitList(*exts), Source: source}
		var info core.DatabaseInfo
		if source == "" {
			info, err = c.CreateDatabase(ctx, req)
		} else {
			info, err = e.createImporting(ctx, c, req)
		}
		if err != nil {
			return err
		}
		if info.Imported != "" {
			fmt.Fprintf(e.out, "Database %s criado no %s com o conteudo de %s (dump de %s).\n", info.Name, info.Engine, info.Imported, databases.HumanSize(info.ImportedBytes))
		} else {
			fmt.Fprintf(e.out, "Database %s criado no %s (host %s:%d, rede %s).\n", info.Name, info.Engine, info.Host, info.Port, info.Network)
		}
		fmt.Fprintf(e.out, "Ligue ao app com: cais app db <app> %s/%s (o app recebe as variaveis %s).\n", info.Engine, info.Name, strings.Join(info.Vars, ", "))
	case "delete":
		if len(pos) != 2 {
			return errors.New("uso: cais db delete postgres|mysql <nome> [-yes]")
		}
		if err := e.confirmName(*yes, pos[1], "Excluir o database "+pos[0]+"/"+pos[1]+" apaga todos os dados dele e o usuario. Nao tem volta."); err != nil {
			return err
		}
		if err := c.DeleteDatabase(ctx, pos[0], pos[1]); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "Database %s/%s excluido.\n", pos[0], pos[1])
	case "list":
		list, err := c.Databases(ctx)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Fprintln(e.out, "Nenhum database criado pelo cais. Crie com: cais db create postgres <nome>")
		}
		w := tabwriter.NewWriter(e.out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "DATABASE\tSECRETS\tESPACO\tAPP")
		for _, d := range list {
			fmt.Fprintf(w, "%s/%s\t%s\t%s\t%s\n", d.Engine, d.Name, strings.Join(d.Vars, ", "), dbSize(d), cmpOr(strings.Join(d.Apps, ", "), "-"))
		}
		w.Flush()
	}
	return nil
}

// createImporting cria o database importando de outro servidor e mostra o
// andamento (quanto do dump ja foi copiado) enquanto espera.
func (e *cli) createImporting(ctx context.Context, c daemonAPI, req core.DatabaseRequest) (core.DatabaseInfo, error) {
	states, unsubscribe, err := c.Subscribe(ctx)
	if err != nil {
		return core.DatabaseInfo{}, err
	}
	printed := make(chan struct{})
	go func() {
		defer close(printed)
		var shown int64 = -1
		var last time.Time
		for st := range states {
			im := st.Import
			if im == nil || im.Engine != req.Engine || im.Name != req.Name {
				continue
			}
			if shown < 0 {
				fmt.Fprintf(e.out, "Importando de %s...\n", im.Source)
				shown = 0
			}
			if im.Bytes > shown && time.Since(last) >= 3*time.Second {
				fmt.Fprintf(e.out, "  %s copiados\n", databases.HumanSize(im.Bytes))
				shown, last = im.Bytes, time.Now()
			}
		}
	}()
	info, err := c.CreateDatabase(ctx, req)
	unsubscribe()
	<-printed
	return info, err
}

// dbSize e o espaco ocupado, ou ? se a instancia nao respondeu.
func dbSize(d core.DatabaseInfo) string {
	if d.Size == nil {
		return "?"
	}
	return databases.HumanSize(*d.Size)
}
