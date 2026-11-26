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
func traefikURLs(labels string) []string {
	if labels == "" {
		return nil
	}
	kv := map[string]string{}
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
