package rpc

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/docker"
)

func okStatus(context.Context) docker.Status {
	return docker.Status{ClientVersion: "29.0.1", ServerVersion: "29.0.1", ComposeVersion: "2.40.0"}
}

type daemon struct {
	path   string
	core   *core.Core
	cancel context.CancelFunc
	done   chan error
	once   sync.Once
}

// startDaemon sobe um daemon de verdade num socket temporario.
func startDaemon(t *testing.T, check func(context.Context) docker.Status) *daemon {
	t.Helper()
	return startDaemonDeps(t, core.Deps{CheckDocker: check})
}

// startDaemonDeps e como startDaemon, com as dependencias do core escolhidas
// pelo teste (caminhos temporarios, docker falso).
func startDaemonDeps(t *testing.T, deps core.Deps) *daemon {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cais.sock")
	ln, err := Listen(path, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &daemon{path: path, core: core.New(deps), cancel: cancel, done: make(chan error, 1)}
	go func() { d.done <- Serve(ctx, ln, d.core) }()
	t.Cleanup(d.stop)
	return d
}

// stop derruba o daemon e espera o Serve terminar. Pode ser chamado mais de
// uma vez (o teste chama e o Cleanup chama de novo).
func (d *daemon) stop() {
	d.once.Do(func() {
		d.cancel()
		select {
		case err := <-d.done:
			if err != nil {
				panic(err)
			}
		case <-time.After(3 * time.Second):
			panic("Serve nao terminou depois do cancelamento")
		}
	})
}

func dial(t *testing.T, path string) *Client {
	t.Helper()
	c, err := Dial(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func recv(t *testing.T, ch <-chan core.State, until func(core.State) bool) core.State {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case s, ok := <-ch:
			if !ok {
				t.Fatal("canal fechado")
			}
			if until(s) {
				return s
			}
		case <-deadline:
			t.Fatal("estado esperado nao chegou")
		}
	}
}

func TestClienteChamaAcaoERecebeResultado(t *testing.T) {
	d := startDaemon(t, okStatus)
	c := dial(t, d.path)

	env, err := c.CheckEnvironment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !env.Ready() || env.Docker.Compose.Version != "2.40.0" {
		t.Fatalf("resultado errado: %+v", env)
	}

	st, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Environment != env {
		t.Fatalf("snapshot remoto diverge:\n%+v\n%+v", st.Environment, env)
	}
}

// o cenario que motivou o daemon: o MCP age e a TUI aberta ve na hora
func TestAcaoDeUmClienteChegaNaInscricaoDeOutro(t *testing.T) {
	d := startDaemon(t, okStatus)
	tui := dial(t, d.path)
	mcp := dial(t, d.path)

	states, unsubscribe, err := tui.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	recv(t, states, func(s core.State) bool { return s.Version == 0 })

	if _, err := mcp.CheckEnvironment(context.Background()); err != nil {
		t.Fatal(err)
	}
	recv(t, states, func(s core.State) bool { return s.Environment.Ready() })
}

func TestDuasInscricoesNoMesmoCliente(t *testing.T) {
	d := startDaemon(t, okStatus)
	c := dial(t, d.path)

	a, ua, err := c.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer ua()
	b, ub, err := c.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer ub()

	d.core.CheckEnvironment(context.Background())
	recv(t, a, func(s core.State) bool { return s.Environment.Ready() })
	recv(t, b, func(s core.State) bool { return s.Environment.Ready() })
}

func TestDaemonCaiFechaInscricaoEFalhaChamadas(t *testing.T) {
	d := startDaemon(t, okStatus)
	c := dial(t, d.path)
	states, unsubscribe, err := c.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe() // depois do fechamento pelo readLoop: nao pode dar panic
	<-states

	d.stop()

	deadline := time.After(3 * time.Second)
	for closed := false; !closed; {
		select {
		case _, ok := <-states:
			closed = !ok
		case <-deadline:
			t.Fatal("canal da inscricao nao fechou quando o daemon caiu")
		}
	}
	if _, err := c.Snapshot(context.Background()); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("esperava ErrDisconnected, veio %v", err)
	}
}

