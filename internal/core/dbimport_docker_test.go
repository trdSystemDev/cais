package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/docker"
)

// Importacao contra bancos de verdade, no docker: os destinos com as
// imagens das stacks do cais (lidas dos templates) e origens "legadas"
// com usuario proprio, serial, identity, enum, view, trigger e procedure.
// O teste conecta como o usuario do app e usa o banco importado.
//
// Lento (sobe 4 containers) e precisa de docker: so roda com
//
//	CAIS_DOCKER_TEST=1 go test ./internal/core -run Docker -v
const (
	legacyPostgresImage = "postgres:16-alpine"
	legacyMySQLImage    = "mysql:8.0"
	// a senha da origem tem @ : / de proposito, sem codificar na URL
	legacyPassword = "Leg@cy:pw/1"
)

func TestImportarBancoDocker(t *testing.T) {
	if os.Getenv("CAIS_DOCKER_TEST") != "1" {
		t.Skip("precisa de docker: CAIS_DOCKER_TEST=1 go test ./internal/core -run Docker")
	}
	ctx := context.Background()
	suffix := randomSuffix(t)
	net := "cais-test-" + suffix
	pg, my := "cais-test-pg-"+suffix, "cais-test-my-"+suffix
	srcPG, srcMy := "cais-test-srcpg-"+suffix, "cais-test-srcmy-"+suffix

	secrets := t.TempDir()
	os.Chmod(secrets, 0o755)
	rootPw := filepath.Join(secrets, "mysql_root_password")
	if err := os.WriteFile(rootPw, []byte("RootPw1234567890abc"), 0o644); err != nil {
		t.Fatal(err)
	}

	run(t, "network", "create", net)
	t.Cleanup(func() {
		exec.Command("docker", "rm", "-f", "-v", pg, my, srcPG, srcMy).Run()
		exec.Command("docker", "network", "rm", net).Run()
	})
	run(t, "run", "-d", "--name", pg, "--network", net, "-e", "POSTGRES_PASSWORD=x", stackImage(t, "postgres"))
	run(t, "run", "-d", "--name", my, "--network", net, "-e", "MYSQL_ROOT_PASSWORD_FILE=/run/secrets/mysql_root_password",
		"-v", rootPw+":/run/secrets/mysql_root_password:ro", stackImage(t, "mysql"))
	run(t, "run", "-d", "--name", srcPG, "--network", net, "-e", "POSTGRES_PASSWORD=x", legacyPostgresImage)
	run(t, "run", "-d", "--name", srcMy, "--network", net, "-e", "MYSQL_ROOT_PASSWORD=x", legacyMySQLImage)

	// so pela rede: durante a inicializacao o banco escuta so no socket, e
	// reinicia no fim
	waitReady(t, pg, "pg_isready", "-h", "127.0.0.1", "-U", "postgres")
	waitReady(t, srcPG, "pg_isready", "-h", "127.0.0.1", "-U", "postgres")
	waitReady(t, srcMy, "mysql", "-h127.0.0.1", "-uroot", "-px", "-e", "SELECT 1")
	waitReady(t, my, "sh", "-c", `mysql -h127.0.0.1 -uroot -p"$(cat /run/secrets/mysql_root_password)" -e "SELECT 1"`)

	sqlIn(t, srcPG, legacyPostgresSQL, "psql", "-v", "ON_ERROR_STOP=1", "-q", "-U", "postgres")
	sqlIn(t, srcMy, legacyMySQLSQL, "mysql", "-uroot", "-px")

	root := t.TempDir()
	names := map[string]string{"postgres": pg, "mysql": my}
	c := New(Deps{
		CheckDocker: okStatus,
		ConfigPath:  filepath.Join(root, "config.yaml"),
		SecretsDir:  filepath.Join(root, "secrets"),
		AppsDir:     filepath.Join(root, "apps"),
		ExecStream: func(ctx context.Context, ct string, in io.Reader, out io.Writer, args ...string) error {
			return docker.ExecStream(ctx, names[ct], in, out, args...)
		},
		ExecInput: func(ctx context.Context, ct string, in []byte, args ...string) (string, error) {
			return docker.ExecInput(ctx, names[ct], in, args...)
		},
	})
	cfg := config.Default()
	cfg.Domain, cfg.Email, cfg.Stacks = "exemplo.com.br", "a@exemplo.com.br", []string{"postgres", "mysql"}
	if err := c.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	t.Run("postgres", func(t *testing.T) {
		info, err := c.CreateDatabase(ctx, DatabaseRequest{Engine: "postgres", Name: "loja",
			Source: "postgres://legacy:" + legacyPassword + "@" + srcPG + "/legacydb"})
		if err != nil {
			t.Fatal(err)
		}
		if info.ImportedBytes == 0 {
			t.Errorf("info %+v", info)
		}
		_, pw, _ := c.dbCredentials("postgres", "loja")
		// tudo como o usuario do app, pela rede (com senha), como o app faz
		out := sqlIn(t, pg, `
SELECT count(*) FROM clientes;
SELECT count(*) FROM vendas.pedidos;
INSERT INTO clientes (nome, mail) VALUES ('novo', 'n@x.com') RETURNING id, nome;
INSERT INTO vendas.pedidos (cliente) VALUES (1) RETURNING id, st, length(token);
SELECT nextval('numero_nf');
REFRESH MATERIALIZED VIEW total;
SELECT * FROM total;
SELECT encode(lo_get((SELECT min(oid) FROM pg_largeobject_metadata)), 'escape');
ALTER TABLE clientes ADD COLUMN migracao int;
CREATE TABLE nova (x int);
SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
  WHERE n.nspname IN ('public', 'vendas') AND c.relkind IN ('r','v','m','S') AND pg_get_userbyid(c.relowner) <> 'loja';
`, "psql", "-v", "ON_ERROR_STOP=1", "-q", "-At", "postgres://loja:"+pw+"@127.0.0.1/loja")
		want := "5000\n20000\n5001|NOVO\n20001|aberto|16\n1000\n20001\narquivo\n0"
		if out != want {
			t.Errorf("como o usuario do app:\n%s\nesperava:\n%s", out, want)
		}
	})

	t.Run("mysql", func(t *testing.T) {
		if _, err := c.CreateDatabase(ctx, DatabaseRequest{Engine: "mysql", Name: "legado",
			Source: "mysql://legacy:" + legacyPassword + "@" + srcMy + "/legado"}); err != nil {
			t.Fatal(err)
		}
		_, pw, _ := c.dbCredentials("mysql", "legado")
		out := sqlIn(t, my, `
SELECT nome FROM caros ORDER BY id;
UPDATE produtos SET preco = 11 WHERE id = 1;
SELECT atualizado IS NOT NULL FROM produtos WHERE id = 1;
CALL conta(@n); SELECT @n;
SELECT count(*) FROM information_schema.VIEWS WHERE TABLE_SCHEMA = 'legado' AND DEFINER LIKE 'legacy@%';
`, "sh", "-c", `MYSQL_PWD="$1" exec mysql -h127.0.0.1 -ulegado -N -B legado`, "sh", pw)
		want := "café\nit's\n1\n3\n0"
		if out != want {
			t.Errorf("como o usuario do app:\n%s\nesperava:\n%s", out, want)
		}
	})

	t.Run("senha errada nao deixa nada", func(t *testing.T) {
		_, err := c.CreateDatabase(ctx, DatabaseRequest{Engine: "postgres", Name: "errada",
			Source: "postgres://legacy:errada@" + srcPG + "/legacydb"})
		if err == nil || !strings.Contains(err.Error(), "usuario ou senha da origem recusados") {
			t.Fatalf("err = %v", err)
		}
		if out := sqlIn(t, pg, "SELECT count(*) FROM pg_database WHERE datname = 'errada';\n",
			"psql", "-At", "-U", "postgres"); out != "0" {
			t.Error("o database pela metade ficou")
		}
		if _, _, err := c.dbCredentials("postgres", "errada"); err == nil {
			t.Error("a senha do database que nao ficou foi guardada")
		}
	})
}

