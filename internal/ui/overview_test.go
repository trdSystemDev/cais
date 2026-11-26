package ui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/docker"
	"github.com/trdSystemDev/cais/internal/sysstat"
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
	// a versao do daemon no topo, com a nova quando houver
	if out := screen(m); !strings.Contains(out, "cais v0.2.0  ") || strings.Contains(out, "->") {
		t.Fatalf("versao no topo:\n%s", out)
	}
	st.Health.Latest = "v0.3.0"
	m.state = st
	if out := screen(m); !strings.Contains(out, "cais v0.2.0 -> v0.3.0 (ssh cais@vps update)") {
		t.Fatalf("versao nova no topo:\n%s", out)
	}
	st.Health.Latest = "v0.2.0"
	m.state = st

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

func TestDashboardQuemMaisUsa(t *testing.T) {
	c, _, _ := testCore(t, okChecker)
	m := open(t, c)
	waitFor(t, m, "Pronto.")
	now := time.Now()
	st := dashState(now)
	st.Apps.List[0].Containers = []docker.Container{{Name: "medko-web-1", Service: "web", State: "running"}, {Name: "medko-worker-1", Service: "worker", State: "running"}}
	st.Infra.Stacks[0].Containers = []docker.Container{{Name: "proxy-traefik", Service: "traefik", State: "running"}}
	m.state = st
	m.sys = core.SystemStats{At: now, CPU: 50, Now: sysstat.Sample{CPUs: 4, MemTotal: 16 << 30, MemUsed: 4 << 30,
		Disks: []sysstat.Disk{{Path: "/", Total: 100 << 30, Used: 10 << 30}}}}
	m.ctrs = core.ContainerUsage{At: now, List: []docker.ContainerStats{
		{Name: "medko-web-1", CPU: 100, Mem: 1 << 30}, {Name: "medko-worker-1", CPU: 60, Mem: 512 << 20},
		{Name: "proxy-traefik", CPU: 4, Mem: 64 << 20}, {Name: "portainer", CPU: 8, Mem: 200 << 20},
	}}

	list := m.consumers()
	if len(list) != 3 || list[0].name != "medko" || list[0].cpu != 40 || list[0].mem != 1536<<20 ||
		list[1].name != "portainer" || list[1].kind != "container" || list[2].kind != "servico" {
		t.Fatalf("donos: %+v", list)
	}
	out := screen(m)
	for _, want := range []string{"Quem mais usa", "medko      app", "40%", "1.5 GB", "portainer", "proxy      servico"} {
		if !strings.Contains(out, want) {
			t.Fatalf("faltou %q:\n%s", want, out)
		}
	}
	// o app na tabela abre ele na aba Apps
	m.apps.list = st.Apps.List
	m.ctrs.List = m.ctrs.List[:1] // so o medko: o clique nao cai no aviso de outro app
	click(t, m, "dash-app:medko")
	if m.tab != tabApps || m.apps.selected().Name != "medko" {
		t.Fatalf("clique: aba %d", m.tab)
	}

	// sem o docker stats o resto continua
	m.setTab(tabDashboard)
	m.ctrs = core.ContainerUsage{At: now, Error: "permissao negada"}
	if !strings.Contains(screen(m), "sem o docker stats: permissao negada") {
		t.Fatalf("erro:\n%s", screen(m))
	}
}

// o Atualizar do aviso de versao nova: o primeiro clique pergunta, o
// segundo pede ao daemon; quando ele reinicia, a tela diz pra abrir de novo
// probeFake finge a unidade cais-update pros testes da TUI.
type probeFake struct {
	mu sync.Mutex
	p  core.UpdateProbe
}

func (f *probeFake) set(p core.UpdateProbe) { f.mu.Lock(); f.p = p; f.mu.Unlock() }
func (f *probeFake) probe(context.Context, time.Time, string) core.UpdateProbe {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.p
}

func updateCore(t *testing.T, asked *[]string, pf *probeFake) *core.Core {
	t.Helper()
	root := t.TempDir()
	return core.New(core.Deps{TraefikMetrics: noTraffic, CheckDocker: okChecker, ConfigPath: filepath.Join(root, "config.yaml"),
		SecretsDir: filepath.Join(root, "secrets"), DataDir: root, Version: "v0.2.0",
		LatestRelease: func(context.Context) (string, error) { return "v0.3.0", nil },
		SelfUpdate:    func(_ context.Context, v, _ string) error { *asked = append(*asked, v); return nil },
		ProbeUpdate:   pf.probe,
		ReleaseSize:   func(context.Context, string) int64 { return 10 << 20 },
		UpdateTick:    10 * time.Millisecond,
		ComposePS:     func(context.Context, string) ([]docker.Container, error) { return nil, nil }})
}

