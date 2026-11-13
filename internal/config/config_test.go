package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validConfig() Config {
	c := Default()
	c.Domain = "exemplo.com.br"
	c.Email = "admin@exemplo.com.br"
	return c
}

func TestSaveLoadIdaEVolta(t *testing.T) {
	path := filepath.Join(t.TempDir(), "etc", "config.yaml")
	want := validConfig()
	want.Hosts.S3 = "arquivos.exemplo.com.br"
	want.GitHub.AppID = 42

	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Domain != want.Domain || got.Email != want.Email || got.Hosts != want.Hosts ||
		got.Proxy != want.Proxy || got.GitHub != want.GitHub ||
		strings.Join(got.Stacks, ",") != strings.Join(want.Stacks, ",") {
		t.Fatalf("lido diferente do gravado:\n got %+v\nwant %+v", got, want)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("permissao %o, esperava 644", perm)
	}
	// o temporario do rename nao pode sobrar
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("sobraram arquivos na pasta: %v", entries)
	}
}

func TestLoadSemArquivo(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nao-existe.yaml"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("esperava ErrNotFound, veio %v", err)
	}
}

func TestLoadAplicaPadroes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(path, []byte("domain: exemplo.com\nemail: a@exemplo.com\nstacks: [proxy, postgres]\n"), 0o644)

	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Proxy.HTTPPort != 80 || c.Proxy.HTTPSPort != 443 {
		t.Errorf("portas padrao nao aplicadas: %+v", c.Proxy)
	}
	if strings.Join(c.Stacks, ",") != "proxy,postgres" {
		t.Errorf("a lista do arquivo tem que substituir a padrao, veio %v", c.Stacks)
	}
	if c.S3Host() != "s3.exemplo.com" {
		t.Errorf("host padrao errado: %s", c.S3Host())
	}
}

// config de antes da simplificacao (com CI e registry) ainda abre, e o
// Save tira o que nao existe mais
func TestLoadConfigAntiga(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	old := "domain: exemplo.com\nemail: a@exemplo.com\nstacks: [proxy, registry, woodpecker]\n" +
		"woodpecker: {github_client: abc, admin: fulano}\nhosts: {ci: build.exemplo.com, registry: img.exemplo.com}\n"
	os.WriteFile(path, []byte(old), 0o644)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	for _, gone := range []string{"woodpecker", "registry", "ci:", "github_client"} {
		if strings.Contains(string(data), gone) {
			t.Errorf("%q continuou no config gravado:\n%s", gone, data)
		}
	}
}

func TestLoadRecusaCampoDesconhecido(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(path, []byte("domain: exemplo.com\nemail: a@exemplo.com\ndomian: x\n"), 0o644)

	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "domian") {
		t.Fatalf("esperava erro citando o campo errado, veio %v", err)
	}
}

func TestValidateRecusa(t *testing.T) {
	cases := map[string]func(*Config){
		"dominio vazio":          func(c *Config) { c.Domain = "" },
		"dominio sem ponto":      func(c *Config) { c.Domain = "localhost" },
		"dominio maiusculo":      func(c *Config) { c.Domain = "Exemplo.com" },
		"dominio com aspas":      func(c *Config) { c.Domain = `x".com` },
		"dominio com espaco":     func(c *Config) { c.Domain = "a b.com" },
		"label com hifen final":  func(c *Config) { c.Domain = "a-.com" },
		"email sem arroba":       func(c *Config) { c.Email = "admin.exemplo.com" },
		"email dominio invalido": func(c *Config) { c.Email = "a@b" },
		"email com aspas":        func(c *Config) { c.Email = `a"b@exemplo.com` },
		"host s3 invalido":       func(c *Config) { c.Hosts.S3 = "s3_x.exemplo.com" },
		"dominio .local":         func(c *Config) { c.Domain = "trd.local" },
		"tls desconhecido":       func(c *Config) { c.TLS = "acme" },
		"porta zero":             func(c *Config) { c.Proxy.HTTPPort = 0 },
		"porta alta":             func(c *Config) { c.Proxy.HTTPSPort = 70000 },
		"portas iguais":          func(c *Config) { c.Proxy.HTTPSPort = c.Proxy.HTTPPort },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := validConfig()
			mutate(&c)
			if err := c.Validate(); err == nil {
				t.Fatalf("aceitou config invalida: %+v", c)
			}
		})
	}
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("recusou config valida: %v", err)
	}
	local := validConfig()
	local.Domain, local.TLS = "trd.localhost", TLSSelfSigned
	if err := local.Validate(); err != nil {
		t.Fatalf("recusou config de teste local: %v", err)
	}
}

func TestSaveRecusaConfigInvalida(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(path, Default()); err == nil {
		t.Fatal("gravou config sem dominio")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("arquivo criado mesmo com config invalida")
	}
}

func TestBackupHorarioERetencao(t *testing.T) {
	c := Default()
	c.Domain, c.Email = "exemplo.com.br", "a@exemplo.com.br"
	for _, bad := range []Backup{{Time: "3:00"}, {Time: "24:00"}, {Time: "03:60"}, {RetentionDays: -1}, {Recipient: "ssh-ed25519 AAAA"}} {
		c.Backup = bad
		if c.Validate() == nil {
			t.Errorf("aceitou %+v", bad)
		}
	}
	c.Backup = Backup{Time: "03:30", RetentionDays: 7, Recipient: "age1qyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqs3290gq"}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 11, 13, 15, 0, 0, 0, time.UTC)
	if at, ok := c.Backup.At(day); !ok || !at.Equal(time.Date(2026, 11, 13, 3, 30, 0, 0, time.UTC)) {
		t.Errorf("horario %v %v", at, ok)
	}
	if (Backup{}).Retention() != 30 || c.Backup.Retention() != 7 {
		t.Error("retencao")
	}
	if _, ok := (Backup{}).At(day); ok {
		t.Error("sem horario devolveu horario")
	}
}
