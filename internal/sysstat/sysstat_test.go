package sysstat

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func fakeProc(t *testing.T, stat string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"stat": stat,
		"meminfo": "MemTotal:       16000000 kB\nMemFree:         1000000 kB\nMemAvailable:    12000000 kB\n" +
			"SwapTotal:       2000000 kB\nSwapFree:        1500000 kB\n",
		"loadavg": "0.52 0.40 0.31 1/400 1234\n",
		"uptime":  "93784.21 300000.00\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestRead(t *testing.T) {
	proc := fakeProc(t, "cpu  100 0 50 800 50 0 0 0 0 0\ncpu0 50 0 25 400 25 0 0 0 0 0\ncpu1 50 0 25 400 25 0 0 0 0 0\nintr 1\n")
	s, err := Read(proc)
	if err != nil {
		t.Fatal(err)
	}
	if s.CPUs != 2 || s.CPUBusy != 150 || s.CPUTotal != 1000 {
		t.Errorf("cpu: %+v", s)
	}
	if s.MemTotal != 16000000*1024 || s.MemUsed != 4000000*1024 || s.MemPercent() != 25 {
		t.Errorf("memoria: %d/%d", s.MemUsed, s.MemTotal)
	}
	if s.SwapUsed != 500000*1024 || s.SwapPercent() != 25 {
		t.Errorf("swap: %d/%d", s.SwapUsed, s.SwapTotal)
	}
	if s.Load != [3]float64{0.52, 0.40, 0.31} || s.Uptime != 93784*time.Second {
		t.Errorf("carga %v, uptime %v", s.Load, s.Uptime)
	}
}

func TestCPUPercent(t *testing.T) {
	a := Sample{CPUBusy: 100, CPUTotal: 1000}
	if p := CPUPercent(a, Sample{CPUBusy: 150, CPUTotal: 1200}); p != 25 {
		t.Errorf("uso %v, queria 25", p)
	}
	if p := CPUPercent(a, a); p != -1 {
		t.Errorf("sem intervalo: %v", p)
	}
}

func TestSemLinhaCPU(t *testing.T) {
	if _, err := Read(fakeProc(t, "intr 1\n")); err == nil {
		t.Error("leu sem a linha cpu")
	}
}

func TestDiscoUmaVezPorSistemaDeArquivos(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("statfs so no Linux")
	}
	dir := t.TempDir()
	s, err := Read("/proc", dir, filepath.Join(dir, "."), "/nao/existe")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Disks) != 1 || s.Disks[0].Path != dir || s.Disks[0].Total == 0 || s.Disks[0].Used > s.Disks[0].Total {
		t.Errorf("discos: %+v", s.Disks)
	}
}
