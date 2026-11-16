package databases

import (
	"io"
	"strings"
	"testing"
)

func TestParseSource(t *testing.T) {
	ok := []struct {
		engine, raw string
		want        string // String(), sem a senha
		pass        string
	}{
		{Postgres, "postgres://app:s3nha@db.exemplo.com:6543/loja?sslmode=require", "app@db.exemplo.com:6543/loja", "s3nha"},
		{Postgres, "postgresql://app:s3nha@db.exemplo.com/loja", "app@db.exemplo.com:5432/loja", "s3nha"},
		{Postgres, "postgres://app:Leg%40cy%3Apw%2F1@10.0.0.5/loja", "app@10.0.0.5:5432/loja", "Leg@cy:pw/1"},
		// senha com @ : / # sem codificar, como vem do painel do provedor
		{Postgres, "postgres://app:Leg@cy:pw/1#x@db.exemplo.com/loja", "app@db.exemplo.com:5432/loja", "Leg@cy:pw/1#x"},
		{Postgres, "postgres://app:123/456@db.exemplo.com/loja", "app@db.exemplo.com:5432/loja", "123/456"},
		{MySQL, "mysql://root:x@[2001:db8::1]:3307/blog", "root@[2001:db8::1]:3307/blog", "x"},
		{MySQL, "  mysql://app@db/blog  ", "app@db:3306/blog", ""},
	}
	for _, c := range ok {
		s, err := ParseSource(c.engine, c.raw)
		if err != nil {
			t.Errorf("%s: %v", c.raw, err)
			continue
		}
		if s.String() != c.want || s.Password != c.pass {
			t.Errorf("%s: %s senha %q", c.raw, s, s.Password)
		}
	}

	bad := []struct{ engine, raw, want string }{
		{Postgres, "nada disso", "invalida"},
		{Postgres, "mysql://a:b@h/db", "banco novo e postgres"},
		{MySQL, "postgres://a:b@h/db", "banco novo e mysql"},
		{Postgres, "postgres://a:b@h", "nome do banco"},
		{Postgres, "postgres://a:b@h:99999/db", "porta"},
		{Postgres, "postgres://h/db", "invalida"},
		{Postgres, "postgres://a:b@localhost/db", "proprio container"},
		{MySQL, "mysql://a:b@127.0.0.1/db", "proprio container"},
		{MySQL, "mysql://a:b@h/db?tls=true", "parametros"},
	}
	for _, c := range bad {
		_, err := ParseSource(c.engine, c.raw)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v (esperava %q)", c.raw, err, c.want)
		}
		if err != nil && strings.Contains(err.Error(), ":b@") {
			t.Errorf("%s: a senha apareceu no erro: %v", c.raw, err)
		}
	}
}

// a senha nunca vai na linha de comando (ps da VPS): so pelo stdin
func TestImportDumpCommandSemSenha(t *testing.T) {
	for _, raw := range []string{"postgres://app:SegredoX1@db/loja?sslmode=require", "mysql://app:SegredoX1@db/blog"} {
		engine := Postgres
		if strings.HasPrefix(raw, "mysql") {
			engine = MySQL
		}
		s, err := ParseSource(engine, raw)
		if err != nil {
			t.Fatal(err)
		}
		cmd, err := ImportDumpCommand(s)
		if err != nil {
			t.Fatal(err)
		}
		all := strings.Join(cmd, " ")
		if strings.Contains(all, "SegredoX1") {
			t.Errorf("senha no comando: %s", all)
		}
		if !strings.Contains(all, "read -r") || !strings.Contains(all, "db") {
			t.Errorf("comando: %s", all)
		}
		if engine == Postgres && !strings.Contains(all, "sslmode=require") {
			t.Errorf("perdeu os parametros: %s", all)
		}
	}
}

func TestStripDefiners(t *testing.T) {
	in := "/*!50013 DEFINER=`legacy`@`%` SQL SECURITY DEFINER */\n" +
		"/*!50003 CREATE*/ /*!50017 DEFINER=`le``gacy`@`10.0.0.%`*/ /*!50003 TRIGGER t BEFORE UPDATE ON p */;\n" +
		"CREATE DEFINER=`legacy`@`%` PROCEDURE `conta`(OUT n INT)\n" +
		"INSERT INTO `notas` VALUES (1,'DEFINER=`x`@`y` fica');\n"
	out, err := io.ReadAll(StripDefiners(strings.NewReader(in)))
	if err != nil {
		t.Fatal(err)
	}
	want := "/*!50013  SQL SECURITY DEFINER */\n" +
		"/*!50003 CREATE*/ /*!50017 */ /*!50003 TRIGGER t BEFORE UPDATE ON p */;\n" +
		"CREATE  PROCEDURE `conta`(OUT n INT)\n" +
		"INSERT INTO `notas` VALUES (1,'DEFINER=`x`@`y` fica');\n"
	if string(out) != want {
		t.Errorf("saiu:\n%s\nesperava:\n%s", out, want)
	}
}

func TestOwnershipSQLRecusaNomeRuim(t *testing.T) {
	if _, err := OwnershipSQL("x'; DROP"); err == nil {
		t.Fatal("aceitou nome invalido")
	}
	sql, err := OwnershipSQL("loja")
	if err != nil || !strings.Contains(sql, "app text := 'loja'") || !strings.Contains(sql, "'pg\\_%'") {
		t.Fatalf("sql: %v\n%s", err, sql)
	}
}
