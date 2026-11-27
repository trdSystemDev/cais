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

// A tarefa que outro service espera terminar (service_completed_successfully)
// e que saiu com 0 nao e servico fora do ar; o service parado (Parar, saida
// 0) continua sendo.
func TestParsePSTarefa(t *testing.T) {
	out := `{"Name":"r-grade-1","Service":"grade","State":"exited","Status":"Exited (0) 2 minutes ago","ExitCode":0,"Labels":"com.docker.compose.project=r"}
{"Name":"r-meteo-1","Service":"meteo","State":"running","Status":"Up","Labels":"com.docker.compose.depends_on=grade:service_completed_successfully:false,web:service_healthy:false,com.docker.compose.project=r"}
{"Name":"r-web-1","Service":"web","State":"exited","Status":"Exited (0) 1 minute ago","ExitCode":0,"Labels":"com.docker.compose.project=r"}`
	cs, err := parsePS(out)
	if err != nil {
		t.Fatal(err)
	}
	if !cs[0].Job || !cs[0].Done() {
		t.Errorf("grade: %+v", cs[0])
	}
	if cs[1].Job || cs[1].Done() || cs[2].Job || cs[2].Done() {
		t.Errorf("meteo/web viraram tarefa: %+v %+v", cs[1], cs[2])
	}
	cs[0].ExitCode = 1
	if cs[0].Done() {
		t.Error("tarefa com erro contou como terminada")
	}
}
