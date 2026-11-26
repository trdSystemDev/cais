package docker

import (
	"slices"
	"testing"
)

func TestTraefikURLs(t *testing.T) {
	cases := []struct {
		name, labels string
		want         []string
	}{
		{"sem labels", "", nil},
		{"sem traefik.enable", "traefik.http.routers.a.rule=Host(`a.com`)", nil},
		{"um dominio", "com.docker.compose.project=portal,traefik.enable=true,traefik.http.routers.portal.rule=Host(`portal.com`),traefik.http.routers.portal.entrypoints=websecure", []string{"https://portal.com"}},
		{"www e api por caminho", "traefik.enable=true,traefik.http.routers.p.rule=Host(`p.com`) || Host(`www.p.com`),traefik.http.routers.p-api.rule=Host(`p.com`) && PathPrefix(`/api`)", []string{"https://p.com", "https://www.p.com"}},
		{"virgula dentro da regra", "traefik.enable=true,traefik.http.routers.r.rule=HostRegexp(`^[a-z]{1,3}$`) || Host(`r.com`),x=y", []string{"https://r.com"}},
		// o middleware do proxy nao e endereco
		{"so middleware", "traefik.enable=true,traefik.http.middlewares.h.headers.stsSeconds=1", nil},
	}
	for _, c := range cases {
		if got := traefikURLs(c.labels); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, quero %v", c.name, got, c.want)
		}
	}
}

// a linha do docker compose ps --format json de verdade (o Garage da infra)
func TestParsePSComURLs(t *testing.T) {
	line := `{"ExitCode":0,"Health":"healthy","Image":"dxflrs/garage:v2.5.0","Labels":"com.docker.compose.container-number=1,traefik.http.routers.garage.rule=Host(` +
		"`s3.trd.localhost`" + `),com.docker.compose.depends_on=,com.docker.compose.service=garage,traefik.http.services.garage.loadbalancer.server.port=3900,traefik.enable=true,traefik.http.routers.garage.entrypoints=websecure","Name":"garage","Service":"garage","State":"running","Status":"Up 27 minutes (healthy)"}`
	cs, err := parsePS(line)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || !slices.Equal(cs[0].URLs, []string{"https://s3.trd.localhost"}) || cs[0].Health != "healthy" {
		t.Errorf("%+v", cs)
	}
}
