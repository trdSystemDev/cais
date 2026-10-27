package stacks

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/trdSystemDev/cais/internal/config"
)

const secretsDir = "/var/lib/cais/secrets"

func validConfig() config.Config {
	c := config.Default()
	c.Domain = "exemplo.com.br"
	c.Email = "admin@exemplo.com.br"
	c.Woodpecker = config.Woodpecker{GitHubClient: "Ov23liAbc123", Admin: "fulano"}
	return c
}

// compose e o pedaco do docker-compose.yml que os testes conferem.
type compose struct {
	Name     string `yaml:"name"`
	Services map[string]struct {
		Ports       []string          `yaml:"ports"`
		Environment map[string]string `yaml:"environment"`
		Labels      yaml.Node         `yaml:"labels"`
	} `yaml:"services"`
	Secrets map[string]struct {
		File string `yaml:"file"`
	} `yaml:"secrets"`
}

func render(t *testing.T, name string, c config.Config) (Rendered, compose) {
	t.Helper()
	r, err := Render(name, c, secretsDir)
	if err != nil {
		t.Fatal(err)
	}
	var cf compose
	if err := yaml.Unmarshal(r.Compose, &cf); err != nil {
		t.Fatalf("stack %s gerou YAML invalido: %v\n%s", name, err, r.Compose)
	}
	return r, cf
}

func TestTodasAsStacksRenderizam(t *testing.T) {
	for _, s := range All {
		t.Run(s.Name, func(t *testing.T) {
			r, cf := render(t, s.Name, validConfig())
			text := string(r.Compose)

			if cf.Name != s.Name {
				t.Errorf("projeto compose %q, esperava %q", cf.Name, s.Name)
			}
			if len(cf.Services) == 0 {
				t.Error("nenhum service")
			}
			// sobra do repo antigo ou placeholder sem valor
			for _, bad := range []string{"trds", "{{", "<no value>", "${"} {
				if strings.Contains(text, bad) {
					t.Errorf("compose contem %q", bad)
				}
			}

			// a lista de secrets e exatamente o que o compose referencia
			var files []string
			for _, sec := range cf.Secrets {
				if filepath.Dir(sec.File) != secretsDir {
					t.Errorf("secret fora de %s: %s", secretsDir, sec.File)
				}
				files = append(files, filepath.Base(sec.File))
			}
			slices.Sort(files)
			files = slices.Compact(files)
			got := slices.Clone(r.Secrets)
			slices.Sort(got)
			if !slices.Equal(files, got) {
				t.Errorf("Secrets %v, compose referencia %v", got, files)
			}
		})
	}
}

func TestProxyNasceVazioComPortasEEmail(t *testing.T) {
	c := validConfig()
	c.Proxy = config.Proxy{HTTPPort: 8080, HTTPSPort: 8443}
	_, cf := render(t, "proxy", c)

	bw := cf.Services["bunkerweb"]
	want := []string{"8080:8080", "8443:8443", "8443:8443/udp"}
	if !slices.Equal(bw.Ports, want) {
		t.Errorf("portas %v, esperava %v", bw.Ports, want)
	}
	sched := cf.Services["bw-scheduler"].Environment
	if sched["EMAIL_LETS_ENCRYPT"] != "admin@exemplo.com.br" {
		t.Errorf("EMAIL_LETS_ENCRYPT = %q", sched["EMAIL_LETS_ENCRYPT"])
	}
	if sched["SERVER_NAME"] != "" {
		t.Errorf("proxy tem que nascer sem sites, SERVER_NAME = %q", sched["SERVER_NAME"])
	}
	for k := range sched {
		if strings.Contains(k, "_REVERSE_PROXY_") {
			t.Errorf("site declarado no proxy: %s", k)
		}
	}
}

func TestHostsSaemDaConfig(t *testing.T) {
	c := validConfig()
	_, wp := render(t, "woodpecker", c)
	env := wp.Services["woodpecker-server"].Environment
	if env["WOODPECKER_HOST"] != "https://ci.exemplo.com.br" {
		t.Errorf("WOODPECKER_HOST = %q", env["WOODPECKER_HOST"])
	}
	if env["WOODPECKER_GITHUB_CLIENT"] != "Ov23liAbc123" || env["WOODPECKER_ADMIN"] != "fulano" {
		t.Errorf("github do woodpecker errado: %v", env)
	}

	c.Hosts.DB = "bancos.outro.com"
	r, _ := render(t, "dbgate", c)
	if !strings.Contains(string(r.Compose), `bunkerweb.SERVER_NAME: "bancos.outro.com"`) {
		t.Error("dbgate nao usou hosts.db")
	}
}

func TestEnabledValidaDependencias(t *testing.T) {
	cases := map[string]struct {
		stacks []string
		mutate func(*config.Config)
		errHas string
	}{
		"desconhecida":         {[]string{"proxy", "redis"}, nil, `"redis"`},
		"repetida":             {[]string{"proxy", "proxy"}, nil, "repetida"},
		"dbgate sem infra":     {[]string{"proxy", "dbgate"}, nil, `precisa da stack "infra"`},
		"woodpecker sem proxy": {[]string{"infra", "woodpecker"}, nil, `precisa da stack "proxy"`},
		"woodpecker sem github": {[]string{"proxy", "infra", "woodpecker"},
			func(c *config.Config) { c.Woodpecker = config.Woodpecker{} }, "github_client"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := validConfig()
			c.Stacks = tc.stacks
			if tc.mutate != nil {
				tc.mutate(&c)
			}
			_, err := Enabled(c)
			if err == nil || !strings.Contains(err.Error(), tc.errHas) {
				t.Fatalf("esperava erro com %q, veio %v", tc.errHas, err)
			}
		})
	}
}

func TestEnabledDevolveNaOrdemDeSubida(t *testing.T) {
	c := validConfig()
	c.Stacks = []string{"dbgate", "infra", "proxy"}
	got, err := Enabled(c)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range got {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "proxy,infra,dbgate" {
		t.Fatalf("ordem %v", names)
	}
}

func TestRenderRecusa(t *testing.T) {
	if _, err := Render("redis", validConfig(), secretsDir); err == nil {
		t.Error("renderizou stack desconhecida")
	}
	if _, err := Render("proxy", config.Default(), secretsDir); err == nil {
		t.Error("renderizou com config sem dominio")
	}
	if _, err := Render("proxy", validConfig(), "secrets"); err == nil {
		t.Error("aceitou pasta de secrets relativa")
	}
	c := validConfig()
	c.Woodpecker = config.Woodpecker{}
	if _, err := Render("woodpecker", c, secretsDir); err == nil {
		t.Error("renderizou woodpecker sem github")
	}
}

// Confere com o proprio docker compose que o arquivo gerado e valido. So
// roda onde tem docker.
func TestDockerComposeAceita(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker nao instalado")
	}
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skip("docker compose nao instalado")
	}
	dir := t.TempDir()
	secrets := filepath.Join(dir, "secrets")
	os.Mkdir(secrets, 0o700)

	for _, s := range All {
		t.Run(s.Name, func(t *testing.T) {
			r, err := Render(s.Name, validConfig(), secrets)
			if err != nil {
				t.Fatal(err)
			}
			for _, sec := range r.Secrets {
				os.WriteFile(filepath.Join(secrets, sec), []byte("x"), 0o444)
			}
			file := filepath.Join(dir, s.Name+".yml")
			os.WriteFile(file, r.Compose, 0o644)

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, "docker", "compose", "-f", file, "config", "--quiet").CombinedOutput()
			if err != nil {
				t.Fatalf("docker compose recusou: %v\n%s", err, out)
			}
		})
	}
}
