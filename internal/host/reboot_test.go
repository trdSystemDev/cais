package host

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"
)

// agendar uma vez: o timer em UTC e o arquivo com a hora; o reinicio
// pendente agendado sai resolvido na checagem
func TestRebootAgendado(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("so no Linux")
	}
	ctx := context.Background()
	f := newFake()
	f.files[rebootRequiredPath] = ""
	if it := checkReboot(ctx, f); it.OK || !strings.Contains(it.Fix, "cais host reboot 01:00") {
		t.Fatalf("pendente: %+v", it)
	}
	at := time.Now().Add(3 * time.Hour).Truncate(time.Minute)
	if err := ScheduleReboot(ctx, f, at, false); err != nil {
		t.Fatal(err)
	}
	i := f.index(t, "systemd-run --unit="+RebootAtUnit)
	if !strings.Contains(f.calls[i], "--on-calendar="+at.UTC().Format("2006-01-02 15:04:05")+" UTC") ||
		!strings.HasSuffix(f.calls[i], "/bin/systemctl reboot") {
		t.Errorf("timer: %s", f.calls[i])
	}
	r := RebootStatus(ctx, f)
	if !r.Pending || !r.At.Equal(at) || r.Auto {
		t.Errorf("status: %+v", r)
	}
	if it := checkReboot(ctx, f); !it.OK || !strings.Contains(it.Detail, at.Format("15:04")) {
		t.Errorf("agendado: %+v", it)
	}

	// o automatico nao mexe no agendado por alguem
	if r, err := EnsureAutoReboot(ctx, f, "01:00", time.Now()); err != nil || !r.At.Equal(at) || r.Auto {
		t.Errorf("automatico por cima do agendado: %+v %v", r, err)
	}

	if err := CancelReboot(ctx, f); err != nil {
		t.Fatal(err)
	}
	if r := RebootStatus(ctx, f); r.Scheduled() || f.Exists(rebootPlanPath) {
		t.Errorf("cancelado: %+v", r)
	}
	if err := ScheduleReboot(ctx, f, time.Now().Add(-time.Minute), false); err == nil {
		t.Error("agendou no passado")
	}
}

// o automatico: so agenda com reinicio pendente, pra proxima vez do
// horario; troca de horario reagenda; desligado ou sem pendente, desfaz
func TestRebootAutomatico(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("so no Linux")
	}
	ctx := context.Background()
	f := newFake()
	now := time.Date(2026, 11, 27, 14, 30, 0, 0, time.Local)

	if r, _ := EnsureAutoReboot(ctx, f, "01:00", now); r.Scheduled() {
		t.Fatalf("agendou sem pendente: %+v", r)
	}
	f.files[rebootRequiredPath] = ""
	// a hora do agendamento e conferida com o relogio de verdade
	now = time.Now()
	r, err := EnsureAutoReboot(ctx, f, "01:00", now)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(now.Year(), now.Month(), now.Day(), 1, 0, 0, 0, time.Local)
	if !want.After(now) {
		want = want.AddDate(0, 0, 1)
	}
	if !r.Auto || !r.At.Equal(want) {
		t.Fatalf("automatico: %+v, queria %v", r, want)
	}
	if st := RebootStatus(ctx, f); !st.Auto || !st.At.Equal(want) {
		t.Errorf("status: %+v", st)
	}
	// de novo, nada muda
	n := len(f.calls)
	EnsureAutoReboot(ctx, f, "01:00", now)
	for _, c := range f.calls[n:] {
		if strings.HasPrefix(c, "systemd-run") {
			t.Errorf("reagendou sem mudar o horario: %s", c)
		}
	}
	if r, _ := EnsureAutoReboot(ctx, f, "02:30", now); !r.Auto || r.At.Format("15:04") != "02:30" {
		t.Errorf("troca de horario: %+v", r)
	}
	if r, _ := EnsureAutoReboot(ctx, f, "", now); r.Scheduled() || RebootStatus(ctx, f).Scheduled() {
		t.Errorf("desligado: %+v", r)
	}
}

func TestRebootQuando(t *testing.T) {
	now := time.Date(2026, 11, 27, 14, 30, 0, 0, time.Local)
	for at, want := range map[time.Time]string{
		time.Date(2026, 11, 27, 23, 0, 0, 0, time.Local): "hoje as 23:00",
		time.Date(2026, 11, 28, 1, 0, 0, 0, time.Local):  "amanha as 01:00",
		time.Date(2026, 11, 30, 1, 0, 0, 0, time.Local):  "30/11 as 01:00",
	} {
		if got := When(at, now); got != want {
			t.Errorf("%v: %q, queria %q", at, got, want)
		}
	}
}
