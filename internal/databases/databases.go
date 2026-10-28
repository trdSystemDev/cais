package databases

import (
	"context"
	"fmt"
	"strings"
)

// Engines suportados: as stacks compartilhadas de mesmo nome.
const (
	Postgres = "postgres"
	MySQL    = "mysql"
)

// Engine e como um app chega numa instancia compartilhada.
type Engine struct {
	Name string
	// Host e o nome do container na rede do banco.
	Host    string
	Port    int
	Network string
	// Container e onde o cais roda os comandos de administracao.
	Container string
	scheme    string
}

var engines = map[string]Engine{
	Postgres: {Name: Postgres, Host: "postgres", Port: 5432, Network: "postgres_network", Container: "postgres", scheme: "postgres"},
	MySQL:    {Name: MySQL, Host: "mysql", Port: 3306, Network: "mysql_network", Container: "mysql", scheme: "mysql"},
}

// Get devolve o engine pelo nome.
func Get(name string) (Engine, error) {
	e, ok := engines[name]
	if !ok {
		return Engine{}, fmt.Errorf("banco %q nao suportado (use %s ou %s)", name, Postgres, MySQL)
	}
	return e, nil
}

// Identifier e o nome do database e do usuario de um app: o nome do app
// com _ no lugar de - (valido sem aspas nos dois bancos e nas URLs).
func Identifier(app string) string { return strings.ReplaceAll(app, "-", "_") }

// URL e a URL de conexao no formato que a maioria dos frameworks aceita.
// A senha do cais e A-Z2-7, entao entra sem escape.
func (e Engine) URL(name string, password []byte) string {
	return fmt.Sprintf("%s://%s:%s@%s:%d/%s", e.scheme, name, password, e.Host, e.Port, name)
}

// Exec roda um comando no container mandando input pelo stdin.
type Exec func(ctx context.Context, container string, input []byte, args ...string) (string, error)

// Provision cria (ou acerta) o database e o usuario do app com a senha
// dada. Idempotente: rodar de novo nao apaga nada e reaplica a senha.
// O usuario so acessa o proprio database; os dos outros apps nem conecta.
func Provision(ctx context.Context, exec Exec, e Engine, name string, password []byte) error {
	if err := checkIdentifier(name); err != nil {
		return err
	}
	if err := checkPassword(password); err != nil {
		return err
	}
	var err error
	switch e.Name {
	case Postgres:
		// socket local do container: o usuario postgres entra sem senha
		_, err = exec(ctx, e.Container, []byte(postgresSQL(name, password)),
			"psql", "-v", "ON_ERROR_STOP=1", "-q", "-U", "postgres", "-d", "postgres")
	case MySQL:
		// a senha do root e lida do secret dentro do proprio container
		_, err = exec(ctx, e.Container, []byte(mysqlSQL(name, password)),
			"sh", "-c", `MYSQL_PWD="$(cat /run/secrets/mysql_root_password)" exec mysql -uroot`)
	default:
		return fmt.Errorf("banco %q nao suportado", e.Name)
	}
	if err != nil {
		return fmt.Errorf("criando database %s no %s: %w", name, e.Name, err)
	}
	return nil
}

func checkIdentifier(name string) error {
	if name == "" || len(name) > 32 {
		return fmt.Errorf("nome de database invalido %q", name)
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return fmt.Errorf("nome de database invalido %q", name)
		}
	}
	return nil
}

// a senha entra num literal SQL: so os caracteres que o cais gera
func checkPassword(pw []byte) error {
	if len(pw) < 16 {
		return fmt.Errorf("senha do banco curta demais")
	}
	for _, c := range pw {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return fmt.Errorf("senha do banco com caractere fora de A-Z, a-z, 0-9")
		}
	}
	return nil
}

func postgresSQL(name string, password []byte) string {
	// nome e senha ja passaram por checkIdentifier e checkPassword
	return fmt.Sprintf(`DO $$
BEGIN
  IF EXISTS (SELECT FROM pg_roles WHERE rolname = '%[1]s') THEN
    ALTER ROLE "%[1]s" WITH LOGIN PASSWORD '%[2]s';
  ELSE
    CREATE ROLE "%[1]s" WITH LOGIN PASSWORD '%[2]s';
  END IF;
END
$$;
SELECT 'CREATE DATABASE "%[1]s" OWNER "%[1]s"'
  WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = '%[1]s')\gexec
ALTER DATABASE "%[1]s" OWNER TO "%[1]s";
REVOKE ALL ON DATABASE "%[1]s" FROM PUBLIC;
GRANT ALL ON DATABASE "%[1]s" TO "%[1]s";
`, name, password)
}

func mysqlSQL(name string, password []byte) string {
	return fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%[1]s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;\n"+
		"CREATE USER IF NOT EXISTS '%[1]s'@'%%' IDENTIFIED BY '%[2]s';\n"+
		"ALTER USER '%[1]s'@'%%' IDENTIFIED BY '%[2]s';\n"+
		"GRANT ALL PRIVILEGES ON `%[1]s`.* TO '%[1]s'@'%%';\n", name, password)
}