func TestDashboardAtualiza(t *testing.T) {
	var asked []string
	pf := &probeFake{p: core.UpdateProbe{Active: true}}
	c := updateCore(t, &asked, pf)
	m := open(t, c)
	waitFor(t, m, "Pronto.")
	m.state.Health = core.Health{Version: "v0.2.0", Latest: "v0.3.0"}
	waitFor(t, m, "Versao nova do cais: v0.3.0 (rodando v0.2.0)")
	click(t, m, "dash-update")
	waitFor(t, m, "Confirma? A TUI fecha e os apps seguem no ar")
	if len(asked) > 0 {
		t.Fatal("atualizou no primeiro clique")
	}
	press(t, m, "u")
	waitFor(t, m, "Atualizando o cais pra v0.3.0")
	if strings.Join(asked, " ") != "v0.3.0" {
		t.Fatalf("pedidos: %v", asked)
	}
	// o andamento: o download com quanto falta, depois o passo do install.sh
	pf.set(core.UpdateProbe{Active: true, Steps: []string{"Baixando o cais v0.3.0 (amd64) do release do GitHub"}, Bytes: 5 << 20})
	waitFor(t, m, "baixando 5.0 MB de 10.0 MB (50%")
	pf.set(core.UpdateProbe{Active: true, Steps: []string{"Baixando", "Reiniciando o daemon"}, Bytes: 10 << 20})
	waitFor(t, m, "Reiniciando o daemon")
	m.Update(closedMsg{})
	if s := screen(m); !strings.Contains(s, "Abra o cais de novo em alguns segundos") || strings.Contains(s, "caiu") {
		t.Fatalf("depois do reinicio:\n%s", s)
	}
}

// abrir a TUI respeita as 6 horas entre as consultas do release; o
// Verificar (enter, r ou o botao) pergunta ao GitHub na hora
func TestDashboardVerificarPerguntaOReleaseNaHora(t *testing.T) {
	var mu sync.Mutex
	asks, latest := 0, "v0.2.0"
	root := t.TempDir()
	c := core.New(core.Deps{TraefikMetrics: noTraffic, CheckDocker: okChecker, ConfigPath: filepath.Join(root, "config.yaml"),
		SecretsDir: filepath.Join(root, "secrets"), Version: "v0.2.0",
		LatestRelease: func(context.Context) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			asks++
			return latest, nil
		},
		SelfUpdate: func(context.Context, string, string) error { return errors.New("sem atualizar nos testes") },
		ComposePS:  func(context.Context, string) ([]docker.Container, error) { return nil, nil }})
	count := func() int { mu.Lock(); defer mu.Unlock(); return asks }
	m := open(t, c)
	waitFor(t, m, "Pronto.")
	// a consulta de saude da abertura pergunta; a seguinte (outra abertura,
	// dentro das 6h) nao
	c.CheckHealth(context.Background())
	c.CheckHealth(context.Background())
	if count() != 1 {
		t.Fatalf("dentro das 6h: %d consultas", count())
	}

	mu.Lock()
	latest = "v0.3.0"
	mu.Unlock()
	press(t, m, "r")
	waitFor(t, m, "Versao nova do cais: v0.3.0 (rodando v0.2.0)")
	if count() != 2 {
		t.Fatalf("Verificar: %d consultas", count())
	}
	waitFor(t, m, "Atualizar")
}

// a atualizacao que falha diz o motivo e libera o Atualizar de novo
func TestDashboardAtualizacaoFalha(t *testing.T) {
	var asked []string
	pf := &probeFake{p: core.UpdateProbe{Active: true}}
	m := open(t, updateCore(t, &asked, pf))
	waitFor(t, m, "Pronto.")
	m.state.Health = core.Health{Version: "v0.2.0", Latest: "v0.3.0"}
	waitFor(t, m, "Versao nova do cais")
	press(t, m, "u")
	press(t, m, "u")
	waitFor(t, m, "Atualizando o cais pra v0.3.0")
	pf.set(core.UpdateProbe{Last: "install.sh: falhou o download de cais-linux-amd64"})
	waitFor(t, m, "falhou (o cais segue na v0.2.0): falhou o download de cais-linux-amd64")
	if !strings.Contains(screen(m), "Atualizar") {
		t.Fatalf("sem o Atualizar depois da falha:\n%s", screen(m))
	}
}

// a conexao caiu (o daemon reiniciou numa atualizacao): o Reconectar so
// fecha a TUI pedindo a reabertura depois que o daemon responde
func TestReconectarDepoisDaQueda(t *testing.T) {
	var asked []string
	m := open(t, updateCore(t, &asked, &probeFake{}))
	waitFor(t, m, "Pronto.")
	back := errors.New("connection refused")
	m.ready = func() error { return back }
	m.Update(closedMsg{})
	if s := screen(m); !strings.Contains(s, "Reconectar") || !strings.Contains(s, "A conexao com o daemon do cais caiu") {
		t.Fatalf("tela da queda:\n%s", s)
	}
	click(t, m, "reconnect")
	if m.reconnect || !strings.Contains(screen(m), "O daemon ainda nao voltou") {
		t.Fatalf("daemon fora do ar: reconnect=%v\n%s", m.reconnect, screen(m))
	}
	back = nil
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.reconnect || cmd == nil {
		t.Fatal("enter com o daemon de volta nao pediu a reabertura")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("o Reconectar nao fechou a TUI")
	}
}
