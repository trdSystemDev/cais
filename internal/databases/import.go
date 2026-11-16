package databases

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Source e o banco de origem de uma importacao, lido da string de conexao
// (postgres://usuario:senha@host:5432/banco ou mysql://...). A senha so
// vai pro comando de dump pelo stdin, nunca pra log, estado ou erro.
type Source struct {
	Engine   string
	Host     string
	Port     int
	User     string
	Password string
	Database string
	// Params sao os parametros da URL do Postgres (ex: sslmode=require).
	Params url.Values
}

// String e a origem sem a senha: usuario@host:porta/banco.
func (s Source) String() string {
	return fmt.Sprintf("%s@%s/%s", s.User, net.JoinHostPort(s.Host, strconv.Itoa(s.Port)), s.Database)
}

// ParseSource le a string de conexao do banco de origem. O esquema tem que
// bater com o engine: dump de Postgres nao entra no MySQL.
func ParseSource(engine, raw string) (Source, error) {
	raw = strings.TrimSpace(raw)
	e, err := Get(engine)
	if err != nil {
		return Source{}, err
	}
	u, ok := parseConnURL(raw)
	if !ok {
		// o erro do url.Parse repete a string inteira, senha junto: nunca vai
		return Source{}, fmt.Errorf("string de conexao invalida: use %s", sourceExample(engine))
	}
	switch scheme := strings.ToLower(u.Scheme); {
	case engine == Postgres && (scheme == "postgres" || scheme == "postgresql"):
	case engine == MySQL && scheme == "mysql":
	default:
		return Source{}, fmt.Errorf("a string de conexao e de %q, mas o banco novo e %s: use %s", u.Scheme, engine, sourceExample(engine))
	}
	s := Source{Engine: engine, Host: u.Hostname(), Port: e.Port, User: u.User.Username(), Database: strings.TrimPrefix(u.Path, "/")}
	s.Password, _ = u.User.Password()
	if p := u.Port(); p != "" {
		if s.Port, err = strconv.Atoi(p); err != nil || s.Port <= 0 || s.Port > 65535 {
			return Source{}, fmt.Errorf("porta invalida %q na string de conexao", p)
		}
	}
	switch {
	case s.User == "":
		return Source{}, fmt.Errorf("falta o usuario na string de conexao: %s", sourceExample(engine))
	case s.Database == "" || strings.Contains(s.Database, "/"):
		return Source{}, fmt.Errorf("falta o nome do banco no fim da string de conexao: %s", sourceExample(engine))
	}
	switch strings.ToLower(s.Host) {
	case "localhost", "127.0.0.1", "::1":
		return Source{}, fmt.Errorf("o dump roda dentro do container do %s, onde %s e o proprio container: "+
			"use o IP ou o nome publico do servidor de origem (se for esta VPS, o IP dela na rede do docker, ex: 172.17.0.1)", engine, s.Host)
	}
	if engine == Postgres {
		s.Params = u.Query()
	} else if len(u.Query()) > 0 {
		return Source{}, errors.New("a string de conexao do MySQL nao aceita parametros (?...): o cais usa TLS quando o servidor oferece")
	}
	return s, nil
}

// parseConnURL le a string de conexao. Senha com / # ? copiada do painel
// do provedor quase nunca vem codificada (%2F...) e quebra o url.Parse: ai
// o usuario e a senha sao o que vem antes do ultimo @.
func parseConnURL(raw string) (*url.URL, bool) {
	u, err := url.Parse(raw)
	if err == nil && u.Host != "" && u.User != nil && !strings.Contains(u.Path, "@") && u.Fragment == "" {
		return u, true
	}
	scheme, rest, ok := strings.Cut(raw, "://")
	at := strings.LastIndex(rest, "@")
	if !ok || at < 0 {
		return nil, false
	}
	u, err = url.Parse(scheme + "://" + rest[at+1:])
	if err != nil || u.Host == "" {
		return nil, false
	}
	if user, pass, ok := strings.Cut(rest[:at], ":"); ok {
		u.User = url.UserPassword(user, pass)
	} else {
		u.User = url.User(user)
	}
	return u, true
}

func sourceExample(engine string) string {
	if engine == MySQL {
		return "mysql://usuario:senha@host:3306/banco"
	}
	return "postgres://usuario:senha@host:5432/banco"
}

// ImportDumpCommand e o comando, dentro do container do banco, que escreve
// no stdout o dump do banco de origem. A primeira linha do stdin tem que ser
// a senha: na linha de comando ela apareceria no ps da VPS.
//
// No Postgres o dump sai sem dono e sem GRANT (os papeis da origem nao
// existem aqui); OwnershipSQL passa tudo pro usuario do app depois.
func ImportDumpCommand(s Source) ([]string, error) {
	switch s.Engine {
	case Postgres:
		u := url.URL{Scheme: "postgres", User: url.User(s.User), Host: net.JoinHostPort(s.Host, strconv.Itoa(s.Port)),
			Path: "/" + s.Database, RawQuery: s.Params.Encode()}
		// PGCONNECT_TIMEOUT: origem atras de firewall que descarta pacote
		// nao prende a importacao por minutos
		return []string{"sh", "-c", `IFS= read -r PGPASSWORD; export PGPASSWORD; export PGCONNECT_TIMEOUT=15; ` +
			`exec pg_dump --no-owner --no-privileges --no-password --dbname="$1"`, "sh", u.String()}, nil
	case MySQL:
		// --single-transaction: copia consistente sem travar a origem
		// (InnoDB); --column-statistics=0: o mysqldump novo nao trava em
		// servidor 5.7/MariaDB, que nao tem a tabela das estatisticas
		// o mysqldump nao tem limite pra conectar: um SELECT 1 pelo mysql
		// (que tem) antes, pra origem atras de firewall nao prender por minutos
		return []string{"sh", "-c", `IFS= read -r MYSQL_PWD; export MYSQL_PWD; ` +
			`mysql --connect-timeout=15 -h "$1" -P "$2" -u "$3" -e 'SELECT 1' "$4" >/dev/null && ` +
			`exec mysqldump -h "$1" -P "$2" -u "$3" --single-transaction --quick --routines --triggers ` +
			`--no-tablespaces --set-gtid-purged=OFF --column-statistics=0 "$4"`,
			"sh", s.Host, strconv.Itoa(s.Port), s.User, s.Database}, nil
	}
	return nil, fmt.Errorf("banco %q nao suportado", s.Engine)
}

