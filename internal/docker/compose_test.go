package docker

import "testing"

func TestParsePS(t *testing.T) {
	ndjson := `{"Name":"proxy-traefik","Service":"traefik","State":"running","Health":"healthy","Status":"Up 48 minutes (healthy)","Image":"traefik:v3.7.16","ExitCode":0}
{"Name":"proxy-docker-proxy","Service":"traefik-docker-proxy","State":"exited","Health":"","Status":"Exited (1) 2 minutes ago","Image":"tecnativa/docker-socket-proxy:v0.5.0"}`
	cs, err := parsePS(ndjson)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 || cs[0].Health != "healthy" || cs[1].State != "exited" || cs[1].Service != "traefik-docker-proxy" {
		t.Fatalf("%+v", cs)
	}

	// versoes antigas do compose devolvem uma lista
	cs, err = parsePS(`[{"Name":"postgres","Service":"postgres","State":"running","Health":"starting"}]`)
	if err != nil || len(cs) != 1 || cs[0].Health != "starting" {
		t.Fatalf("lista: %+v, %v", cs, err)
	}

	// projeto sem containers (derrubado): saida vazia, sem erro
	if cs, err := parsePS("\n"); err != nil || len(cs) != 0 {
		t.Fatalf("vazio: %+v, %v", cs, err)
	}
	if _, err := parsePS("isso nao e json"); err == nil {
		t.Fatal("aceitou saida invalida")
	}
}
