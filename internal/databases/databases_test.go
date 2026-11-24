package databases

import (
	"context"
	"strings"
	"testing"
)

type call struct {
	container string
	input     string
	args      []string
}

func fakeExec(calls *[]call) Exec {
	return func(_ context.Context, container string, input []byte, args ...string) (string, error) {
		*calls = append(*calls, call{container, string(input), args})
		return "", nil
	}
}

const pw = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"

func TestProvisionPostgres(t *testing.T) {
	var calls []call
	e, _ := Get(Postgres)
	if err := Provision(context.Background(), fakeExec(&calls), e, "meu_app", []byte(pw), nil); err != nil {
		t.Fatal(err)
	}
	c := calls[0]
	if c.container != "postgres" || c.args[0] != "psql" {
		t.Fatalf("chamada %+v", c)
	}
	// a senha vai pelo stdin, nunca nos argumentos (apareceria no ps)
	if strings.Contains(strings.Join(c.args, " "), pw) || !strings.Contains(c.input, pw) {
		t.Error("senha fora do stdin")
	}
	for _, want := range []string{`CREATE ROLE "meu_app"`, `ALTER ROLE "meu_app"`, `CREATE DATABASE "meu_app" OWNER "meu_app"`, `REVOKE ALL ON DATABASE "meu_app" FROM PUBLIC`} {
		if !strings.Contains(c.input, want) {
			t.Errorf("SQL sem %q", want)
		}
	}
}

func TestProvisionMySQL(t *testing.T) {
	var calls []call
	e, _ := Get(MySQL)
	if err := Provision(context.Background(), fakeExec(&calls), e, "meu_app", []byte(pw), nil); err != nil {
		t.Fatal(err)
	}
	c := calls[0]
	if c.container != "mysql" || strings.Contains(strings.Join(c.args, " "), pw) {
		t.Fatalf("chamada %+v", c)
	}
	for _, want := range []string{"CREATE DATABASE IF NOT EXISTS `meu_app`", "CREATE USER IF NOT EXISTS 'meu_app'@'%'", "GRANT ALL PRIVILEGES ON `meu_app`.* TO 'meu_app'@'%'"} {
		if !strings.Contains(c.input, want) {
			t.Errorf("SQL sem %q", want)
		}
	}
}

func TestProvisionRecusaEntradaQueQuebraOSQL(t *testing.T) {
	var calls []call
	e, _ := Get(Postgres)
	for _, name := range []string{"", "a-b", `x"; DROP DATABASE postgres; --`, "Maiusculo", strings.Repeat("a", 33)} {
		if err := Provision(context.Background(), fakeExec(&calls), e, name, []byte(pw), nil); err == nil {
			t.Errorf("aceitou nome %q", name)
		}
	}
	for _, bad := range []string{"curta", pw + "'", pw + "$$"} {
		if err := Provision(context.Background(), fakeExec(&calls), e, "meu_app", []byte(bad), nil); err == nil {
			t.Errorf("aceitou senha %q", bad)
		}
	}
	if len(calls) != 0 {
		t.Fatal("rodou SQL com entrada invalida")
	}
}

func TestIdentificador(t *testing.T) {
	if Identifier("meu-app") != "meu_app" {
		t.Error("identificador")
	}
	if _, err := Get("mongo"); err == nil {
		t.Error("aceitou mongo")
	}
}

func TestProvisionComExtensao(t *testing.T) {
	var calls []call
	e, _ := Get(Postgres)
	if err := Provision(context.Background(), fakeExec(&calls), e, "portal", []byte(pw), []string{"postgis"}); err != nil {
		t.Fatal(err)
	}
	sql := calls[0].input
	// a extensao e criada no database do app, depois de criado, como superusuario
	iConnect := strings.Index(sql, "\\connect \"portal\"")
	iExt := strings.Index(sql, `CREATE EXTENSION IF NOT EXISTS "postgis";`)
	if iConnect < 0 || iExt < iConnect || iConnect < strings.Index(sql, "CREATE DATABASE") {
		t.Fatalf("SQL das extensoes:\n%s", sql)
	}

	for _, bad := range [][]string{{"postgis; DROP DATABASE x"}, {"plpython3u"}, {`"x"`}} {
		if err := Provision(context.Background(), fakeExec(&calls), e, "portal", []byte(pw), bad); err == nil {
			t.Errorf("aceitou extensao %q", bad)
		}
	}
	my, _ := Get(MySQL)
	if err := Provision(context.Background(), fakeExec(&calls), my, "portal", []byte(pw), []string{"postgis"}); err == nil {
		t.Error("aceitou extensao no MySQL")
	}
}

func TestSizes(t *testing.T) {
	exec := func(_ context.Context, container string, input []byte, args ...string) (string, error) {
		if container == "postgres" {
			return "postgres\t7500259\nportal\t12345678\n", nil
		}
		return "loja\t49152", nil
	}
	pg, _ := Get(Postgres)
	if got, err := Sizes(context.Background(), exec, pg); err != nil || got["portal"] != 12345678 || len(got) != 2 {
		t.Errorf("postgres %v %v", got, err)
	}
	my, _ := Get(MySQL)
	if got, err := Sizes(context.Background(), exec, my); err != nil || got["loja"] != 49152 {
		t.Errorf("mysql %v %v", got, err)
	}
}

func TestHumanSize(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 7500259: "7.2 MB", 3 << 30: "3.0 GB"} {
		if got := HumanSize(n); got != want {
			t.Errorf("HumanSize(%d) = %s, quer %s", n, got, want)
		}
	}
}

func TestDrop(t *testing.T) {
	var sql []string
	exec := func(_ context.Context, container string, input []byte, args ...string) (string, error) {
		sql = append(sql, container+": "+string(input))
		return "", nil
	}
	pg, _ := Get(Postgres)
	my, _ := Get(MySQL)
	if err := Drop(context.Background(), exec, pg, "loja"); err != nil {
		t.Fatal(err)
	}
	if err := Drop(context.Background(), exec, my, "loja"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql[0], `DROP DATABASE IF EXISTS "loja" WITH (FORCE)`) || !strings.Contains(sql[0], `DROP ROLE IF EXISTS "loja"`) ||
		!strings.Contains(sql[1], "DROP DATABASE IF EXISTS `loja`") || !strings.Contains(sql[1], "DROP USER IF EXISTS 'loja'@'%'") {
		t.Errorf("sql %q", sql)
	}
	if err := Drop(context.Background(), exec, pg, "x; DROP"); err == nil {
		t.Error("aceitou nome invalido")
	}
}

func TestConsoleCommand(t *testing.T) {
	pg, _ := ConsoleCommand(Postgres, "")
	pgDB, _ := ConsoleCommand(Postgres, "loja")
	my, _ := ConsoleCommand(MySQL, "loja")
	if strings.Join(pg, " ") != "psql -U postgres -d postgres" || strings.Join(pgDB, " ") != "psql -U postgres -d loja" {
		t.Errorf("psql: %v %v", pg, pgDB)
	}
	if my[0] != "sh" || !strings.Contains(my[2], "mysql_root_password") || my[len(my)-1] != "loja" {
		t.Errorf("mysql: %v", my)
	}
	if _, err := ConsoleCommand(Postgres, "loja; drop"); err == nil {
		t.Error("aceitou nome invalido")
	}
	if _, err := ConsoleCommand("mongo", ""); err == nil {
		t.Error("aceitou banco desconhecido")
	}
}