func TestAcaoTerminaMesmoSeClienteDesconectar(t *testing.T) {
	release := make(chan struct{})
	var finished atomic.Bool
	d := startDaemon(t, func(ctx context.Context) docker.Status {
		<-release
		if ctx.Err() == nil {
			finished.Store(true)
		}
		return okStatus(ctx)
	})
	c := dial(t, d.path)

	go c.CheckEnvironment(context.Background())
	waitUntil(t, func() bool {
		s, _ := d.core.Snapshot(context.Background())
		return s.Environment.Checking
	})
	c.Close()
	close(release)

	waitUntil(t, func() bool {
		s, _ := d.core.Snapshot(context.Background())
		return s.Environment.Ready()
	})
	if !finished.Load() {
		t.Fatal("a acao foi cancelada junto com a conexao do cliente")
	}
}

func TestMetodoDesconhecido(t *testing.T) {
	d := startDaemon(t, okStatus)
	c := dial(t, d.path)

	err := c.call(context.Background(), "nao.existe", nil, nil)
	var rerr *Error
	if !errors.As(err, &rerr) || rerr.Code != CodeMethodNotFound {
		t.Fatalf("esperava CodeMethodNotFound, veio %v", err)
	}
}

func TestJSONInvalidoRecebeErroDeParse(t *testing.T) {
	d := startDaemon(t, okStatus)
	conn, err := net.Dial("unix", d.path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	conn.Write([]byte("{isso nao e json\n"))
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "-32700") {
		t.Fatalf("esperava erro de parse, veio %s", line)
	}
}

func TestListenRecusaSegundoDaemon(t *testing.T) {
	d := startDaemon(t, okStatus)
	if _, err := Listen(d.path, ""); err == nil || !strings.Contains(err.Error(), "ja existe um daemon") {
		t.Fatalf("esperava recusa, veio %v", err)
	}
}

func TestListenRemoveSocketOrfao(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cais.sock")
	// arquivo no lugar do socket, sem ninguem ouvindo: sobra de um daemon morto
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ln, err := Listen(path, "")
	if err != nil {
		t.Fatalf("deveria ter removido o socket orfao: %v", err)
	}
	ln.Close()
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condicao nao foi atingida a tempo")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestListenRecusaCaminhoLongo(t *testing.T) {
	path := filepath.Join(t.TempDir(), strings.Repeat("x", 120)+".sock")
	if _, err := Listen(path, ""); err == nil || !strings.Contains(err.Error(), "o maximo e 107") {
		t.Fatalf("esperava erro claro de caminho longo, veio %v", err)
	}
}

// daemon com caminhos temporarios e docker falso, pra testar as acoes que
// mexem na VPS pelo socket
func startSetupDaemon(t *testing.T) (*daemon, *atomic.Int32) {
	t.Helper()
	root := t.TempDir()
	var ups atomic.Int32
	d := startDaemonDeps(t, core.Deps{
		CheckDocker: okStatus,
		ConfigPath:  filepath.Join(root, "config.yaml"),
		SecretsDir:  filepath.Join(root, "secrets"),
		StackDir:    func(n string) string { return filepath.Join(root, "stacks", n) },
		ComposeUp: func(context.Context, string, bool) error {
			ups.Add(1)
			return nil
		},
		ComposeDown: func(context.Context, string) error { return nil },
		Login:       func(context.Context, string, string, []byte) error { return nil },
	})
	return d, &ups
}

