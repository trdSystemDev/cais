package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validConfig() Config {
	c := Default()
	c.Domain = "exemplo.com.br"
	c.Email = "admin@exemplo.com.br"
	c.Woodpecker = Woodpecker{GitHubClient: "Ov23liAbc123", Admin: "fulano"}
	return c
}

func TestSaveLoadIdaEVolta(t *testing.T) {
	path := filepath.Join(t.TempDir(), "etc", "config.yaml")
	want := validConfig()
	want.Hosts.CI = "build.exemplo.com.br"
	want.Hosts.Registry = "imagens.exemplo.com.br"

	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Domain != want.Domain || got.Email != want.Email || got.Hosts != want.Hosts ||
		got.Proxy != want.Proxy || got.Woodpecker != want.Woodpecker ||
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
	os.WriteFile(path, []byte("domain: exemplo.com\nemail: a@exemplo.com\nstacks: [proxy, infra]\n"), 0o644)

	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Proxy.HTTPPort != 80 || c.Proxy.HTTPSPort != 443 {
		t.Errorf("portas padrao nao aplicadas: %+v", c.Proxy)
	}
	if strings.Join(c.Stacks, ",") != "proxy,infra" {
		t.Errorf("a lista do arquivo tem que substituir a padrao, veio %v", c.Stacks)
	}
	if c.CIHost() != "ci.exemplo.com" || c.DBHost() != "db.exemplo.com" || c.RegistryHost() != "registry.exemplo.com" {
		t.Errorf("hosts padrao errados: %s %s %s", c.CIHost(), c.DBHost(), c.RegistryHost())
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
		"host ci invalido":       func(c *Config) { c.Hosts.CI = "ci_x.exemplo.com" },
		"host registry invalido": func(c *Config) { c.Hosts.Registry = "registry" },
		"dominio .local":         func(c *Config) { c.Domain = "trd.local" },
		"tls desconhecido":       func(c *Config) { c.TLS = "acme" },
		"allow_from invalido":    func(c *Config) { c.DBGate.AllowFrom = []string{"10.0.0.0/33"} },
		"allow_from com nome":    func(c *Config) { c.DBGate.AllowFrom = []string{"vpn.exemplo.com"} },
		"porta zero":             func(c *Config) { c.Proxy.HTTPPort = 0 },
		"porta alta":             func(c *Config) { c.Proxy.HTTPSPort = 70000 },
		"portas iguais":          func(c *Config) { c.Proxy.HTTPSPort = c.Proxy.HTTPPort },
		"admin invalido":         func(c *Config) { c.Woodpecker.Admin = "-fulano" },
		"client com espaco":      func(c *Config) { c.Woodpecker.GitHubClient = "a b" },
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
	withAllow := validConfig()
	withAllow.DBGate.AllowFrom = []string{"203.0.113.7", "10.8.0.0/24", "2001:db8::/32"}
	if err := withAllow.Validate(); err != nil {
		t.Fatalf("recusou allow_from valido: %v", err)
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
