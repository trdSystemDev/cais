package apps

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/trdSystemDev/cais/internal/stacks"
)

// composeConfig e o pedaco do `docker compose config --format json` (o
// compose ja resolvido: interpolado, com ancoras e extends aplicados) que os
// avisos olham.
type composeConfig struct {
	Services map[string]struct {
		Privileged    bool     `json:"privileged"`
		CapAdd        []string `json:"cap_add"`
		Devices       []any    `json:"devices"`
		NetworkMode   string   `json:"network_mode"`
		Pid           string   `json:"pid"`
		ContainerName string   `json:"container_name"`
		Ports         []struct {
			HostIP    string `json:"host_ip"`
			Published string `json:"published"`
			Target    int    `json:"target"`
		} `json:"ports"`
		Volumes []struct {
			Type   string `json:"type"`
			Source string `json:"source"`
		} `json:"volumes"`
		Build *struct {
			Context string `json:"context"`
		} `json:"build"`
		Labels   map[string]string `json:"labels"`
		Networks map[string]any    `json:"networks"`
	} `json:"services"`
}

// Warnings olha o compose resolvido e avisa do que costuma dar problema ou
// dar acesso demais ao host. Nao bloqueia nada: o compose e do app. src e a
// pasta do codigo, secretsDir a dos secrets (montar de la e o esperado) e
// dotEnv o .env que o cais grava.
func Warnings(configJSON []byte, src, secretsDir, dotEnv string) []string {
	var c composeConfig
	if err := json.Unmarshal(configJSON, &c); err != nil {
		return []string{"nao consegui ler o compose resolvido pra conferir: " + err.Error()}
	}
	names := make([]string, 0, len(c.Services))
	for n := range c.Services {
		names = append(names, n)
	}
	sort.Strings(names)

	var out []string
	warn := func(svc, format string, args ...any) {
		out = append(out, svc+": "+fmt.Sprintf(format, args...))
	}
	for _, n := range names {
		s := c.Services[n]
		if s.Privileged {
			warn(n, "privileged da acesso total ao host")
		}
		if len(s.CapAdd) > 0 {
			warn(n, "cap_add %v da poderes sobre o host", s.CapAdd)
		}
		if len(s.Devices) > 0 {
			warn(n, "devices da acesso a dispositivos do host")
		}
		if s.NetworkMode == "host" || s.Pid == "host" {
			warn(n, "usa a rede ou os processos do host")
		}
		if s.ContainerName != "" {
			if _, ok := stacks.Get(s.ContainerName); ok {
				warn(n, "container_name %q e o mesmo de uma stack do cais: um dos dois nao sobe", s.ContainerName)
			}
		}
		for _, p := range s.Ports {
			if p.Published == "" {
				continue
			}
			where := "em todas as interfaces (aberta na internet)"
			if strings.HasPrefix(p.HostIP, "127.") {
				where = "no loopback"
			}
			warn(n, "publica a porta %s:%d %s; o acesso pelo dominio e pelo Traefik (labels), sem porta", p.Published, p.Target, where)
		}
		for _, v := range s.Volumes {
			if v.Type != "bind" {
				continue
			}
			host := filepath.Clean(v.Source)
			switch {
			case strings.HasSuffix(host, "docker.sock"):
				warn(n, "monta o docker.sock: controla o Docker (e o host) inteiro")
			case !within(host, src) && !within(host, secretsDir):
				warn(n, "monta %s do host, fora da pasta do app", v.Source)
			}
		}
		if s.Build != nil && within(filepath.Clean(dotEnv), filepath.Clean(s.Build.Context)) && !ignoresEnv(s.Build.Context) {
			warn(n, "o .env (com as variaveis do app) fica no contexto do build e nao esta no .dockerignore: um COPY . grava as variaveis na imagem")
		}
		if s.Labels["traefik.enable"] == "true" {
			if _, ok := s.Networks["proxy_network"]; !ok {
				warn(n, "tem labels do Traefik mas nao esta na proxy_network: o Traefik nao alcanca o service")
			}
		}
	}
	return out
}

// ignoresEnv diz se o .dockerignore do contexto tira o .env do build.
func ignoresEnv(context string) bool {
	data, err := os.ReadFile(filepath.Join(context, ".dockerignore"))
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(data), "\n") {
		l = strings.TrimPrefix(strings.TrimSpace(l), "/")
		l = strings.TrimPrefix(l, "**/")
		if l == ".env" || l == ".env*" || l == "*.env" || l == "*" || l == ".*" {
			return true
		}
	}
	return false
}

// within diz se p esta dentro de dir (ou e o proprio dir).
func within(p, dir string) bool {
	dir = filepath.Clean(dir)
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}
