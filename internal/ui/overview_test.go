package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/docker"
)

func dashState(now time.Time) core.State {
	running := []docker.Container{{Service: "web", State: "running"}}
	return core.State{
		Environment: core.Environment{CheckedAt: now, Host: core.Host{Hostname: "vps", OS: "linux", Arch: "amd64"},
			Docker: core.Docker{Installed: true, CLI: core.Component{Version: "29"}, Daemon: core.Component{Version: "29"}, Compose: core.Component{Version: "2"}}},
		Apps: core.Apps{CheckedAt: now, List: []core.AppStatus{
			{App: apps.App{Name: "medko", Commit: "abc1234def", Last: &apps.Deploy{At: now.Add(-2 * time.Hour), Commit: "abc1234def"},
				History: []apps.Deploy{{At: now.Add(-2 * time.Hour), Commit: "abc1234def"}}}, Containers: running},
			{App: apps.App{Name: "pmiu", Commit: "111aaaa", Last: &apps.Deploy{At: now.Add(-time.Hour), Commit: "222bbbb", Error: "build", RolledBack: true},
				History: []apps.Deploy{{At: now.Add(-time.Hour), Commit: "222bbbb", Error: "build", RolledBack: true}}}, Containers: running},
			{App: apps.App{Name: "arbor", Commit: "333cccc"}, Containers: []docker.Container{{Service: "api", State: "exited", ExitCode: 1}}},
		}},
		Infra: core.Infra{CheckedAt: now, Stacks: []core.StackStatus{
			{Name: "proxy", Enabled: true, Installed: true, Containers: running},
			{Name: "garage", Enabled: true, Installed: true},
			{Name: "mysql"},
		}},
		Backup: core.BackupStatus{ClientID: "x", Email: "a@b", Recipient: "age1", Time: "03:00",
			Next: now.Add(14 * time.Hour), Last: &core.BackupRun{At: now.Add(-7 * time.Hour), Size: 2048}},
		Health: core.Health{Version: "v0.1.0", Latest: "v0.2.0",
			Certs: []core.Cert{{Domain: "medko.exemplo.com", NotAfter: now.Add(5 * 24 * time.Hour)}}},
	}
}

func TestDashboardVisaoGeralEAvisos(t *testing.T) {
	c, _, _ := testCore(t, okChecker)
	m := open(t, c)
	waitFor(t, m, "Pronto.")
	now := time.Now()
	m.state = dashState(now)
	m.apps.list = m.state.Apps.List

	out := screen(m)
	for _, want := range []string{
		"✗ App arbor: api parado (exited, saida 1)", "✗ Servico garage fora do ar",
		"! Ultimo deploy do pmiu falhou (a versao anterior continua no ar)",
		"! Certificado de medko.exemplo.com vence em 5 dias", "+1 avisos",
		"Apps  2 no ar · 1 com problema", "medko  no ar  abc1234 · ha 2h",
		"✓ proxy  ✗ garage", "✓ ha 7h · 2.0 KB · proximo", "Ultimos deploys", "pmiu   222bbbb  ha 1h · falhou, voltou",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("faltou %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "mysql") {
		t.Fatalf("servico desligado na visao geral:\n%s", out)
	}
	// a versao nova e o ultimo aviso (informativo), atras do +1
	list := m.alerts(now)
	if last := list[len(list)-1]; last.sev != sevInfo || !strings.Contains(last.text, "v0.2.0") {
		t.Fatalf("ultimo aviso: %+v", last)
	}

	// o aviso do app leva pro app na aba Apps
	click(t, m, "alert:0")
	if m.tab != tabApps || m.apps.selected() == nil || m.apps.selected().Name != "arbor" {
		t.Fatalf("clique no aviso: aba %d, app %v", m.tab, m.apps.selected())
	}
}

func TestDashboardTudoCerto(t *testing.T) {
	c, _, _ := testCore(t, okChecker)
	m := open(t, c)
	waitFor(t, m, "Pronto.")
	now := time.Now()
	st := dashState(now)
	st.Apps.List = st.Apps.List[:1]
	st.Infra.Stacks = st.Infra.Stacks[:1]
	st.Health = core.Health{Version: "v0.2.0", Latest: "v0.2.0"}
	m.state = st
	if out := screen(m); !strings.Contains(out, "✓ Tudo certo") {
		t.Fatalf("sem avisos:\n%s", out)
	}

	// backup sem configurar e atrasado
	st.Backup = core.BackupStatus{}
	m.state = st
	if !strings.Contains(screen(m), "Backups nao configurados") {
		t.Fatalf("backup sem configurar:\n%s", screen(m))
	}
	st.Backup = dashState(now).Backup
	st.Backup.Next = now.Add(-3 * time.Hour)
	m.state = st
	if !strings.Contains(screen(m), "Backup atrasado") {
		t.Fatalf("backup atrasado:\n%s", screen(m))
	}
}
