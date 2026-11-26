package stacks

import (
	"strings"
	"testing"
)

// a versao escolhida vai pro compose; sem ela, a padrao do cais
func TestVersaoNoCompose(t *testing.T) {
	c := validConfig()
	c.Stacks, c.Hosts.S3 = append(c.Stacks, "postgres", "mysql", "garage"), "s3.exemplo.com.br"
	r, _ := render(t, "postgres", c)
	if !strings.Contains(string(r.Compose), "image: postgis/postgis:"+Images["postgres"].Default+"\n") {
		t.Errorf("sem versao:\n%s", r.Compose)
	}
	c.Versions = map[string]string{"postgres": "18-3.5-alpine", "mysql": "8.4.5", "garage": "v2.6.1"}
	for name, want := range map[string]string{"postgres": "postgis/postgis:18-3.5-alpine", "mysql": "mysql:8.4.5", "garage": "dxflrs/garage:v2.6.1"} {
		if r, _ := render(t, name, c); !strings.Contains(string(r.Compose), "image: "+want+"\n") {
			t.Errorf("%s:\n%s", name, r.Compose)
		}
	}
}

func TestVersaoRecusada(t *testing.T) {
	for name, v := range map[string]map[string]string{
		"servico sem versao":   {"proxy": "v3.7.16"},
		"postgres sem a major": {"postgres": "alpine"},
		"postgres latest":      {"postgres": "latest"},
		"mysql sem minor":      {"mysql": "9"},
		"garage sem v":         {"garage": "2.5.0"},
	} {
		c := validConfig()
		c.Versions = v
		if err := CheckVersions(c); err == nil {
			t.Errorf("%s: passou %v", name, v)
		}
		if _, err := Render("postgres", c, secretsDir); err == nil {
			t.Errorf("%s: renderizou", name)
		}
	}
	c := validConfig()
	c.Versions = map[string]string{"postgres": "18.1-3.6-alpine", "mysql": "9.7-oracle", "garage": "v2.5.0"}
	if err := CheckVersions(c); err != nil {
		t.Error(err)
	}
}

func TestTrocaDeVersao(t *testing.T) {
	ok := [][3]string{
		{"postgres", "", "17-3.5-alpine"}, // nunca subiu: qualquer uma
		{"postgres", "18-3.6-alpine", "18-3.6-alpine"},
		{"postgres", "18-3.6-alpine", "18-3.5-alpine"}, // o PostGIS e do database, nao do formato
		{"postgres", "18-3.6-alpine", "18.1-3.6-alpine"},
		{"mysql", "8.4.5", "9.7"},
		{"mysql", "9.7", "9.7.1"},
		{"garage", "v2.5.0", "v2.6.1"},
		{"postgres", "imagem-de-fora", "18-3.6-alpine"}, // no ar sem formato: nao compara
	}
	for _, c := range ok {
		if err := CheckUpgrade(c[0], c[1], c[2]); err != nil {
			t.Errorf("%v: %v", c, err)
		}
	}
	bad := map[[3]string]string{
		{"postgres", "18-3.6-alpine", "19-3.6-alpine"}: "pg_upgrade",
		{"postgres", "18-3.6-alpine", "17-3.6-alpine"}: "pg_upgrade",
		{"mysql", "9.7", "8.4.5"}:                      "mais velha",
		{"mysql", "9.7.2", "9.7.1"}:                    "mais velha",
		{"garage", "v2.5.0", "v3.0.0"}:                 "garage migrate",
		{"garage", "v2.5.0", "v2.4.0"}:                 "mais velha",
	}
	for c, want := range bad {
		if err := CheckUpgrade(c[0], c[1], c[2]); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v: %v (queria %q)", c, err, want)
		}
	}
}

func TestTagOf(t *testing.T) {
	for img, want := range map[string]string{
		"postgis/postgis:18-3.6-alpine":           "18-3.6-alpine",
		"docker.io/postgis/postgis:18-3.6-alpine": "18-3.6-alpine",
		"postgres:18":     "",
		"postgis/postgis": "",
	} {
		if got := TagOf("postgres", img); got != want {
			t.Errorf("%s: %q", img, got)
		}
	}
	if got := TagOf("mysql", "mysql:9.7"); got != "9.7" {
		t.Errorf("mysql: %q", got)
	}
}
