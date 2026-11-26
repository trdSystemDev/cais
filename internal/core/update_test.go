package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeUpdate finge a unidade cais-update: o teste diz o que o probe le.
type fakeUpdate struct {
	mu sync.Mutex
	p  UpdateProbe
}

func (f *fakeUpdate) set(p UpdateProbe) { f.mu.Lock(); f.p = p; f.mu.Unlock() }
func (f *fakeUpdate) probe(context.Context, time.Time, string) UpdateProbe {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.p
}

func updateDeps(t *testing.T, version string, started *[]string, fu *fakeUpdate) Deps {
	t.Helper()
	return Deps{
		Version:       version,
		DataDir:       t.TempDir(),
		LatestRelease: func(context.Context) (string, error) { return "v0.6.0", nil },
		SelfUpdate: func(_ context.Context, v, dir string) error {
			if _, err := os.Stat(dir); err != nil {
				t.Errorf("pasta do download nao existe: %v", err)
			}
			*started = append(*started, v)
			return nil
		},
		ProbeUpdate: fu.probe,
		ReleaseSize: func(context.Context, string) int64 { return 1000 },
		UpdateTick:  10 * time.Millisecond,
	}
}

// waitUpdate espera o estado da atualizacao chegar em ok.
func waitUpdate(t *testing.T, c *Core, ok func(*UpdateProgress) bool) *UpdateProgress {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st, _ := c.Snapshot(context.Background())
		if ok(st.Update) {
			return st.Update
		}
		time.Sleep(5 * time.Millisecond)
	}
	st, _ := c.Snapshot(context.Background())
	t.Fatalf("atualizacao no estado: %+v", st.Update)
	return nil
}

func TestUpdateCais(t *testing.T) {
	var started []string
	fu := &fakeUpdate{p: UpdateProbe{Active: true}}
	c := New(updateDeps(t, "v0.5.3", &started, fu))
	ctx := context.Background()
	if r, err := c.UpdateCais(ctx, ""); err != nil || !r.Started || r.From != "v0.5.3" || r.To != "v0.6.0" {
		t.Fatalf("mais novo: %+v %v", r, err)
	}
	// o andamento chega no estado: o passo do install.sh e o download
	fu.set(UpdateProbe{Active: true, Steps: []string{"Baixando o cais v0.6.0 (amd64) do release do GitHub"}, Bytes: 400})
	u := waitUpdate(t, c, func(u *UpdateProgress) bool { return u != nil && u.Bytes == 400 })
	if u.From != "v0.5.3" || u.To != "v0.6.0" || u.Total != 1000 || !strings.HasPrefix(u.Step, "Baixando") || !u.Running() {
		t.Fatalf("andamento: %+v", u)
	}
	// uma de cada vez
	if _, err := c.UpdateCais(ctx, "v0.4.0"); err == nil || !strings.Contains(err.Error(), "ja tem uma atualizacao") {
		t.Fatalf("segunda atualizacao: %v", err)
	}
	// a unidade terminou e o daemon continua aqui: falhou, com o motivo
	fu.set(UpdateProbe{Last: "install.sh: falhou o download de cais-linux-amd64"})
	u = waitUpdate(t, c, func(u *UpdateProgress) bool { return u != nil && u.Error != "" })
	if u.Error != "falhou o download de cais-linux-amd64" || u.Running() {
		t.Fatalf("falha: %+v", u)
	}

	// uma tag especifica, inclusive mais velha (voltar de versao)
	fu.set(UpdateProbe{Active: true})
	if r, err := c.UpdateCais(ctx, "v0.4.0"); err != nil || !r.Started || r.To != "v0.4.0" {
		t.Fatalf("voltar: %+v %v", r, err)
	}
	if r, err := New(updateDeps(t, "v0.5.3", &started, fu)).UpdateCais(ctx, "v0.5.3"); err != nil || r.Started {
		t.Fatalf("mesma versao: %+v %v", r, err)
	}
	if _, err := New(updateDeps(t, "v0.5.3", &started, fu)).UpdateCais(ctx, "latest; rm -rf /"); err == nil || !strings.Contains(err.Error(), "versao invalida") {
		t.Fatalf("tag invalida: %v", err)
	}
	if strings.Join(started, " ") != "v0.6.0 v0.4.0" {
		t.Fatalf("atualizacoes: %v", started)
	}

	// ja na mais nova: nada comeca
	started = nil
	c = New(updateDeps(t, "v0.6.0", &started, fu))
	if r, err := c.UpdateCais(ctx, ""); err != nil || r.Started || r.To != "v0.6.0" || len(started) > 0 {
		t.Fatalf("ja na mais nova: %+v %v %v", r, err, started)
	}

	// GitHub fora do ar; cais de desenvolvimento
	c = New(Deps{Version: "v0.5.3", LatestRelease: func(context.Context) (string, error) { return "", errors.New("timeout") }})
	if _, err := c.UpdateCais(ctx, ""); err == nil || !strings.Contains(err.Error(), "release mais novo") {
		t.Fatalf("sem GitHub: %v", err)
	}
	c = New(Deps{Version: "dev"})
	if _, err := c.UpdateCais(ctx, ""); err == nil || !strings.Contains(err.Error(), "versao dev") {
		t.Fatalf("dev: %v", err)
	}
}

