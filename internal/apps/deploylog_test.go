package apps

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/trdSystemDev/cais/internal/docker"
)

// o deploy grava os passos e a saida do build, linha a linha com o
// horario; o proximo deploy apaga o do anterior
func TestDeployGravaOLogDoDeploy(t *testing.T) {
	m, _, d := newManager(t)
	m.Create(App{Name: "web", Repo: "o/web"})
	if out, err := m.ReadDeployLog("web", docker.LogsOptions{}); err != nil || out != "" {
		t.Fatalf("antes do deploy: %q %v", out, err)
	}
	if _, err := m.Deploy(context.Background(), "web", "c1"); err != nil {
		t.Fatal(err)
	}
	out, err := m.ReadDeployLog("web", docker.LogsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"2026-10-28T10:00:00.000000000Z ==> deploy de web\n",
		"==> baixando o/web@c1 (commit c1)",
		"==> build (docker compose build --pull)",
		"Z #1 [web] build v1\n",
		"Z #2 DONE\n", // linha sem \n no fim do build entra antes do proximo passo
		"==> pronto: c1 no ar\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("falta %q em\n%s", want, out)
		}
	}
	if strings.Index(out, "#2 DONE") > strings.Index(out, "build pronto") {
		t.Errorf("saida do build depois do fim dele:\n%s", out)
	}

	// o deploy que falha diz por que e que voltou
	d.failUp["v2"] = true
	m.Deploy(context.Background(), "web", "c2")
	out, _ = m.ReadDeployLog("web", docker.LogsOptions{})
	if strings.Contains(out, "build v1") || !strings.Contains(out, "build v2") {
		t.Errorf("o log do deploy anterior ficou:\n%s", out)
	}
	if !strings.Contains(out, "voltando pra c1") || !strings.Contains(out, "==> falhou: container web is unhealthy") {
		t.Errorf("log do deploy que falhou:\n%s", out)
	}

	// tail e since
	if out, _ := m.ReadDeployLog("web", docker.LogsOptions{Tail: 1}); strings.Count(out, "\n") != 1 || !strings.Contains(out, "restaurada") {
		t.Errorf("tail 1: %q", out)
	}
	m.Now = func() time.Time { return time.Date(2026, 10, 28, 11, 0, 0, 0, time.UTC) }
	if out, err := m.ReadDeployLog("web", docker.LogsOptions{Since: "10m"}); err != nil || out != "" {
		t.Errorf("since 10m uma hora depois: %q %v", out, err)
	}
	if _, err := m.ReadDeployLog("web", docker.LogsOptions{Since: "ontem"}); err == nil {
		t.Error("since invalido passou")
	}
	if _, err := m.ReadDeployLog("outro", docker.LogsOptions{}); err == nil {
		t.Error("app que nao existe")
	}
}

// o compose recusado tambem fica no log, com o motivo
func TestLogDoDeployRecusado(t *testing.T) {
	m, src, _ := newManager(t)
	src.commits["c1"] = map[string]string{"outro.yml": "x"}
	m.Create(App{Name: "web", Repo: "o/web"})
	if _, err := m.Deploy(context.Background(), "web", "c1"); err == nil {
		t.Fatal("deploy sem compose passou")
	}
	out, _ := m.ReadDeployLog("web", docker.LogsOptions{})
	if !strings.Contains(out, "==> falhou (a versao no ar continua): o commit c1 nao tem o compose docker-compose.yml") {
		t.Errorf("log:\n%s", out)
	}
}

func TestDeployLogCortaNoLimite(t *testing.T) {
	l := &deployLog{f: mustTemp(t), now: time.Now}
	big := strings.Repeat("x", 1<<20) + "\n"
	for range 10 {
		l.Write([]byte(big))
	}
	l.Step("depois")
	if l.size > deployLogLimit || !l.cut {
		t.Errorf("tamanho %d, cortado %v", l.size, l.cut)
	}
}

func mustTemp(t *testing.T) *os.File {
	f, err := os.CreateTemp(t.TempDir(), "log")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}