// OwnershipSQL passa pro usuario do app (name) tudo o que a importacao
// criou como superusuario: schemas, tabelas, views, sequences, funcoes,
// tipos e large objects. O que pertence a extensoes fica como esta. Roda
// dentro do database importado.
func OwnershipSQL(name string) (string, error) {
	if err := checkIdentifier(name); err != nil {
		return "", err
	}
	return fmt.Sprintf(`DO $$
DECLARE
  r record;
  app text := '%s';
BEGIN
  FOR r IN SELECT n.nspname AS obj FROM pg_namespace n
    WHERE n.nspname NOT LIKE 'pg\_%%' AND n.nspname <> 'information_schema'
      AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_namespace'::regclass AND d.objid = n.oid AND d.deptype = 'e')
  LOOP
    EXECUTE format('ALTER SCHEMA %%I OWNER TO %%I', r.obj, app);
  END LOOP;

  -- sequences de serial/identity seguem a tabela (e nao aceitam ALTER sozinhas)
  FOR r IN SELECT c.oid::regclass::text AS obj, c.relkind FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE c.relkind IN ('r', 'p', 'v', 'm', 'S', 'f')
      AND n.nspname NOT LIKE 'pg\_%%' AND n.nspname <> 'information_schema'
      AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'e')
      AND NOT (c.relkind = 'S' AND EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_class'::regclass
        AND d.objid = c.oid AND d.refobjsubid > 0 AND d.deptype IN ('a', 'i')))
  LOOP
    EXECUTE format('ALTER %%s %%s OWNER TO %%I',
      CASE r.relkind WHEN 'v' THEN 'VIEW' WHEN 'm' THEN 'MATERIALIZED VIEW' WHEN 'S' THEN 'SEQUENCE'
        WHEN 'f' THEN 'FOREIGN TABLE' ELSE 'TABLE' END, r.obj, app);
  END LOOP;

  FOR r IN SELECT p.oid::regprocedure::text AS obj, p.prokind FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
    WHERE n.nspname NOT LIKE 'pg\_%%' AND n.nspname <> 'information_schema'
      AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e')
  LOOP
    EXECUTE format('ALTER %%s %%s OWNER TO %%I', CASE r.prokind WHEN 'a' THEN 'AGGREGATE' ELSE 'ROUTINE' END, r.obj, app);
  END LOOP;

  -- tipos soltos: enum, dominio, range e composto criado com CREATE TYPE
  -- (o composto de cada tabela segue a tabela)
  FOR r IN SELECT t.oid::regtype::text AS obj, t.typtype FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace
    WHERE (t.typtype IN ('e', 'd', 'r') OR (t.typtype = 'c' AND (SELECT c.relkind FROM pg_class c WHERE c.oid = t.typrelid) = 'c'))
      AND n.nspname NOT LIKE 'pg\_%%' AND n.nspname <> 'information_schema'
      AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_type'::regclass AND d.objid = t.oid AND d.deptype = 'e')
  LOOP
    EXECUTE format('ALTER %%s %%s OWNER TO %%I', CASE r.typtype WHEN 'd' THEN 'DOMAIN' ELSE 'TYPE' END, r.obj, app);
  END LOOP;

  FOR r IN SELECT oid FROM pg_largeobject_metadata LOOP
    EXECUTE format('ALTER LARGE OBJECT %%s OWNER TO %%I', r.oid, app);
  END LOOP;
END
$$;
`, name), nil
}

// definer e a clausula DEFINER=`usuario`@`host` do mysqldump: o usuario da
// origem nao existe aqui, e view ou trigger com dono inexistente falha ao
// ser usada. Sem a clausula, o dono vira quem importa.
var definer = regexp.MustCompile("DEFINER=`(?:[^`]|``)*`@`(?:[^`]|``)*`")

// StripDefiners tira o DEFINER das views, triggers e rotinas de um dump do
// MySQL, linha a linha (as linhas de dados nao sao mexidas).
// Fechar o leitor devolvido para a copia (o restore desistiu).
func StripDefiners(r io.Reader) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		br := bufio.NewReaderSize(r, 1<<20)
		for {
			line, err := br.ReadBytes('\n')
			if len(line) > 0 {
				if !bytes.HasPrefix(line, []byte("INSERT INTO")) && bytes.Contains(line, []byte("DEFINER=")) {
					line = definer.ReplaceAll(line, nil)
				}
				if _, werr := pw.Write(line); werr != nil {
					return
				}
			}
			if err != nil {
				if err == io.EOF {
					err = nil
				}
				pw.CloseWithError(err)
				return
			}
		}
	}()
	return pr
}