// o probe de verdade le o binario baixando na pasta temporaria do install.sh
func TestProbeUpdateMedeODownload(t *testing.T) {
	dir := t.TempDir()
	tmp := filepath.Join(dir, "tmp.abc123")
	os.MkdirAll(tmp, 0o700)
	os.WriteFile(filepath.Join(tmp, "SHA256SUMS"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(tmp, "cais-linux-amd64"), make([]byte, 4096), 0o600)
	if p := probeUpdate(context.Background(), time.Now(), dir); p.Bytes != 4096 {
		t.Fatalf("bytes: %d", p.Bytes)
	}
}

func TestUpdateProgressLine(t *testing.T) {
	start := time.Date(2026, 11, 26, 13, 41, 0, 0, time.UTC)
	now := start.Add(85 * time.Second)
	cases := []struct {
		u    UpdateProgress
		want string
	}{
		{UpdateProgress{To: "v0.6.0", StartedAt: start}, "Atualizando o cais pra v0.6.0 · 1m25s · comecando"},
		{UpdateProgress{To: "v0.6.0", StartedAt: start, Step: "Baixando o cais v0.6.0 (amd64) do release do GitHub",
			Bytes: 6 << 20, Total: 12 << 20, Rate: 100 << 10},
			"Atualizando o cais pra v0.6.0 · 1m25s · baixando 6.0 MB de 12.0 MB (50%, 100.0 KB/s, falta ~1m2s)"},
		// sem o tamanho do release (o GitHub nao respondeu)
		{UpdateProgress{To: "v0.6.0", StartedAt: start, Bytes: 3 << 20}, "Atualizando o cais pra v0.6.0 · 1m25s · baixando 3.0 MB"},
		// o download acabou: o passo do install.sh
		{UpdateProgress{To: "v0.6.0", StartedAt: start, Step: "Reiniciando o daemon", Bytes: 12 << 20, Total: 12 << 20},
			"Atualizando o cais pra v0.6.0 · 1m25s · Reiniciando o daemon"},
	}
	for _, c := range cases {
		if got := c.u.Line(now); got != c.want {
			t.Errorf("\n got %q\nwant %q", got, c.want)
		}
	}
}

func TestParseUpdateLog(t *testing.T) {
	out := "==> Baixando o cais v0.6.0 (amd64) do release do GitHub\n" +
		"install.sh: falhou o download de cais-linux-amd64\n" +
		"curl: (28) Operation timed out\n"
	steps, last := parseUpdateLog(out)
	if len(steps) != 1 || steps[0] != "Baixando o cais v0.6.0 (amd64) do release do GitHub" {
		t.Errorf("passos: %q", steps)
	}
	// o die explica melhor que a ultima linha
	if last != "install.sh: falhou o download de cais-linux-amd64" {
		t.Errorf("motivo: %q", last)
	}
	if _, last := parseUpdateLog("==> Baixando\nalgo deu errado\n"); last != "algo deu errado" {
		t.Errorf("sem die: %q", last)
	}
}
