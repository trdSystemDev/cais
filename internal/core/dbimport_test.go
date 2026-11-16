package core

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

// fakeImport finge o container do Postgres numa importacao: o dump le a
// senha da primeira linha do stdin e escreve o dump; o restore guarda o que
// recebeu. failDump e failRestore simulam as falhas.
type fakeImport struct {
	mu          sync.Mutex
	password    string
	restored    string
	sql         []string
	existing    string // database que ja existe na instancia
	failDump    error
	failRestore error
	big         bool // dump que nao acaba sozinho (so para se cortado)
}

func (f *fakeImport) execInput(_ context.Context, container string, input []byte, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := string(input)
	f.sql = append(f.sql, strings.Join(args, " ")+": "+s)
	if strings.Contains(s, "pg_database_size") {
		return "postgres\t100\n" + f.existing + "\t10\n", nil
	}
	return "", nil
}

func (f *fakeImport) stream(ctx context.Context, container string, stdin io.Reader, stdout io.Writer, args ...string) error {
	all := strings.Join(args, " ")
	if strings.Contains(all, "pg_dump") {
		line, _ := bufio.NewReader(stdin).ReadString('\n')
		f.mu.Lock()
		f.password = strings.TrimSuffix(line, "\n")
		fail, big := f.failDump, f.big
		f.mu.Unlock()
		if fail != nil {
			return fail
		}
		if big {
			for {
				if _, err := io.WriteString(stdout, strings.Repeat("x", 4096)); err != nil {
					return err
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
			}
		}
		_, err := io.WriteString(stdout, "CREATE TABLE clientes (id int);\n")
		return err
	}
	f.mu.Lock()
	fail := f.failRestore
	f.mu.Unlock()
	if fail != nil {
		return fail // nao le o stdin: o dump fica sem pra onde escrever
	}
	data, err := io.ReadAll(stdin)
	f.mu.Lock()
	f.restored = string(data)
	f.mu.Unlock()
	return err
}

func importCore(t *testing.T) (*Core, *fakeImport) {
	t.Helper()
	c, _, _ := newAppsCore(t)
	f := &fakeImport{}
	c.deps.ExecInput, c.deps.ExecStream = f.execInput, f.stream
	return c, f
}

const legacy = "postgres://legacy:S3nh@:com/barra@db.antigo.com:5432/loja?sslmode=require"

func TestImportarBanco(t *testing.T) {
	c, f := importCore(t)
	ctx := context.Background()
	info, err := c.CreateDatabase(ctx, DatabaseRequest{Engine: "postgres", Name: "loja", Source: legacy})
	if err != nil {
		t.Fatal(err)
	}
	if info.Imported != "legacy@db.antigo.com:5432/loja" || info.ImportedBytes != int64(len(f.restored)) || info.Name != "loja" {
		t.Errorf("info %+v", info)
	}
	if f.password != "S3nh@:com/barra" {
		t.Errorf("senha que chegou no dump: %q", f.password)
	}
	if f.restored != "CREATE TABLE clientes (id int);\n" {
		t.Errorf("restore recebeu %q", f.restored)
	}
	all := strings.Join(f.sql, "\n")
	if !strings.Contains(all, "-d loja: DO $$") || !strings.Contains(all, "OWNER TO") {
		t.Errorf("faltou passar as tabelas pro usuario do app:\n%s", all)
	}
	if strings.Contains(all, "S3nh@") {
		t.Error("a senha da origem foi parar num SQL")
	}
	if ok, _ := c.secretStore().Get(dbSecret("postgres", "loja")); ok == nil {
		t.Error("senha do database novo nao foi guardada")
	}
	if st, _ := c.Snapshot(ctx); st.Import != nil {
		t.Errorf("andamento ficou no estado: %+v", st.Import)
	}
}

// dump que falha (senha, rede): o erro e o do dump, o database novo some e
// a senha dele nao fica guardada
func TestImportarBancoFalhaNaOrigem(t *testing.T) {
	c, f := importCore(t)
	f.failDump = errors.New(`pg_dump: error: connection to server at "db.antigo.com" failed: FATAL:  password authentication failed for user "legacy"`)
	_, err := c.CreateDatabase(context.Background(), DatabaseRequest{Engine: "postgres", Name: "loja", Source: legacy})
	if err == nil || !strings.Contains(err.Error(), "lendo o banco de origem legacy@db.antigo.com") ||
		!strings.Contains(err.Error(), "usuario ou senha da origem recusados") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "S3nh@") {
		t.Error("a senha apareceu no erro")
	}
	if !strings.Contains(strings.Join(f.sql, "\n"), `DROP DATABASE IF EXISTS "loja"`) {
		t.Error("o database pela metade nao foi apagado")
	}
	if _, err := c.secretStore().Get(dbSecret("postgres", "loja")); err == nil {
		t.Error("guardou a senha de um database que nao ficou")
	}
}

// restore que para no meio: o dump (que nao acaba sozinho) e cortado
func TestImportarBancoFalhaNoRestore(t *testing.T) {
	c, f := importCore(t)
	f.big, f.failRestore = true, errors.New(`psql:<stdin>:12: ERROR:  extension "timescaledb" is not available`)
	_, err := c.CreateDatabase(context.Background(), DatabaseRequest{Engine: "postgres", Name: "loja", Source: legacy})
	if err == nil || !strings.Contains(err.Error(), "gravando no database loja") || !strings.Contains(err.Error(), "timescaledb") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(strings.Join(f.sql, "\n"), `DROP DATABASE IF EXISTS "loja"`) {
		t.Error("o database pela metade nao foi apagado")
	}
}

func TestImportarBancoRecusaNomeQueJaExiste(t *testing.T) {
	c, f := importCore(t)
	f.existing = "loja"
	_, err := c.CreateDatabase(context.Background(), DatabaseRequest{Engine: "postgres", Name: "loja", Source: legacy})
	if err == nil || !strings.Contains(err.Error(), "nao foi criado pelo cais") {
		t.Fatalf("err = %v", err)
	}
	for _, s := range f.sql {
		if strings.Contains(s, "CREATE ROLE") || strings.Contains(s, "DROP DATABASE") {
			t.Errorf("mexeu no database que ja existia: %s", s)
		}
	}
}
