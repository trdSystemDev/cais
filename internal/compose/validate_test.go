package compose

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var policy = Policy{App: "meu-app", Networks: []string{"proxy_network", "postgres_network", "mysql_network", "garage_network"}}

// compose realista de um app: web + worker, banco compartilhado, secrets
const validCompose = `
name: meu-app

x-base: &base
  image: registry.exemplo.com.br/meu-app:abc123
  restart: unless-stopped
  env_file: ./app.env
  secrets: [db_password]
  networks: [default, postgres_network]

services:
  web:
    <<: *base
    command: ["node", "server.js"]
    environment:
      DB_HOST: postgres
      DB_PASSWORD_FILE: /run/secrets/db_password
      PRICE: "$$10"
    expose: ["3000"]
    labels:
      com.exemplo.time: backend
    volumes:
      - uploads:/app/uploads
      - ./config:/app/config:ro
      - type: tmpfs
        target: /tmp
    healthcheck:
      test: ["CMD", "wget", "-q", "-O", "-", "http://localhost:3000/health"]
      interval: 10s
    security_opt: ["no-new-privileges:true"]
    cap_drop: [ALL]
    read_only: true
    deploy:
      resources:
        limits: {cpus: "1", memory: 512M}
    networks: [default, postgres_network, proxy_network]
  worker:
    <<: *base
    command: ["node", "worker.js"]
    depends_on: [web]

volumes:
  uploads:

networks:
  postgres_network:
    external: true
  proxy_network:
    external: true

secrets:
  db_password:
    file: ./secrets/db_password
`

func TestComposeValidoPassa(t *testing.T) {
	if err := Validate([]byte(validCompose), policy); err != nil {
		t.Fatalf("recusou compose valido:\n%v", err)
	}
}

// o que a validacao aceita o docker compose tambem tem que aceitar
func TestComposeValidoEValidoProDocker(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker nao instalado")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(validCompose), 0o644)
	os.WriteFile(filepath.Join(dir, "app.env"), []byte("A=1\n"), 0o644)
	os.Mkdir(filepath.Join(dir, "secrets"), 0o700)
	os.WriteFile(filepath.Join(dir, "secrets", "db_password"), []byte("x"), 0o444)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "compose", "-f", filepath.Join(dir, "docker-compose.yml"), "config", "--quiet")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docker compose recusou o compose de exemplo: %v\n%s", err, out)
	}
}

func TestComposeRecusa(t *testing.T) {
	svc := func(extra string) string {
		return "services:\n  web:\n    image: nginx\n" + extra
	}
	cases := map[string]struct {
		compose string
		where   string // trecho que tem que aparecer no erro
	}{
		// escalada direta pro host
		"privileged":           {svc("    privileged: true\n"), "services.web.privileged"},
		"privileged na ancora": {"x-b: &b {privileged: true}\nservices:\n  web:\n    <<: *b\n    image: nginx\n", "services.web.privileged"},
		"cap_add":              {svc("    cap_add: [SYS_ADMIN]\n"), "services.web.cap_add"},
		"devices":              {svc("    devices: [/dev/sda]\n"), "services.web.devices"},
		"pid host":             {svc("    pid: host\n"), "services.web.pid"},
		"ipc host":             {svc("    ipc: host\n"), "services.web.ipc"},
		"network_mode host":    {svc("    network_mode: host\n"), "services.web.network_mode"},
		"network_mode outro":   {svc("    network_mode: container:postgres\n"), "services.web.network_mode"},
		"seccomp off":          {svc("    security_opt: [seccomp=unconfined]\n"), "services.web.security_opt[0]"},
		"sysctls":              {svc("    sysctls: {kernel.shm_rmid_forced: 1}\n"), "services.web.sysctls"},
		"hook privilegiado":    {svc("    post_start:\n      - command: id\n        privileged: true\n"), "post_start[0].privileged"},
		"gpu":                  {svc("    deploy:\n      resources:\n        reservations:\n          devices: [{capabilities: [gpu]}]\n"), "reservations.devices"},
		"provider":             {svc("    provider:\n      type: model\n"), "services.web.provider"},

		// caminho do host
		"docker.sock":        {svc("    volumes: [/var/run/docker.sock:/var/run/docker.sock]\n"), "services.web.volumes[0]"},
		"raiz do host":       {svc("    volumes: [\"/:/host\"]\n"), "services.web.volumes[0]"},
		"sobe de pasta":      {svc("    volumes: [../outro-app/secrets:/x]\n"), "services.web.volumes[0]"},
		"home":               {svc("    volumes: [\"~/.ssh:/x\"]\n"), "services.web.volumes[0]"},
		"bind longo":         {svc("    volumes:\n      - type: bind\n        source: /etc\n        target: /x\n"), "services.web.volumes[0]"},
		"env_file do host":   {svc("    env_file: /etc/shadow\n"), "services.web.env_file[0]"},
		"env_file longo":     {svc("    env_file:\n      - path: ../../secrets/postgres_password\n"), "services.web.env_file[0]"},
		"secret de outro":    {svc("secrets:\n  s:\n    file: ../outro/secrets/s\n"), "secrets.s.file"},
		"secret absoluto":    {svc("secrets:\n  s:\n    file: /var/lib/cais/secrets/postgres_password\n"), "secrets.s.file"},
		"secret do ambiente": {svc("secrets:\n  s:\n    environment: AWS_SECRET_ACCESS_KEY\n"), "secrets.s.environment"},
		"config fora":        {svc("configs:\n  c:\n    file: /etc/passwd\n"), "configs.c.file"},

		// recursos de outras stacks
		"projeto de outro":    {"name: registry\n" + svc(""), "name"},
		"volume de outro":     {svc("volumes:\n  dados:\n    name: postgres_data\n"), "volumes.dados.name"},
		"volume externo":      {svc("volumes:\n  dados:\n    external: true\n"), "volumes.dados.external"},
		"volume driver_opts":  {svc("volumes:\n  dados:\n    driver_opts: {type: none, o: bind, device: /}\n"), "volumes.dados.driver_opts"},
		"rede do docker.sock": {svc("networks:\n  p:\n    external: true\n    name: woodpecker_docker_proxy_network\n"), "networks.p"},
		"rede externa direta": {svc("networks:\n  traefik-docker-proxy-network:\n    external: true\n"), "networks.traefik-docker-proxy-network"},
		"rede com nome":       {svc("networks:\n  n:\n    name: postgres_network\n"), "networks.n.name"},
		"container_name":      {svc("    container_name: registry\n"), "services.web.container_name"},
		"volumes_from":        {svc("    volumes_from: [container:postgres]\n"), "services.web.volumes_from"},

		// ambiente do daemon e roteamento
		"interpolacao":        {svc("    environment:\n      X: ${AWS_SECRET_ACCESS_KEY}\n"), "services.web.environment.X"},
		"interpolacao curta":  {"services:\n  web:\n    image: x/$HOME\n", "services.web.image"},
		"label traefik":       {svc("    labels:\n      traefik.http.routers.r.rule: Host(`registry.exemplo.com.br`)\n"), "traefik.http.routers.r.rule"},
		"label traefik lista": {svc("    labels: [\"traefik.tcp.routers.r.rule=HostSNI(`*`)\"]\n"), "traefik.tcp.routers.r.rule"},
		"label do compose":    {svc("    labels: {com.docker.compose.project: registry}\n"), "com.docker.compose.project"},

		// outros arquivos e build
		"build":     {"services:\n  web:\n    build: .\n", "services.web.build"},
		"extends":   {svc("    extends: {file: /etc/x.yml, service: a}\n"), "services.web.extends"},
		"include":   {"include: [/etc/outro.yml]\n" + svc(""), "include"},
		"ports":     {svc("    ports: [\"5432:5432\"]\n"), "services.web.ports"},
		"logging":   {svc("    logging: {driver: syslog}\n"), "services.web.logging.driver"},
		"sem image": {"services:\n  web:\n    command: x\n", "services.web: falta image"},

		// formato
		"vazio":          {"", "compose vazio"},
		"sem services":   {"volumes: {}\n", "services"},
		"dois docs":      {svc("") + "---\n" + svc(""), "mais de um documento"},
		"chave repetida": {svc("    image: outro\n"), "already defined"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := Validate([]byte(tc.compose), policy)
			if err == nil {
				t.Fatalf("aceitou:\n%s", tc.compose)
			}
			if !strings.Contains(err.Error(), tc.where) {
				t.Fatalf("erro nao cita %q:\n%v", tc.where, err)
			}
		})
	}
}

