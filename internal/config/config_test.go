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
	if got.Email != want.Email || got.Hosts != want.Hosts ||
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
	os.WriteFile(path, []byte("email: a@exemplo.com\nstacks: [proxy, postgres]\n"), 0o644)

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
	os.WriteFile(path, []byte("email: a@exemplo.com\nemial: x\n"), 0o644)

	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "emial") {
		t.Fatalf("esperava erro citando o campo errado, veio %v", err)
	}
}

func TestValidateRecusa(t *testing.T) {
	cases := map[string]func(*Config){
		"selfsigned sem dominio": func(c *Config) { c.TLS = TLSSelfSigned },
		"garage sem host do s3":  func(c *Config) { c.Stacks = []string{"garage"} },
		"dominio sem ponto":      func(c *Config) { c.TestDomain = "localhost" },
		"dominio maiusculo":      func(c *Config) { c.TestDomain = "Exemplo.com" },
		"dominio com aspas":      func(c *Config) { c.TestDomain = `x".com` },
		"dominio com espaco":     func(c *Config) { c.Hosts.S3 = "a b.com" },
		"label com hifen final":  func(c *Config) { c.Hosts.S3 = "a-.com" },
		"email sem arroba":       func(c *Config) { c.Email = "admin.exemplo.com" },
		"email dominio invalido": func(c *Config) { c.Email = "a@b" },
		"email com aspas":        func(c *Config) { c.Email = `a"b@exemplo.com` },
		"host s3 invalido":       func(c *Config) { c.Hosts.S3 = "s3_x.exemplo.com" },
		"dominio .local":         func(c *Config) { c.TestDomain, c.TLS = "trd.local", TLSSelfSigned },
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
	local.TestDomain, local.TLS = "trd.localhost", TLSSelfSigned
	if err := local.Validate(); err != nil {
		t.Fatalf("recusou config de teste local: %v", err)
	}
}

func TestSaveRecusaConfigInvalida(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(path, Default()); err == nil {
		t.Fatal("gravou config sem e-mail")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("arquivo criado mesmo com config invalida")
	}
}

func TestBackupHorarioERetencao(t *testing.T) {
	c := Default()
	c.Email = "a@exemplo.com.br"
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

// nao ha dominio da VPS: a config so com o e-mail vale, o Garage precisa
// do host do S3 e o modo de teste do dominio do teste
func TestSemDominio(t *testing.T) {
	c := validConfig()
	if err := c.Validate(); err != nil {
		t.Fatalf("recusou config so com o e-mail: %v", err)
	}
	c.Stacks, c.Hosts.S3 = []string{"garage", "postgres"}, "arquivos.exemplo.com.br"
	if err := c.Validate(); err != nil || c.S3Host() != "arquivos.exemplo.com.br" {
		t.Fatalf("garage com host: %v (%q)", err, c.S3Host())
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(path, validConfig()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); strings.Contains(string(b), "domain") {
		t.Errorf("gravou dominio:\n%s", b)
	}
}

// config de quando havia o dominio base: o que ele fazia vai pros campos
// de agora (dominio do teste e host do S3), e o Save tira o campo
func TestLoadConfigComDominio(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(path, []byte("domain: trd.localhost\nemail: a@exemplo.com\ntls: selfsigned\nstacks: [postgres, garage]\n"), 0o644)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.TestDomain != "trd.localhost" || c.S3Host() != "s3.trd.localhost" {
		t.Errorf("migracao: dominio do teste %q, s3 %q", c.TestDomain, c.S3Host())
	}
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); strings.Contains(string(b), "\ndomain:") || !strings.Contains(string(b), "test_domain: trd.localhost") {
		t.Errorf("gravado:\n%s", b)
	}

	// em producao (Let's Encrypt) e sem Garage o dominio so some
	os.WriteFile(path, []byte("domain: exemplo.com.br\nemail: a@exemplo.com\n"), 0o644)
	if c, err := Load(path); err != nil || c.TestDomain != "" || c.Hosts.S3 != "" {
		t.Fatalf("producao: %+v %v", c, err)
	}
}

// fora de 80/443 com Let's Encrypt: avisa (nao bloqueia: serve pra teste)
func TestAvisoDePortasForaDoPadrao(t *testing.T) {
	c := validConfig()
	if c.PortsWarning() != "" {
		t.Fatalf("avisou nas portas padrao: %s", c.PortsWarning())
	}
	c.Proxy = Proxy{HTTPPort: 8080, HTTPSPort: 8443}
	if err := c.Validate(); err != nil {
		t.Fatalf("recusou porta de teste: %v", err)
	}
	if w := c.PortsWarning(); !strings.Contains(w, "8080/8443") || !strings.Contains(w, "Let's Encrypt") {
		t.Errorf("aviso: %q", w)
	}
	c.TLS, c.TestDomain = TLSSelfSigned, "trd.localhost"
	if c.PortsWarning() != "" {
		t.Error("avisou do Let's Encrypt no modo de teste (CA local)")
	}
}
