package host

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/trdSystemDev/cais/internal/config"
)

// O reinicio agendado: um timer do systemd (transiente, some no reinicio)
// que roda o systemctl reboot na hora marcada, e um arquivo em /run com a
// hora e quem agendou (tambem some no reinicio). Como o Corrigir, o
// systemctl reboot espera os deploys e backups em andamento.
const (
	// RebootAtUnit e o timer do reinicio agendado (o de agora e o RebootUnit).
	RebootAtUnit   = "cais-reboot-at"
	rebootPlanPath = "/run/cais-reboot-at"
)

// Reboot e o reinicio da VPS: se uma atualizacao espera por ele e quando
// ele esta agendado.
type Reboot struct {
	// Pending: uma atualizacao (kernel ou biblioteca) so vale depois de
	// reiniciar.
	Pending bool `json:"pending,omitempty"`
	// At e a hora agendada (zero: nada agendado).
	At time.Time `json:"at,omitzero"`
	// Auto: agendado pelo horario automatico (reboot_time da config), e nao
	// por alguem.
	Auto bool `json:"auto,omitempty"`
}

// Scheduled diz se ha reinicio agendado.
func (r Reboot) Scheduled() bool { return !r.At.IsZero() }

// RebootStatus le o reinicio pendente e o agendado. Fora do Linux ou sem
// root, nada.
func RebootStatus(ctx context.Context, sys System) Reboot {
	if runtime.GOOS != "linux" || sys.Geteuid() != 0 {
		return Reboot{}
	}
	r := Reboot{Pending: sys.Exists(rebootRequiredPath)}
	if out, _ := sys.Run(ctx, "systemctl", "is-active", RebootAtUnit+".timer"); strings.TrimSpace(out) != "active" {
		return r
	}
	b, err := sys.ReadFile(rebootPlanPath)
	if err != nil {
		return r
	}
	at, who, _ := strings.Cut(strings.TrimSpace(string(b)), " ")
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return r
	}
	r.At, r.Auto = t.Local(), who == "auto"
	return r
}

// ScheduleReboot agenda o reinicio pra at, no lugar do que estiver
// agendado; auto marca que foi o horario automatico.
func ScheduleReboot(ctx context.Context, sys System, at time.Time, auto bool) error {
	if sys.Geteuid() != 0 {
		return errors.New("precisa de root")
	}
	if !at.After(time.Now()) {
		return errors.New("o horario ja passou")
	}
	if err := CancelReboot(ctx, sys); err != nil {
		return err
	}
	who := "manual"
	if auto {
		who = "auto"
	}
	if err := sys.WriteFile(rebootPlanPath, []byte(at.Format(time.RFC3339)+" "+who+"\n"), 0o644); err != nil {
		return err
	}
	// em UTC: o systemd le a hora sem fuso no fuso dele, que pode nao ser
	// o do cais
	if _, err := sys.Run(ctx, "systemd-run", "--unit="+RebootAtUnit,
		"--on-calendar="+at.UTC().Format("2006-01-02 15:04:05")+" UTC", "--timer-property=AccuracySec=1s",
		"--description=cais: reinicia a VPS no horario agendado",
		"/bin/systemctl", "reboot"); err != nil {
		sys.Remove(rebootPlanPath)
		return errors.New("nao deu pra agendar o reinicio: " + firstLine(err))
	}
	return nil
}

// CancelReboot desfaz o reinicio agendado (sem nada agendado, nada faz).
func CancelReboot(ctx context.Context, sys System) error {
	if sys.Geteuid() != 0 {
		return errors.New("precisa de root")
	}
	sys.Run(ctx, "systemctl", "stop", RebootAtUnit+".timer")
	sys.Run(ctx, "systemctl", "reset-failed", RebootAtUnit+".timer", RebootAtUnit+".service")
	if sys.Exists(rebootPlanPath) {
		return sys.Remove(rebootPlanPath)
	}
	return nil
}

// When escreve a hora do reinicio perto de now: "hoje as 01:00", "amanha
// as 01:00" ou "28/11 as 01:00".
func When(at, now time.Time) string {
	at, now = at.Local(), now.Local()
	day := at.Format("02/01")
	switch d := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.Local).
		Sub(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)); {
	case d == 0:
		day = "hoje"
	case d > 0 && d <= 25*time.Hour:
		day = "amanha"
	}
	return fmt.Sprintf("%s as %s", day, at.Format("15:04"))
}

// EnsureAutoReboot aplica o horario automatico (HH:MM; vazio desliga): com
// um reinicio pendente, agenda pra proxima vez desse horario; sem pendente
// ou sem horario, desfaz o que ele agendou. O agendado por alguem fica como
// esta. Devolve o reinicio depois disso.
func EnsureAutoReboot(ctx context.Context, sys System, clock string, now time.Time) (Reboot, error) {
	r := RebootStatus(ctx, sys)
	if runtime.GOOS != "linux" || sys.Geteuid() != 0 || (r.Scheduled() && !r.Auto) {
		return r, nil
	}
	if clock == "" || !r.Pending {
		if r.Auto {
			if err := CancelReboot(ctx, sys); err != nil {
				return r, err
			}
			r.At, r.Auto = time.Time{}, false
		}
		return r, nil
	}
	want, ok := config.NextClock(now, clock)
	if !ok {
		return r, fmt.Errorf("horario %q invalido (use HH:MM, ex: 01:00)", clock)
	}
	if r.Auto && r.At.Equal(want) {
		return r, nil
	}
	if err := ScheduleReboot(ctx, sys, want, true); err != nil {
		return r, err
	}
	r.At, r.Auto = want, true
	return r, nil
}