func TestConfigESetupPeloSocket(t *testing.T) {
	d, ups := startSetupDaemon(t)
	c := dial(t, d.path)
	ctx := context.Background()

	info, err := c.LoadConfig(ctx)
	if err != nil || info.Exists {
		t.Fatalf("sem config: %+v, %v", info, err)
	}
	cfg := info.Config
	cfg.Domain = "exemplo.com.br"
	cfg.Email = "admin@exemplo.com.br"
	cfg.Stacks = []string{"postgres"}
	cfg.Woodpecker = config.Woodpecker{GitHubClient: "Ov23li", Admin: "fulano"}
	if err := c.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := c.SetSecret(ctx, "github_client_secret", "x"); err != nil {
		t.Fatal(err)
	}
	if info, _ = c.LoadConfig(ctx); !info.Exists || info.Config.Domain != "exemplo.com.br" {
		t.Fatalf("config nao gravou: %+v", info)
	}

	plan, err := c.PlanSetup(ctx)
	if err != nil || !plan.Ready() || len(plan.Stacks) != 4 {
		t.Fatalf("plano: %+v, %v", plan, err)
	}

	states, unsub, _ := c.Subscribe(ctx)
	defer unsub()
	setup, err := c.RunSetup(ctx)
	if err != nil || setup.Running || setup.Error != "" {
		t.Fatalf("RunSetup: %+v, %v", setup, err)
	}
	if ups.Load() != 4 {
		t.Errorf("%d compose up, esperava 4", ups.Load())
	}
	// o progresso chegou pela notificacao de estado
	st := recv(t, states, func(s core.State) bool { return !s.Setup.Running && !s.Setup.FinishedAt.IsZero() })
	if len(st.Setup.Steps) == 0 {
		t.Fatal("estado sem os passos do Setup")
	}

	pw, err := c.AddRegistryUser(ctx, "patrick")
	if err != nil || len(pw) != 26 {
		t.Fatalf("AddRegistryUser: %q, %v", pw, err)
	}
	users, _ := c.RegistryUsers(ctx)
	if strings.Join(users, ",") != "ci,vps,patrick" {
		t.Errorf("usuarios %v", users)
	}
	if ups.Load() != 5 {
		t.Error("registry nao foi recriado depois de criar usuario")
	}

	// ligar um servico pelo socket aplica na hora
	if _, err := c.SetService(ctx, "mysql", true); err != nil {
		t.Fatal(err)
	}
	if info, _ := c.LoadConfig(ctx); !info.Config.HasStack("mysql") {
		t.Error("mysql nao ligou pelo socket")
	}
	if err := c.RemoveRegistryUser(ctx, "ci"); err == nil {
		t.Error("removeu usuario de servico pelo socket")
	}
}

func TestConfigInvalidaVoltaComoErro(t *testing.T) {
	d, _ := startSetupDaemon(t)
	c := dial(t, d.path)
	cfg := config.Default()
	cfg.Domain = "sem-ponto"
	err := c.SaveConfig(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "domain") {
		t.Fatalf("esperava erro de dominio, veio %v", err)
	}
}

func TestParametroDesconhecidoERecusado(t *testing.T) {
	d, _ := startSetupDaemon(t)
	c := dial(t, d.path)
	err := c.call(context.Background(), MethodRegistryUserAdd, map[string]string{"usuario": "x"}, nil)
	var rerr *Error
	if !errors.As(err, &rerr) || rerr.Code != CodeInvalidParams {
		t.Fatalf("esperava CodeInvalidParams, veio %v", err)
	}
}

func TestContainerLogsPeloSocket(t *testing.T) {
	d := startDaemonDeps(t, core.Deps{
		ComposePS: func(context.Context, string) ([]docker.Container, error) {
			return []docker.Container{{Name: "garage"}}, nil
		},
		ContainerLogs: func(context.Context, string) (string, error) { return "stdout\nstderr\n", nil },
	})
	c := dial(t, d.path)
	out, err := c.ContainerLogs(context.Background(), "garage", "garage")
	if err != nil || out != "stdout\nstderr\n" {
		t.Fatalf("%q, %v", out, err)
	}
	if _, err := c.ContainerLogs(context.Background(), "garage", "mysql"); err == nil {
		t.Fatal("aceitou container de outra stack")
	}
}
