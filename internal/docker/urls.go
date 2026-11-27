package docker

import (
	"regexp"
	"slices"
	"strings"
)

// hostRule pega cada Host(`nome`) de uma regra do Traefik (o PathPrefix que
// vier junto fica de fora: o endereco e o do dominio).
var hostRule = regexp.MustCompile("Host\\(`([^`]+)`\\)")

// traefikURLs le os enderecos publicados nos labels do container, no formato
// do compose ps (k=v separados por virgula). Uma virgula dentro do valor
// (regra com HostRegexp, por exemplo) continua o label anterior.
// parseLabels le as labels do compose ps ("k=v,k=v"). Valor com virgula
// (regra do Traefik, depends_on) vem quebrado: o pedaco sem "=" volta pro
// valor anterior.
func parseLabels(labels string) map[string]string {
	kv := map[string]string{}
	if labels == "" {
		return kv
	}
	last := ""
	for _, part := range strings.Split(labels, ",") {
		k, v, ok := strings.Cut(part, "=")
		if !ok || strings.ContainsAny(k, " `()") {
			if last != "" {
				kv[last] += "," + part
			}
			continue
		}
		kv[k], last = v, k
	}
	return kv
}

func traefikURLs(labels string) []string {
	kv := parseLabels(labels)
	if kv["traefik.enable"] != "true" {
		return nil
	}
	var urls []string
	for k, v := range kv {
		if !strings.HasPrefix(k, "traefik.http.routers.") || !strings.HasSuffix(k, ".rule") {
			continue
		}
		for _, m := range hostRule.FindAllStringSubmatch(v, -1) {
			u := "https://" + m[1]
			if !slices.Contains(urls, u) {
				urls = append(urls, u)
			}
		}
	}
	slices.Sort(urls)
	return urls
}
