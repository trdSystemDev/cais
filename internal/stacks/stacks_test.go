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
	"github.com/trdSystemDev/cais/internal/docker"
)

const secretsDir = "/var/lib/cais/secrets"

func validConfig() config.Config {
	c := config.Default()
	c.Domain = "exemplo.com.br"
	c.Email = "admin@exemplo.com.br"
	c.Stacks = []string{"postgres", "mysql"}
	return c
}

// command aceita as duas formas do compose: lista ou texto.
type command []string

func (c *command) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*c = strings.Fields(n.Value)
		return nil
	}
	var list []string
	err := n.Decode(&list)
	*c = list
	return err
}

// compose e o pedaco do docker-compose.yml que os testes conferem.
type compose struct {
	Name     string `yaml:"name"`
	Services map[string]struct {
		ContainerName string            `yaml:"container_name"`
		Ports         []string          `yaml:"ports"`
		Environment   map[string]string `yaml:"environment"`
		Command       command           `yaml:"command"`
		Labels        yaml.Node         `yaml:"labels"`
		Networks      []string          `yaml:"networks"`
		Logging       struct {
			Driver  string            `yaml:"driver"`
			Options map[string]string `yaml:"options"`
		} `yaml:"logging"`
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
			// sem limite o log enche o disco (o Traefik loga cada requisicao)
			for name, svc := range cf.Services {
				if svc.Logging.Driver != "json-file" || svc.Logging.Options["max-size"] == "" || svc.Logging.Options["max-file"] == "" {
					t.Errorf("%s sem limite de log: %+v", name, svc.Logging)
				}
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

	tr := cf.Services["traefik"]
	want := []string{"8080:80", "8443:443", "8443:443/udp"}
	if !slices.Equal(tr.Ports, want) {
		t.Errorf("portas %v, esperava %v", tr.Ports, want)
	}
	for _, flag := range []string{
		// sem isso o Traefik publica todo container do host
		"--providers.docker.exposedbydefault=false",
		// o docker.sock so chega pelo socket-proxy somente leitura
		"--providers.docker.endpoint=tcp://traefik-docker-proxy:2375",
		"--entrypoints.web.http.redirections.entrypoint.to=websecure",
		// o padrao de 60s corta upload grande dos apps
		"--entrypoints.websecure.transport.respondingtimeouts.readtimeout=30m",
		"--certificatesresolvers.le.acme.email=admin@exemplo.com.br",
		// a API que o deploy sem queda consulta, so no loopback do container
		"--api.insecure=true",
		"--entrypoints.traefik.address=127.0.0.1:8080",
		"--entrypoints.websecure.http.tls.certresolver=le",
	} {
		if !slices.Contains(tr.Command, flag) {
			t.Errorf("traefik sem %s", flag)
		}
	}
	// o deploy sem queda acha o Traefik por esse nome (docker.TraefikContainer)
	if tr.ContainerName != docker.TraefikContainer {
		t.Errorf("container_name %q, o rollout procura %q", tr.ContainerName, docker.TraefikContainer)
	}
	// nenhum site declarado no proxy: so os headers padrao
	var labels map[string]string
	tr.Labels.Decode(&labels)
	for k := range labels {
		if strings.Contains(k, ".routers.") {
			t.Errorf("site declarado no proxy: %s", k)
		}
	}
}

// labels de um servico publicado, ja decodificados
func labelsOf(t *testing.T, cf compose, service string) map[string]string {
	t.Helper()
	var labels map[string]string
	node := cf.Services[service].Labels // valor do mapa nao e enderecavel
	if err := node.Decode(&labels); err != nil {
		t.Fatal(err)
	}
	return labels
}

func TestBancosIsolados(t *testing.T) {
	c := validConfig()
	nets := map[string]bool{}
	for _, db := range []string{"postgres", "mysql"} {
		_, cf := render(t, db, c)
		if len(cf.Services) != 1 {
			t.Errorf("stack %s com %d servicos, esperava so o banco", db, len(cf.Services))
		}
		for name, svc := range cf.Services {
			// cada banco na propria rede: app de Postgres nao enxerga o MySQL
			if !slices.Equal(svc.Networks, []string{db + "_network"}) {
				t.Errorf("%s/%s nas redes %v, esperava so %s_network", db, name, svc.Networks, db)
			}
			nets[svc.Networks[0]] = true
			// so no loopback, pro tunel SSH; nunca em todas as interfaces
			if len(svc.Ports) != 1 || !strings.HasPrefix(svc.Ports[0], "127.0.0.1:") {
				t.Errorf("%s/%s tem que publicar so no 127.0.0.1: %v", db, name, svc.Ports)
			}
		}
	}
	if len(nets) != 2 {
		t.Errorf("bancos dividindo rede: %v", nets)
	}
}

// escolher um banco so gera os secrets dele
func TestSoOBancoEscolhido(t *testing.T) {
	c := validConfig()
	c.Stacks = []string{"proxy", "mysql"}
	names, err := Needed(c)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(names, "postgres_password") || !slices.Contains(names, "mysql_root_password") {
		t.Errorf("so mysql ativo, secrets %v", names)
	}
}

func TestTLSAutoassinadoParaTesteLocal(t *testing.T) {
	c := validConfig()
	c.Domain, c.TLS = "trd.localhost", config.TLSSelfSigned
	r, cf := render(t, "proxy", c)
	text := string(r.Compose)

	if !slices.Contains(r.Secrets, "tls_cert") || !slices.Contains(r.Secrets, "tls_key") {
		t.Errorf("proxy nao monta o certificado da CA local: %v", r.Secrets)
	}
	cmd := cf.Services["traefik"].Command
	if slices.ContainsFunc(cmd, func(f string) bool { return strings.Contains(f, "certresolver") || strings.Contains(f, "acme") }) {
		t.Error("modo autoassinado pede Let's Encrypt")
	}
	if !slices.Contains(cmd, "--providers.file.directory=/etc/traefik/dynamic") {
		t.Error("traefik sem o arquivo com o certificado padrao")
	}
	if !strings.Contains(text, "certFile: /run/secrets/tls_cert") {
		t.Error("certificado padrao nao aponta pro secret")
	}

	c.TLS = ""
	r, _ = render(t, "proxy", c)
	if slices.Contains(r.Secrets, "tls_cert") || strings.Contains(string(r.Compose), "providers.file") {
		t.Error("Let's Encrypt nao usa certificado da CA local")
	}
}

func TestEnabledValidaDependencias(t *testing.T) {
	cases := map[string]struct {
		stacks []string
		mutate func(*config.Config)
		errHas string
	}{
		"desconhecida": {[]string{"proxy", "redis"}, nil, `"redis"`},
		"repetida":     {[]string{"proxy", "proxy"}, nil, "repetida"},
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

// as fixas sobem sem estar na config; listar uma delas (config antiga) e aceito
func TestFixasSempreAtivas(t *testing.T) {
	for _, list := range [][]string{nil, {"proxy"}} {
		c := validConfig()
		c.Stacks = list
		got, err := Enabled(c)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, s := range got {
			names = append(names, s.Name)
		}
		if strings.Join(names, ",") != "proxy" {
			t.Errorf("stacks %v com config %v", names, list)
		}
	}
	for _, s := range All {
		if s.Optional == config.IsCore(s.Name) {
			t.Errorf("%s: Optional e config.CoreStacks divergem", s.Name)
		}
	}
}

func TestGaragePublicadoEmS3(t *testing.T) {
	c := validConfig()
	c.Stacks = []string{"garage"}
	r, cf := render(t, "garage", c)
	labels := labelsOf(t, cf, "garage")
	if labels["traefik.http.routers.garage.rule"] != "Host(`s3.exemplo.com.br`)" {
		t.Errorf("rule = %q", labels["traefik.http.routers.garage.rule"])
	}
	if !slices.Equal(r.Secrets, []string{"garage_rpc_secret", "garage_admin_token"}) {
		t.Errorf("secrets %v", r.Secrets)
	}
	if !slices.Contains(TLSHosts(c), "s3.exemplo.com.br") {
		t.Error("certificado local sem o s3")
	}
}

func TestEnabledDevolveNaOrdemDeSubida(t *testing.T) {
	c := validConfig()
	c.Stacks = []string{"garage", "mysql", "postgres", "proxy"}
	got, err := Enabled(c)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range got {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "proxy,postgres,mysql,garage" {
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

	// os dois modos de TLS geram compose diferentes (o local usa configs
	// com content inline)
	local := validConfig()
	local.Domain, local.TLS = "trd.localhost", config.TLSSelfSigned
	modes := map[string]config.Config{"letsencrypt": validConfig(), "selfsigned": local}

	for mode, c := range modes {
		for _, s := range All {
			t.Run(mode+"/"+s.Name, func(t *testing.T) {
				r, err := Render(s.Name, c, secrets)
				if err != nil {
					t.Fatal(err)
				}
				for _, sec := range r.Secrets {
					os.WriteFile(filepath.Join(secrets, sec), []byte("x"), 0o444)
				}
				file := filepath.Join(dir, mode+"-"+s.Name+".yml")
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
}
