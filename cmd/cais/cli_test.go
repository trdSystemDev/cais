package main

import (
	"bytes"
	"errors"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/backup"
	"github.com/trdSystemDev/cais/internal/bootstrap"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/core/coretest"
	"github.com/trdSystemDev/cais/internal/docker"
	"github.com/trdSystemDev/cais/internal/github"
)

// fakeDaemon e o core falso com o Close do daemonAPI.
type fakeDaemon struct{ *coretest.Recorder }

func (fakeDaemon) Close() error { return nil }

// testCLI e o cli com o core falso e a saida em buffers.
type testCLI struct {
	*cli
	fake      *coretest.Recorder
	out, errb *bytes.Buffer
}

// newTestCLI monta o cli; fake nil e sem daemon (conectar da erro).
func newTestCLI(fake *coretest.Recorder) *testCLI {
	t := &testCLI{fake: fake, out: &bytes.Buffer{}, errb: &bytes.Buffer{}}
	t.cli = &cli{out: t.out, err: t.errb, in: strings.NewReader(""), dial: func(string) (daemonAPI, error) {
		if fake == nil {
			return nil, errors.New("sem daemon")
		}
		return fakeDaemon{fake}, nil
	}}
	return t
}

func (t *testCLI) methods() []string {
	var ms []string
	for _, c := range t.fake.Take() {
		ms = append(ms, c.Method)
	}
	return ms
}

var when = time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)

// loja e um app com deploy feito, pra quem lista e mostra historico.
func loja() core.Apps {
	d := apps.Deploy{At: when, Commit: "c0ffee1234567"}
	return core.Apps{List: []core.AppStatus{{
		App:        apps.App{Name: "loja", Repo: "dono/loja", Commit: "c0ffee1234567", Last: &d, History: []apps.Deploy{d}},
		Containers: []docker.Container{{Name: "loja-web-1", Service: "web", State: "running"}},
	}}}
}

type cliCase struct {
	name    string
	args    string
	stdin   string
	setup   func(f *coretest.Recorder)
	code    int
	calls   []string // metodos do core, em ordem (nil: nao confere)
	out     []string // trechos da saida padrao
	errOut  []string // trechos da saida de erro
	noCalls bool     // nao pode chamar o core
}

func runCase(t *testing.T, c cliCase) *testCLI {
	t.Helper()
	fake := coretest.NewRecorder()
	if c.setup != nil {
		c.setup(fake)
	}
	tc := newTestCLI(fake)
	tc.in = strings.NewReader(c.stdin)
	code := tc.run(strings.Fields(c.args))
	if code != c.code {
		t.Fatalf("cais %s: saida %d, esperado %d\nstdout: %s\nstderr: %s", c.args, code, c.code, tc.out, tc.errb)
	}
	got := tc.methods()
	if c.noCalls && len(got) > 0 {
		t.Fatalf("cais %s: chamou o core: %v", c.args, got)
	}
	if c.calls != nil && !slices.Equal(got, c.calls) {
		t.Fatalf("cais %s: chamadas %v, esperado %v", c.args, got, c.calls)
	}
	for _, s := range c.out {
		if !strings.Contains(tc.out.String(), s) {
			t.Fatalf("cais %s: saida sem %q:\n%s", c.args, s, tc.out)
		}
	}
	for _, s := range c.errOut {
		if !strings.Contains(tc.errb.String(), s) {
			t.Fatalf("cais %s: stderr sem %q:\n%s", c.args, s, tc.errb)
		}
	}
	return tc
}