const legacyPostgresSQL = `
CREATE ROLE legacy LOGIN PASSWORD '` + legacyPassword + `';
CREATE DATABASE legacydb OWNER legacy;
\connect legacydb
SET ROLE legacy;
CREATE EXTENSION pgcrypto;
CREATE SCHEMA vendas;
CREATE TYPE status AS ENUM ('aberto', 'pago');
CREATE DOMAIN email AS text CHECK (value LIKE '%@%');
CREATE TABLE clientes (id serial PRIMARY KEY, nome text NOT NULL, mail email, criado timestamptz DEFAULT now());
CREATE TABLE vendas.pedidos (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, cliente int REFERENCES clientes,
  st status DEFAULT 'aberto', token text DEFAULT encode(gen_random_bytes(8), 'hex'));
CREATE SEQUENCE numero_nf START 1000;
CREATE VIEW resumo AS SELECT c.nome, count(p.*) FROM clientes c LEFT JOIN vendas.pedidos p ON p.cliente = c.id GROUP BY c.nome;
CREATE MATERIALIZED VIEW total AS SELECT count(*) FROM vendas.pedidos;
CREATE FUNCTION grita() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.nome := upper(NEW.nome); RETURN NEW; END $$;
CREATE TRIGGER t_grita BEFORE INSERT ON clientes FOR EACH ROW EXECUTE FUNCTION grita();
INSERT INTO clientes (nome, mail) SELECT 'cliente ' || g, 'c' || g || '@x.com' FROM generate_series(1, 5000) g;
INSERT INTO vendas.pedidos (cliente) SELECT (g % 5000) + 1 FROM generate_series(1, 20000) g;
SELECT lo_from_bytea(0, 'arquivo'::bytea);
`