func TestComposeJuntaTodosOsProblemas(t *testing.T) {
	err := Validate([]byte("services:\n  web:\n    image: nginx\n    privileged: true\n    ports: [\"80:80\"]\n"), policy)
	if err == nil || strings.Count(err.Error(), "\n") != 1 {
		t.Fatalf("esperava os 2 problemas juntos:\n%v", err)
	}
	if !strings.Contains(err.Error(), "Traefik") {
		t.Errorf("o erro de ports devia dizer o que fazer:\n%v", err)
	}
}

func TestComposeGrandeDemais(t *testing.T) {
	big := "services:\n  web:\n    image: nginx\n" + strings.Repeat("# x\n", MaxSize/4+1)
	if err := Validate([]byte(big), policy); err == nil || !strings.Contains(err.Error(), "maximo") {
		t.Fatalf("aceitou compose de %d bytes: %v", len(big), err)
	}
}

// bomba de aliases (billion laughs) nao pode travar o daemon
func TestComposeBombaDeAliases(t *testing.T) {
	bomb := `a: &a ["x","x","x","x","x","x","x","x","x"]
b: &b [*a,*a,*a,*a,*a,*a,*a,*a,*a]
c: &c [*b,*b,*b,*b,*b,*b,*b,*b,*b]
d: &d [*c,*c,*c,*c,*c,*c,*c,*c,*c]
e: &e [*d,*d,*d,*d,*d,*d,*d,*d,*d]
f: &f [*e,*e,*e,*e,*e,*e,*e,*e,*e]
g: &g [*f,*f,*f,*f,*f,*f,*f,*f,*f]
h: &h [*g,*g,*g,*g,*g,*g,*g,*g,*g]
services: {web: {image: nginx, x-y: *h}}
`
	done := make(chan error, 1)
	go func() { done <- Validate([]byte(bomb), policy) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("aceitou a bomba")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("travou na bomba de aliases")
	}
}

func TestServicesLeImagemERedes(t *testing.T) {
	ss, err := Services([]byte(validCompose))
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 2 || ss[0].Name != "web" || ss[1].Name != "worker" {
		t.Fatalf("services %+v", ss)
	}
	if ss[0].Image != "registry.exemplo.com.br/meu-app:abc123" {
		t.Errorf("imagem %q (veio da ancora)", ss[0].Image)
	}
	if strings.Join(ss[0].Networks, ",") != "default,postgres_network,proxy_network" {
		t.Errorf("redes do web %v", ss[0].Networks)
	}

	ss, _ = Services([]byte("services:\n  a:\n    image: x\n  b:\n    image: y\n    networks:\n      rede: {aliases: [z]}\n"))
	if len(ss[0].Networks) != 0 || strings.Join(ss[1].Networks, ",") != "rede" {
		t.Errorf("redes %+v", ss)
	}
}