func TestCLIComandos(t *testing.T) {
	failed := func(f *coretest.Recorder) { f.SetErr(errors.New("o core recusou")) }
	cases := []cliCase{
		// gerais
		{name: "version", args: "version", out: []string{"cais dev"}, noCalls: true},
		{name: "help", args: "help app deploy", out: []string{"Uso: cais app deploy"}, noCalls: true},
		{name: "desconhecido", args: "nao-existe", code: 2, errOut: []string{"comando desconhecido: nao-existe"}, noCalls: true},
		{name: "-h sai com 0", args: "app deploy -h", out: []string{"Uso: cais app deploy"}, noCalls: true},
		{name: "flag invalida sai com 2", args: "app deploy x -nao-existe", code: 2, out: []string{"flag provided but not defined: -nao-existe"}, noCalls: true},
		{name: "state", args: "state", calls: []string{"Snapshot"}, out: []string{`"version"`}},
		{name: "check", args: "check", calls: []string{"CheckEnvironment"}},
		{name: "erro do core sai com 1", args: "state", setup: failed, code: 1, errOut: []string{"cais: o core recusou"}},

		// apps
		{name: "apps vazio", args: "apps", calls: []string{"CheckApps"}, out: []string{"Nenhum app"}},
		{name: "apps", args: "apps", setup: func(f *coretest.Recorder) { f.SetOut("CheckApps", loja()) },
			calls: []string{"CheckApps"}, out: []string{"loja", "dono/loja", "c0ffee1"}},
		{name: "app sem subcomando", args: "app", code: 1, noCalls: true},
		{name: "app argumentos errados", args: "app deploy", code: 1, errOut: []string{"argumentos errados"}, noCalls: true},
		{name: "app create", args: "app create loja dono/loja -compose prod.yml -auto on",
			setup: func(f *coretest.Recorder) {
				f.SetOut("CreateApp", apps.App{Name: "loja", Repo: "dono/loja", Compose: "prod.yml", AutoDeploy: true})
			},
			calls: []string{"CreateApp"}, out: []string{"App loja criado", "Deploy automatico"}},
		{name: "app create repo invalido", args: "app create loja nao-e-repo", code: 1, noCalls: true},
		{name: "app create auto invalido", args: "app create loja dono/loja -auto talvez", code: 1, errOut: []string{"on ou off"}, noCalls: true},
		{name: "app deploy", args: "app deploy loja -ref main",
			setup: func(f *coretest.Recorder) {
				f.SetOut("DeployApp", apps.DeployResult{App: "loja", Commit: "abcdef1234", Warnings: []string{"cuidado"}})
			},
			calls: []string{"DeployApp"}, out: []string{"aviso: cuidado", "loja no ar no commit abcdef1"}},
		{name: "app deploy falha", args: "app deploy loja", setup: failed, code: 1, calls: []string{"DeployApp"}, errOut: []string{"o core recusou"}},
		{name: "app rollback", args: "app rollback loja c0ffee",
			setup: func(f *coretest.Recorder) {
				f.SetOut("RollbackApp", apps.DeployResult{Commit: "c0ffee1234", Images: true})
			},
			calls: []string{"RollbackApp"}, out: []string{"com as imagens guardadas"}},
		{name: "app history", args: "app history loja", setup: func(f *coretest.Recorder) { f.SetOut("CheckApps", loja()) },
			calls: []string{"CheckApps"}, out: []string{"c0ffee1"}},
		{name: "app guide", args: "app guide loja", setup: func(f *coretest.Recorder) { f.SetOut("AppGuide", "services:\n") },
			calls: []string{"AppGuide"}, out: []string{"services:"}},
		{name: "app env lista so os nomes", args: "app env loja", setup: func(f *coretest.Recorder) { f.SetOut("AppEnv", map[string]string{"A": "1"}) },
			calls: []string{"AppEnv", "CheckApps"}, out: []string{"  A\n", "cais app env loja -show"}},
		{name: "app env -show", args: "app env loja -show", setup: func(f *coretest.Recorder) { f.SetOut("AppEnv", map[string]string{"A": "1"}) },
			calls: []string{"AppEnv", "CheckApps"}, out: []string{"A=1"}},
		{name: "app env sem eco", args: "app env loja JWT", stdin: "linha1\nlinha2\n", calls: []string{"AppEnv", "SetAppEnv"}},
		{name: "app env duas sem valor", args: "app env loja JWT OUTRA", stdin: "x", code: 1, errOut: []string{"so uma variavel sem ="}},
		{name: "app env grava", args: "app env loja B=2 -unset A",
			setup: func(f *coretest.Recorder) { f.SetOut("AppEnv", map[string]string{"A": "1"}) }},
		{name: "app env import stdin", args: "app env loja -import -", stdin: "C=3\n# comentario\n"},
		{name: "app restart", args: "app restart loja web", calls: []string{"RestartApp"}},
		{name: "app stop", args: "app stop loja", calls: []string{"StopApp"}},
		{name: "app logs", args: "app logs loja web", setup: func(f *coretest.Recorder) { f.SetOut("AppLogs", "linha 1\n") },
			calls: []string{"AppLogs"}, out: []string{"linha 1"}},
		{name: "app delete sem confirmar", args: "app delete loja", stdin: "outro\n", code: 1, noCalls: true},
		{name: "app delete -yes", args: "app delete loja -yes", calls: []string{"DeleteApp"}},
		{name: "app delete confirmado", args: "app delete loja", stdin: "loja\n", calls: []string{"DeleteApp"}, out: []string{"loja excluido"}},
		{name: "db delete confirmado", args: "db delete postgres loja", stdin: "loja\n", calls: []string{"DeleteDatabase"}},
		{name: "db delete sem confirmar", args: "db delete postgres loja", stdin: "outro\n", code: 1, noCalls: true},
		{name: "app set", args: "app set loja -branch develop -auto off", setup: func(f *coretest.Recorder) { f.SetOut("CheckApps", loja()) }, calls: []string{"CheckApps", "UpdateApp"}},
		{name: "app promote", args: "app promote hml -yes",
			setup: func(f *coretest.Recorder) {
				f.SetOut("CheckPromotion", apps.Promotion{From: "hml", To: "loja", Commit: "abcdef1234"})
				f.SetOut("PromoteApp", apps.DeployResult{App: "loja", Commit: "abcdef1234"})
			}},
		{name: "app db lista", args: "app db loja", setup: func(f *coretest.Recorder) { f.SetOut("CheckApps", loja()) }},
		{name: "app db liga", args: "app db loja postgres/loja"},
		{name: "app db desliga", args: "app db loja off postgres/loja"},
		{name: "app bucket lista", args: "app bucket loja", setup: func(f *coretest.Recorder) { f.SetOut("CheckApps", loja()) }},
		{name: "app bucket liga", args: "app bucket loja fotos"},
		{name: "app bucket desliga", args: "app bucket loja off fotos"},

		// bancos e buckets
		{name: "db list vazio", args: "db list", calls: []string{"Databases"}, out: []string{"Nenhum database"}},
		{name: "db list", args: "db list",
			setup: func(f *coretest.Recorder) {
				f.SetOut("Databases", []core.DatabaseInfo{{Engine: "postgres", Name: "loja"}})
			}, calls: []string{"Databases"}, out: []string{"loja"}},
		{name: "db create", args: "db create postgres loja -extensions postgis",
			setup: func(f *coretest.Recorder) {
				f.SetOut("CreateDatabase", core.DatabaseInfo{Engine: "postgres", Name: "loja"})
			},
			calls: []string{"CreateDatabase"}},
		{name: "db create sem nome", args: "db create postgres", code: 1, errOut: []string{"uso: cais db create"}, noCalls: true},
		{name: "db delete -yes", args: "db delete postgres loja -yes", calls: []string{"DeleteDatabase"}},
		{name: "db sem subcomando", args: "db", code: 1, noCalls: true},
		{name: "bucket list", args: "bucket list",
			setup: func(f *coretest.Recorder) { f.SetOut("Buckets", []core.BucketInfo{{Name: "fotos"}}) },
			calls: []string{"Buckets"}, out: []string{"fotos"}},
		{name: "bucket create", args: "bucket create fotos",
			setup: func(f *coretest.Recorder) { f.SetOut("CreateBucket", core.BucketInfo{Name: "fotos"}) },
			calls: []string{"CreateBucket"}},
		{name: "bucket delete -yes", args: "bucket delete fotos -yes", calls: []string{"DeleteBucket"}},

		// servicos e setup
		{name: "service sem acao", args: "service proxy", code: 1, noCalls: true},
		{name: "service restart", args: "service proxy restart", calls: []string{"ControlService"}},
		{name: "setup plan", args: "setup",
			setup: func(f *coretest.Recorder) { f.SetOut("PlanSetup", bootstrap.Plan{}) }},
		{name: "setup config", args: "setup config -email a@b.c -test-domain exemplo.com"},
		{name: "setup secret", args: "setup secret github_app_key", stdin: "-----BEGIN-----\n", calls: []string{"SetSecret"}},
		{name: "setup notify-test", args: "setup notify-test", calls: []string{"TestNotify"}},
		{name: "setup github-test", args: "setup github-test",
			setup: func(f *coretest.Recorder) { f.SetOut("TestGitHub", github.Check{}) }, calls: []string{"TestGitHub"}},

		// backup
		{name: "backup status", args: "backup status", calls: []string{"RefreshBackup"}},
		{name: "backup provider", args: "backup provider google", calls: []string{"SetBackupProvider", "RefreshBackup"}},
		{name: "backup client", args: "backup client id-123", stdin: "segredo\n", calls: []string{"RefreshBackup", "SetBackupClient"}},
		{name: "backup logout", args: "backup logout", calls: []string{"RefreshBackup", "DisconnectDrive"}},
		{name: "backup schedule", args: "backup schedule 03:00 -days 10", calls: []string{"SetBackupSchedule", "RefreshBackup"}},
		{name: "backup schedule off", args: "backup schedule off", calls: []string{"SetBackupSchedule", "RefreshBackup"}},
		{name: "backup now", args: "backup now"},
		{name: "backup files", args: "backup files",
			setup: func(f *coretest.Recorder) { f.SetOut("BackupFiles", []backup.File{{Name: "vps-2026-11-23.cais"}}) },
			calls: []string{"BackupFiles"}, out: []string{"vps-2026-11-23"}},
		{name: "backup key -yes", args: "backup key -yes", setup: func(f *coretest.Recorder) { f.SetOut("NewBackupKey", "AGE-SECRET-KEY-1X") },
			calls: []string{"RefreshBackup", "NewBackupKey"}, out: []string{"AGE-SECRET-KEY-1X"}},
		{name: "backup sem subcomando", args: "backup", code: 1, noCalls: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// Sem daemon, todo comando que precisa dele falha com a mensagem do dial.
func TestCLISemDaemon(t *testing.T) {
	tc := newTestCLI(nil)
	if code := tc.run([]string{"apps"}); code != 1 || !strings.Contains(tc.errb.String(), "sem daemon") {
		t.Fatalf("saida %d, stderr %q", code, tc.errb)
	}
}

// Todo comando da ajuda recusa flag desconhecida com saida 2 (o pacote flag
// mostra o erro e a ajuda), sem chegar no daemon.
func TestCLITodoComandoRecusaFlagInvalida(t *testing.T) {
	semFlags := map[string]bool{"tui": true, "version": true, "help": true}
	for _, s := range sections {
		for _, c := range s.cmds {
			if semFlags[c.name] {
				continue
			}
			args := strings.Fields(c.name)
			if c.name == "setup plan" {
				args = []string{"setup"} // o plano e o setup sem subcomando
			}
			t.Run(c.name, func(t *testing.T) {
				fake := coretest.NewRecorder()
				tc := newTestCLI(fake)
				code := tc.run(append(args, "-flag-que-nao-existe"))
				if code != 2 {
					t.Fatalf("saida %d, esperado 2\nstdout: %s\nstderr: %s", code, tc.out, tc.errb)
				}
				if !strings.Contains(tc.out.String(), "flag provided but not defined") {
					t.Fatalf("sem o erro da flag:\n%s", tc.out)
				}
				if calls := fake.Take(); len(calls) > 0 {
					t.Fatalf("chamou o core: %v", calls)
				}
			})
		}
	}
}

// VAR sozinho le o valor do stdin (sem eco no terminal), com varias linhas,
// e mantem as outras variaveis
func TestCLIAppEnvLeOValor(t *testing.T) {
	fake := coretest.NewRecorder()
	fake.SetOut("AppEnv", map[string]string{"A": "1"})
	tc := newTestCLI(fake)
	tc.in = strings.NewReader("-----BEGIN-----\nabc\n-----END-----\n")
	if code := tc.run([]string{"app", "env", "loja", "CHAVE"}); code != 0 {
		t.Fatalf("saida %d: %s", code, tc.errb)
	}
	calls := fake.Take()
	if len(calls) != 2 || calls[1].Method != "SetAppEnv" {
		t.Fatalf("chamadas %+v", calls)
	}
	want := map[string]string{"A": "1", "CHAVE": "-----BEGIN-----\nabc\n-----END-----"}
	if got, _ := calls[1].Args[1].(map[string]string); !maps.Equal(got, want) {
		t.Errorf("gravou %q", calls[1].Args[1])
	}
	if strings.Contains(tc.out.String(), "abc") {
		t.Errorf("o valor apareceu na saida: %s", tc.out)
	}
}

// cais update: pede ao daemon e espera ele voltar na versao nova
func TestCLIUpdate(t *testing.T) {
	updateWait, updatePoll = time.Second, time.Millisecond
	t.Cleanup(func() { updateWait, updatePoll = 10*time.Minute, 2*time.Second })

	runCase(t, cliCase{name: "ja na mais nova", args: "update",
		setup: func(f *coretest.Recorder) {
			f.SetOut("UpdateCais", core.UpdateResult{From: "v0.5.3", To: "v0.5.3"})
		},
		calls: []string{"UpdateCais"}, out: []string{"o cais ja esta na v0.5.3"}})

	runCase(t, cliCase{name: "atualiza", args: "update -version v0.6.0",
		setup: func(f *coretest.Recorder) {
			f.SetOut("UpdateCais", core.UpdateResult{From: "v0.5.3", To: "v0.6.0", Started: true})
			f.SetOut("CheckHealth", core.Health{Version: "v0.6.0"})
		},
		calls: []string{"UpdateCais", "CheckHealth"},
		out:   []string{"Atualizando de v0.5.3 pra v0.6.0", "Pronto: o cais esta na v0.6.0."}})

	runCase(t, cliCase{name: "nao voltou", args: "update", code: 1,
		setup: func(f *coretest.Recorder) {
			f.SetOut("UpdateCais", core.UpdateResult{From: "v0.5.3", To: "v0.6.0", Started: true})
			f.SetOut("CheckHealth", core.Health{Version: "v0.5.3"})
		},
		errOut: []string{"o daemon nao voltou na v0.6.0", "journalctl -u cais-update"}})
	runCase(t, cliCase{name: "argumento sobrando", args: "update agora", code: 1, noCalls: true, errOut: []string{"uso: cais update"}})
}

// cais setup github-connect: o link, o codigo pelo stdin e o link de instalar
func TestCLIGitHubConnect(t *testing.T) {
	runCase(t, cliCase{name: "conecta", args: "setup github-connect -org trdSystemDev", stdin: "codigo-do-github\n",
		setup: func(f *coretest.Recorder) {
			f.SetOut("GitHubConnectLink", "https://trdsystemdev.github.io/cais/github/?name=cais-x&org=trdSystemDev")
			f.SetOut("ConnectGitHub", core.GitHubConnected{AppID: 42, Name: "cais-x", Owner: "trdSystemDev",
				InstallURL: "https://github.com/apps/cais-x/installations/new"})
		},
		calls: []string{"GitHubConnectLink", "ConnectGitHub"},
		out: []string{"?name=cais-x&org=trdSystemDev", `Conectado: app "cais-x" (App ID 42) em trdSystemDev`,
			"https://github.com/apps/cais-x/installations/new"}})
	runCase(t, cliCase{name: "sem codigo", args: "setup github-connect", code: 1,
		calls: []string{"GitHubConnectLink"}, errOut: []string{"sem codigo"}})
}

// cais remote add/list/remove, a lista deste computador, e o cais -r NOME,
// que roda a TUI ou o comando na VPS pelo ssh
func TestCLIRemote(t *testing.T) {
	t.Setenv("CAIS_REMOTES", filepath.Join(t.TempDir(), "remotes.yaml"))
	var calls [][]string
	exit := 0
	stub := func(args []string, in io.Reader, out, errw io.Writer) error {
		calls = append(calls, args)
		if slices.Contains(args, "version") {
			io.WriteString(out, "cais v0.5.12 (commit abc, go1.27)\n")
		}
		if exit != 0 {
			return quietExit(exit)
		}
		return nil
	}
	run := func(args ...string) (*testCLI, int) {
		tc := newTestCLI(nil)
		tc.ssh = stub
		return tc, tc.run(args)
	}

	if tc, code := run("remote", "list"); code != 0 || !strings.Contains(tc.out.String(), "Nenhuma VPS") {
		t.Fatalf("lista vazia: %d %s", code, tc.out)
	}
	tc, code := run("remote", "add", "prod", "cais@prod.tail1234.ts.net")
	if code != 0 || !strings.Contains(tc.out.String(), "VPS prod adicionada") || !strings.Contains(tc.out.String(), "Respondeu: cais v0.5.12") {
		t.Fatalf("add: %d %s %s", code, tc.out, tc.errb)
	}
	if got := strings.Join(calls[0], " "); !strings.Contains(got, "BatchMode=yes") || !strings.HasSuffix(got, "-- cais@prod.tail1234.ts.net cais version") {
		t.Errorf("ssh do add: %s", got)
	}
	if _, code := run("remote", "add", "x", "-oProxyCommand=touch /tmp/x"); code != 1 {
		t.Errorf("destino que vira opcao do ssh: %d", code)
	}
	exit = 255 // o ssh nao entrou: grava, mas avisa
	tc, _ = run("remote", "add", "dev", "cais@dev")
	if !strings.Contains(tc.out.String(), "VPS dev adicionada") || !strings.Contains(tc.out.String(), "nao respondeu sem perguntar nada") {
		t.Errorf("add sem resposta: %s", tc.out)
	}
	exit = 0
	if tc, _ := run("remote"); !strings.Contains(tc.out.String(), "prod") || !strings.Contains(tc.out.String(), "cais@dev") {
		t.Errorf("list: %s", tc.out)
	}

	// cais -r: a TUI (com terminal) e um comando, com o codigo de saida dele
	calls = nil
	if _, code := run("-r", "prod"); code != 0 || strings.Join(calls[0][len(calls[0])-3:], " ") != "-- cais@prod.tail1234.ts.net cais" || !slices.Contains(calls[0], "-t") {
		t.Errorf("-r prod: %d %v", code, calls)
	}
	exit = 3
	if _, code := run("-r", "dev", "update", "-version", "v0.5.12"); code != 3 || !strings.HasSuffix(strings.Join(calls[1], " "), "cais@dev cais update -version v0.5.12") {
		t.Errorf("-r dev update: %d %v", code, calls[1])
	}
	exit = 0
	if tc, code := run("-r", "homolog"); code != 1 || !strings.Contains(tc.errb.String(), "as VPS sao prod, dev") {
		t.Errorf("-r de VPS que nao existe: %d %s", code, tc.errb)
	}
	if _, code := run("-r"); code != 2 {
		t.Errorf("-r sem nome: %d", code)
	}

	if tc, code := run("remote", "remove", "dev"); code != 0 || !strings.Contains(tc.out.String(), "tirada da lista") {
		t.Errorf("remove: %d %s", code, tc.out)
	}
	if _, code := run("remote", "remove", "dev"); code != 1 {
		t.Errorf("remove de novo: %d", code)
	}
}