const legacyMySQLSQL = `
CREATE DATABASE legado;
CREATE USER 'legacy'@'%' IDENTIFIED BY '` + legacyPassword + `';
GRANT ALL ON legado.* TO 'legacy'@'%';
GRANT PROCESS ON *.* TO 'legacy'@'%';
USE legado;
CREATE TABLE produtos (id int AUTO_INCREMENT PRIMARY KEY, nome varchar(100), preco decimal(10,2), atualizado datetime)
  DEFAULT CHARSET utf8mb4;
INSERT INTO produtos (nome, preco) VALUES ('café', 10.5), ('pão', 1.25), ('it''s', 3);
CREATE DEFINER='legacy'@'%' VIEW caros AS SELECT * FROM produtos WHERE preco > 2;
CREATE DEFINER='legacy'@'%' TRIGGER t_upd BEFORE UPDATE ON produtos FOR EACH ROW SET NEW.atualizado = NOW();
CREATE DEFINER='legacy'@'%' PROCEDURE conta(OUT n INT) SELECT count(*) INTO n FROM produtos;
`

// stackImage e a imagem que a stack do cais usa (do template).
func stackImage(t *testing.T, stack string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "stacks", "templates", stack, "docker-compose.yml.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^\s*image:\s*(\S+)`).FindSubmatch(b)
	if m == nil {
		t.Fatalf("template de %s sem image", stack)
	}
	return string(m[1])
}

func randomSuffix(t *testing.T) string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func run(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func waitReady(t *testing.T, container string, cmd ...string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for {
		if exec.Command("docker", append([]string{"exec", container}, cmd...)...).Run() == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s nao ficou pronto", container)
		}
		time.Sleep(time.Second)
	}
}

// sqlIn roda o SQL pelo stdin num container e devolve a saida.
func sqlIn(t *testing.T, container, sql string, cmd ...string) string {
	t.Helper()
	out, err := docker.ExecInput(context.Background(), container, []byte(sql), cmd...)
	if err != nil {
		t.Fatalf("%s: %v", container, err)
	}
	return strings.TrimSpace(out)
}
