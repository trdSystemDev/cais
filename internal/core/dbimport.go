package core

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"github.com/trdSystemDev/cais/internal/databases"
)

// importProgress e de quanto em quanto tempo o andamento vai pro estado.
var importProgress = time.Second

// importDB copia o banco de origem pro database name (ja criado, vazio): o
// dump roda dentro do container do banco e vai direto pro restore, sem
// passar pelo disco. Devolve o tamanho do dump copiado.
func (c *Core) importDB(ctx context.Context, e databases.Engine, name string, src databases.Source) (int64, error) {
	dump, err := databases.ImportDumpCommand(src)
	if err != nil {
		return 0, err
	}
	restore, err := databases.RestoreCommand(e, name)
	if err != nil {
		return 0, err
	}

	var copied atomic.Int64
	c.update(func(s *State) {
		s.Import = &DBImport{Engine: e.Name, Name: name, Source: src.String(), StartedAt: c.deps.Now()}
	})
	defer c.update(func(s *State) { s.Import = nil })
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		t := time.NewTicker(importProgress)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				n := copied.Load()
				c.update(func(s *State) {
					if s.Import != nil {
						s.Import.Bytes = n
					}
				})
			}
		}
	}()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pr, pw := io.Pipe()
	dumpErr := make(chan error, 1)
	go func() {
		// a senha e a primeira linha do stdin (o comando le e exporta)
		err := c.deps.ExecStream(ctx, e.Container, strings.NewReader(src.Password+"\n"), countWriter{pw, &copied}, dump...)
		pw.CloseWithError(err)
		dumpErr <- err
	}()
	var in io.ReadCloser = pr
	if e.Name == databases.MySQL {
		in = databases.StripDefiners(pr)
	}
	rerr := c.deps.ExecStream(ctx, e.Container, in, io.Discard, restore...)

	// dump que cai no meio (senha, rede) so fecha o stdin do restore, que
	// termina "bem" com o que recebeu: o erro do dump e sempre a causa.
	// Restore que para antes deixa o dump sem pra onde escrever: corta.
	var derr error
	if rerr == nil {
		derr = <-dumpErr
	} else {
		select {
		case derr = <-dumpErr:
		case <-time.After(2 * time.Second):
			cancel()
			in.Close()
			pr.Close()
			<-dumpErr
		}
	}
	in.Close()
	switch {
	case derr != nil:
		return 0, fmt.Errorf("lendo o banco de origem %s: %w%s", src, derr, sourceHint(derr))
	case rerr != nil:
		return 0, fmt.Errorf("gravando no database %s: %w", name, rerr)
	}

	if e.Name == databases.Postgres {
		sql, err := databases.OwnershipSQL(name)
		if err != nil {
			return 0, err
		}
		if _, err := c.deps.ExecInput(ctx, e.Container, []byte(sql),
			"psql", "-v", "ON_ERROR_STOP=1", "-q", "-U", "postgres", "-d", name); err != nil {
			return 0, fmt.Errorf("passando as tabelas importadas pro usuario %s: %w", name, err)
		}
	}
	return copied.Load(), nil
}

// sourceHint explica os erros mais comuns ao ler a origem.
func sourceHint(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "server version mismatch") || strings.Contains(msg, "aborting because of server version"):
		return "\n\no servidor de origem e mais novo que o pg_dump do cais: atualize a stack postgres antes de importar"
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "timed out") || strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "can't connect") || strings.Contains(msg, "could not connect") || strings.Contains(msg, "no route"):
		return "\n\nconfira se o servidor de origem aceita conexao vinda do IP desta VPS (firewall, pg_hba.conf, bind-address)"
	case strings.Contains(msg, "could not translate host name") || strings.Contains(msg, "unknown mysql server host") ||
		strings.Contains(msg, "name does not resolve") || strings.Contains(msg, "name or service not known"):
		return "\n\nnao achei o servidor de origem: confira o endereco na string de conexao"
	case strings.Contains(msg, "password authentication failed") || strings.Contains(msg, "access denied"):
		return "\n\nusuario ou senha da origem recusados"
	}
	return ""
}

// countWriter conta o que passa por ele.
type countWriter struct {
	w io.Writer
	n *atomic.Int64
}

func (c countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n.Add(int64(n))
	return n, err
}
