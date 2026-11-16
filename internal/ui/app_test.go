package ui

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/docker"
	"github.com/trdSystemDev/cais/internal/rpc"
)

func okChecker(context.Context) docker.Status {
	return docker.Status{ClientVersion: "29.0.1", ServerVersion: "29.0.1", ComposeVersion: "2.40.0"}
}

func missingChecker(context.Context) docker.Status {
	return docker.Status{
		ClientErr:  docker.ErrNotInstalled,
		ServerErr:  docker.ErrNotInstalled,
		ComposeErr: docker.ErrNotInstalled,
	}
}

// testCore e um core com caminhos temporarios e docker falso: o Setup roda
// de verdade (secrets, compose no disco), so os comandos do docker nao.
// Devolve quantos compose up e compose ps foram pedidos.
func testCore(t *testing.T, check func(context.Context) docker.Status) (*core.Core, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	root := t.TempDir()
	var ups, pss atomic.Int32
	c := core.New(core.Deps{
		CheckDocker: check,
		ConfigPath:  filepath.Join(root, "config.yaml"),
		SecretsDir:  filepath.Join(root, "secrets"),
		StackDir:    func(n string) string { return filepath.Join(root, "stacks", n) },
		ComposeUp: func(context.Context, string, bool) error {
			ups.Add(1)
			return nil
		},
		ContainerLogs: func(context.Context, string) (string, error) { return "container iniciado\nouvindo conexoes", nil },
		ComposeDown:   func(context.Context, string) error { return nil },
		// toda stack instalada aparece com um container saudavel
		ComposePS: func(_ context.Context, file string) ([]docker.Container, error) {
			pss.Add(1)
			name := filepath.Base(filepath.Dir(file))
			return []docker.Container{{Name: name, Service: name, State: "running", Health: "healthy", Status: "Up 1 minute (healthy)"}}, nil
		},
		ExecInput: func(context.Context, string, []byte, ...string) (string, error) { return "", nil },
		Exec:      fakeGarage,
		Hostname:  func() (string, error) { return "vps-teste", nil },
		// importacao de banco: o dump escreve um SQL curto, o restore engole
		ExecStream: func(_ context.Context, _ string, stdin io.Reader, stdout io.Writer, args ...string) error {
			if strings.Contains(strings.Join(args, " "), "_dump") {
				_, err := io.WriteString(stdout, "CREATE TABLE t (id int);\n")
				return err
			}
			_, err := io.Copy(io.Discard, stdin)
			return err
		},
	})
	return c, &ups, &pss
}

// fakeGarage responde o CLI do Garage como o container de verdade (layout
// ja aplicado, chave nova, bucket vazio).
func fakeGarage(_ context.Context, _ string, args ...string) (string, error) {
	cmd := strings.Join(args, " ")
	switch {
	case strings.Contains(cmd, "layout show"):
		return "Current cluster layout version: 1", nil
	case strings.Contains(cmd, "key create"):
		return "Key ID:              GK5d0e\nSecret key:          6c9b79\n", nil
	case strings.Contains(cmd, "bucket info"):
		return "Size:            0 B (0 B)\n", nil
	}
	return "", nil
}

// open monta a TUI sobre uma core.API, como o Run faz, numa tela de 130x34.
// O teste faz o papel do Bubble Tea: entrega as fotos do core e executa os
// comandos.
func open(t *testing.T, api core.API) *model {
	t.Helper()
	states, unsub, err := api.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unsub)
	m := newModel(api, states)
	t.Cleanup(m.zones.Close)
	m.Update(tea.WindowSizeMsg{Width: 130, Height: 34})
	// o Subscribe entrega a foto atual na hora; o programa real a recebe
	// logo no Init
	m.Update(stateMsg(<-states))
	run(t, m, m.checkCmd())
	run(t, m, m.setup.load())
	return m
}

// run executa um comando e entrega a mensagem ao modelo, seguindo os
// comandos que ela gerar (menos a espera de estado, que o teste conduz).
func run(t *testing.T, m *model, cmd tea.Cmd) tea.Msg {
	t.Helper()
	var last tea.Msg
	for cmd != nil {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				last = run(t, m, c)
			}
			return last
		}
		if msg == nil {
			return last
		}
		last = msg
		_, cmd = m.Update(msg)
		if _, ok := msg.(infraLogsMsg); ok {
			return last
		}
		if _, ok := msg.(appLogsMsg); ok {
			return last
		}
		if _, ok := msg.(stateMsg); ok {
			return last
		}
	}
	return last
}

func screen(m *model) string { return ansi.Strip(m.View().Content) }

// waitFor entrega as fotos do core ate o texto aparecer na tela.
func waitFor(t *testing.T, m *model, text string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for !strings.Contains(screen(m), text) {
		select {
		case s, ok := <-m.states:
			if !ok {
				m.Update(closedMsg{})
				if !strings.Contains(screen(m), text) {
					t.Fatalf("conexao fechou e %q nao apareceu:\n%s", text, screen(m))
				}
				return
			}
			// como o programa: a foto e o que ela dispara (menos a espera
			// da proxima, que este laco faz)
			run(t, m, m.onState(core.State(s)))
		case <-deadline:
			t.Fatalf("texto %q nao apareceu na tela:\n%s", text, screen(m))
		}
	}
}

// click clica no meio da zona, com as coordenadas reais da tela. O
// bubblezone registra as zonas numa goroutine depois do View.
func click(t *testing.T, m *model, id string) tea.Msg {
	t.Helper()
	m.View()
	// as zonas desta tela sao registradas numa goroutine: sem esperar, o
	// clique pode usar a posicao da tela anterior (layout mudou) e acertar
	// o vizinho
	time.Sleep(15 * time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if z := m.zones.Get(id); z != nil && !z.IsZero() {
			_, cmd := m.Update(tea.MouseReleaseMsg{X: (z.StartX + z.EndX) / 2, Y: z.StartY, Button: tea.MouseLeft})
			return run(t, m, cmd)
		}
		if time.Now().After(deadline) {
			t.Fatalf("zona %q nao esta na tela:\n%s", id, screen(m))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func press(t *testing.T, m *model, text string) tea.Msg {
	t.Helper()
	_, cmd := m.Update(tea.KeyPressMsg{Code: rune(text[0]), Text: text})
	return run(t, m, cmd)
}

func TestDashboardMostraDockerOK(t *testing.T) {
	c, _, _ := testCore(t, okChecker)
	m := open(t, c)
	waitFor(t, m, "Pronto.")
	waitFor(t, m, "29.0.1")
	waitFor(t, m, "2.40.0")
}

func TestDashboardMostraDockerAusente(t *testing.T) {
	c, _, _ := testCore(t, missingChecker)
	m := open(t, c)
	waitFor(t, m, "docker nao encontrado")
	waitFor(t, m, "Daemon   depende do CLI")
	waitFor(t, m, "Resolva os itens em vermelho")
}

// a tela tem que refletir uma acao disparada fora dela -- e o caso do MCP
// publicando um app enquanto alguem esta com a TUI aberta
func TestUIReageAAcaoExternaNoCore(t *testing.T) {
	var installed atomic.Bool
	c, _, _ := testCore(t, func(ctx context.Context) docker.Status {
		if installed.Load() {
			return okChecker(ctx)
		}
		return missingChecker(ctx)
	})
	m := open(t, c)
	waitFor(t, m, "docker nao encontrado")

	installed.Store(true)
	c.CheckEnvironment(context.Background()) // ninguem clicou em nada
	waitFor(t, m, "Pronto.")
}

func TestBotaoVerificarDisparaAcao(t *testing.T) {
	var calls atomic.Int32
	c, _, _ := testCore(t, func(ctx context.Context) docker.Status {
		calls.Add(1)
		return okChecker(ctx)
	})
	m := open(t, c)
	waitFor(t, m, "Pronto.")
	click(t, m, "check")
	if n := calls.Load(); n < 2 {
		t.Fatalf("esperava a checagem inicial + a do clique, houve %d", n)
	}
}

// ponta a ponta: daemon real no socket, TUI como um cliente e um segundo
// cliente fazendo o papel do MCP
func TestTUIViaDaemonReageAOutroCliente(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cais.sock")
	ln, err := rpc.Listen(path, "")
	if err != nil {
		t.Fatal(err)
	}
	var installed atomic.Bool
	daemonCore, _, _ := testCore(t, func(ctx context.Context) docker.Status {
		if installed.Load() {
			return okChecker(ctx)
		}
		return missingChecker(ctx)
	})
	ctx, stopDaemon := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- rpc.Serve(ctx, ln, daemonCore) }()
	defer func() { stopDaemon(); <-served }()

	tuiClient, err := rpc.Dial(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer tuiClient.Close()
	m := open(t, tuiClient)
	waitFor(t, m, "docker nao encontrado")

	mcp, err := rpc.Dial(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer mcp.Close()
	installed.Store(true)
	if _, err := mcp.CheckEnvironment(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, m, "Pronto.")

	// daemon cai com a TUI aberta: ela tem que avisar em vez de mostrar
	// dados velhos como se estivessem certos
	stopDaemon()
	waitFor(t, m, "A conexao com o daemon do cais caiu.")
	waitFor(t, m, "Veja: systemctl status cais")
	if _, ok := click(t, m, "quit").(tea.QuitMsg); !ok {
		t.Fatal("Sair do aviso nao saiu")
	}
}

func TestCliqueTrocaDeAba(t *testing.T) {
	c, _, _ := testCore(t, okChecker)
	m := open(t, c)
	waitFor(t, m, "Pronto.")

	click(t, m, "tab:Apps")
	waitFor(t, m, "+ Novo app")

	click(t, m, "tab:Setup")
	waitFor(t, m, "Configuracao")
}

func TestAtalhoNumericoTrocaDeAba(t *testing.T) {
	c, _, _ := testCore(t, okChecker)
	m := open(t, c)
	waitFor(t, m, "Pronto.")

	press(t, m, "3")
	waitFor(t, m, "nada sobe sem ligar")
	if m.tab != tabInfra {
		t.Fatalf("aba %d", m.tab)
	}
}

// num campo de texto, numero e digitacao, nao troca de aba
func TestNumeroNoCampoNaoTrocaDeAba(t *testing.T) {
	c, _, _ := testCore(t, okChecker)
	m := open(t, c)
	press(t, m, "4")
	click(t, m, "email")
	press(t, m, "4")
	if m.tab != tabSetup {
		t.Fatalf("trocou pra aba %d digitando no campo", m.tab)
	}
	if v := m.setup.get("email").input.Value(); !strings.HasSuffix(v, "4") {
		t.Fatalf("campo = %q, o 4 tinha que entrar nele", v)
	}
}

func TestSairPeloBotaoEPeloAtalho(t *testing.T) {
	c, _, _ := testCore(t, okChecker)
	m := open(t, c)
	if _, ok := click(t, m, "quit").(tea.QuitMsg); !ok {
		t.Fatal("clique em Sair nao saiu")
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl})
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("ctrl+q nao saiu")
	}
}

// configura a VPS e roda o Setup pelo core, com os servicos pedidos
func setupDone(t *testing.T, c *core.Core, services ...string) {
	t.Helper()
	ctx := context.Background()
	info, _ := c.LoadConfig(ctx)
	cfg := info.Config
	cfg.TestDomain, cfg.Email, cfg.TLS = "trd.localhost", "a@exemplo.com.br", "selfsigned"
	cfg.Stacks, cfg.Hosts.S3 = services, "s3.trd.localhost"
	if err := c.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RunSetup(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestInfraMostraAsStacks(t *testing.T) {
	c, _, _ := testCore(t, okChecker)
	setupDone(t, c, "proxy", "postgres")
	// mysql ligado na config depois, sem aplicar
	info, _ := c.LoadConfig(context.Background())
	info.Config.Stacks = append(info.Config.Stacks, "mysql")
	c.SaveConfig(context.Background(), info.Config)

	m := open(t, c)
	click(t, m, "tab:Infra")
	waitFor(t, m, "Ultima verificacao")
	out := screen(m)
	for _, want := range []string{
		"HTTPS dos apps", "✓ proxy", "Logs · proxy",
		"✓ postgres", "ainda nao subiu", // mysql
		"· garage", "Ligar", "Desligar",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("tela sem %q:\n%s", want, out)
		}
	}
	// todos ligam e desligam, o proxy tambem; o mysql, ligado na config sem
	// subir, tem o Aplicar
	for _, name := range []string{"proxy", "postgres", "mysql", "garage"} {
		if z := m.zones.Get("toggle:" + name); z == nil || z.IsZero() {
			t.Errorf("%s sem botao de ligar/desligar", name)
		}
	}
	if z := m.zones.Get("apply:mysql"); z == nil || z.IsZero() {
		t.Errorf("mysql sem o Aplicar:\n%s", out)
	}
	if z := m.zones.Get("apply:proxy"); z != nil && !z.IsZero() {
		t.Error("proxy em dia com o Aplicar")
	}
}

func TestInfraLigaEDesligaServico(t *testing.T) {
	c, ups, _ := testCore(t, okChecker)
	setupDone(t, c)
	m := open(t, c)
	click(t, m, "tab:Infra")
	waitFor(t, m, "Ultima verificacao")

	before := ups.Load()
	click(t, m, "toggle:postgres")
	waitFor(t, m, "postgres ligado.")
	if info, _ := c.LoadConfig(context.Background()); !info.Config.HasStack("postgres") {
		t.Fatal("postgres nao ligou na config")
	}
	if ups.Load() <= before {
		t.Fatal("ligar nao subiu nada")
	}
	waitFor(t, m, "✓ postgres")

	// pelo teclado: postgres e o primeiro servico, ja selecionado
	press(t, m, " ")
	waitFor(t, m, "postgres desligado")
	if info, _ := c.LoadConfig(context.Background()); info.Config.HasStack("postgres") {
		t.Fatal("postgres continua ligado")
	}
}

func TestInfraSemConfigAvisa(t *testing.T) {
	c, _, _ := testCore(t, okChecker)
	m := open(t, c)
	click(t, m, "tab:Infra")
	waitFor(t, m, "Ultima verificacao")
	click(t, m, "toggle:mysql")
	waitFor(t, m, "preencha a aba Setup primeiro")
}

func TestInfraAtualizaAoAbrirEPeloBotao(t *testing.T) {
	c, _, pss := testCore(t, okChecker)
	setupDone(t, c, "proxy", "postgres")
	m := open(t, c)

	click(t, m, "tab:Infra")
	waitFor(t, m, "Ultima verificacao")
	opened := pss.Load()
	if opened == 0 {
		t.Fatal("abrir a aba nao perguntou ao docker")
	}
	click(t, m, "infra-refresh")
	if pss.Load() <= opened {
		t.Fatal("Atualizar nao perguntou ao docker de novo")
	}
	press(t, m, "r")
	if pss.Load() <= opened+2 {
		t.Fatal("tecla r nao atualizou")
	}
}

// o proxy liga pela Infra; mudar a config dele no Setup mostra o Aplicar,
// que sobe de novo so com a config nova
func TestInfraLigaOProxyEAplicaAConfig(t *testing.T) {
	c, ups, _ := testCore(t, okChecker)
	m := open(t, c)
	click(t, m, "tab:Setup")
	// Let's Encrypt: o e-mail vai no compose do proxy
	m.setup.get("email").input.SetValue("admin@exemplo.com.br")
	click(t, m, "save")
	waitFor(t, m, "Config salva.")
	if ups.Load() != 0 {
		t.Fatal("salvar subiu stack")
	}

	click(t, m, "tab:Infra")
	waitFor(t, m, "· proxy")
	click(t, m, "toggle:proxy")
	waitFor(t, m, "proxy ligado.")
	waitFor(t, m, "✓ proxy")
	if ups.Load() != 1 {
		t.Fatalf("%d compose up, esperava 1 (proxy)", ups.Load())
	}

	// e-mail novo muda o compose do proxy: Aplicar
	click(t, m, "tab:Setup")
	m.setup.get("email").input.SetValue("outro@exemplo.com.br")
	click(t, m, "save")
	waitFor(t, m, "Aplicar na aba Infra")
	click(t, m, "tab:Infra")
	waitFor(t, m, "Aplicar")
	click(t, m, "apply:proxy")
	waitFor(t, m, "config aplicada no proxy.")
	if ups.Load() != 2 {
		t.Fatalf("%d compose up, esperava 2", ups.Load())
	}
	if info, _ := c.LoadConfig(context.Background()); !info.Config.HasStack("proxy") {
		t.Fatal("aplicar desligou o proxy")
	}
}

// Novo banco: database e usuario com senha nova, e a lista com secrets e espaco
func TestInfraNovoBanco(t *testing.T) {
	c, _, _ := testCore(t, okChecker)
	setupDone(t, c, "postgres")
	m := open(t, c)
	click(t, m, "tab:Infra")
	waitFor(t, m, "Novo banco")
	click(t, m, "newdb:postgres")
	waitFor(t, m, "Novo banco no postgres")
	typeIn(t, m, "db-name", "loja")
	// numero no campo nao troca de aba
	typeIn(t, m, "db-exts", "postgis")
	click(t, m, "db-create")
	waitFor(t, m, "Database loja criado")
	waitFor(t, m, "postgres_loja_user, postgres_loja_pass (sem app)")
	list, err := c.Databases(context.Background())
	if err != nil || len(list) != 1 || list[0].Name != "loja" {
		t.Fatalf("databases %+v %v", list, err)
	}

	// repetido: o motivo aparece
	typeIn(t, m, "db-name", "loja")
	click(t, m, "db-create")
	waitFor(t, m, "ja existe")
	click(t, m, "db-close")
	waitFor(t, m, "Logs · ")
}

// Novo bucket no Garage: bucket e chave, e a lista com os secrets do app
func TestInfraNovoBucket(t *testing.T) {
	c, _, _ := testCore(t, okChecker)
	setupDone(t, c, "proxy", "garage")
	m := open(t, c)
	click(t, m, "tab:Infra")
	waitFor(t, m, "Novo bucket")
	click(t, m, "newdb:garage")
	waitFor(t, m, "Novo bucket no garage")
	typeIn(t, m, "db-name", "portal-fotos")
	click(t, m, "db-create")
	waitFor(t, m, "Bucket portal-fotos criado")
	waitFor(t, m, "s3_portal_fotos_access_key, s3_portal_fotos_secret_key (sem app)")
	if list, err := c.Buckets(context.Background()); err != nil || len(list) != 1 {
		t.Fatalf("buckets %+v %v", list, err)
	}
}

// excluir da lista: pede o nome digitado, recusa ligado a app, depois apaga
func TestInfraExcluirBancoEBucket(t *testing.T) {
	c, _, _ := testCore(t, okChecker)
	setupDone(t, c, "proxy", "postgres", "garage")
	ctx := context.Background()
	c.CreateDatabase(ctx, core.DatabaseRequest{Engine: "postgres", Name: "loja"})
	c.CreateBucket(ctx, "loja-fotos")
	m := open(t, c)
	click(t, m, "tab:Infra")
	waitFor(t, m, "Novo bucket")

	click(t, m, "newdb:postgres")
	waitFor(t, m, "Ja criados")
	click(t, m, "db-del:loja")
	waitFor(t, m, "apaga todos os dados dele")
	typeIn(t, m, "db-del-confirm", "outro")
	click(t, m, "db-del-ok")
	waitFor(t, m, "Digite loja pra confirmar.")
	typeIn(t, m, "db-del-confirm", "loja")
	click(t, m, "db-del-ok")
	waitFor(t, m, "loja excluido.")
	if list, _ := c.Databases(ctx); len(list) != 0 {
		t.Fatalf("banco continua: %+v", list)
	}
	click(t, m, "db-close")

	click(t, m, "newdb:garage")
	waitFor(t, m, "loja-fotos")
	click(t, m, "db-del:loja-fotos")
	waitFor(t, m, "Excluir o bucket loja-fotos apaga")
	click(t, m, "db-del-cancel")
	if strings.Contains(screen(m), "Nao tem volta") {
		t.Error("confirmacao continua aberta depois de cancelar")
	}
	click(t, m, "db-del:loja-fotos")
	typeIn(t, m, "db-del-confirm", "loja-fotos")
	click(t, m, "db-del-ok")
	waitFor(t, m, "loja-fotos excluido.")
	if list, _ := c.Buckets(ctx); len(list) != 0 {
		t.Fatalf("bucket continua: %+v", list)
	}
}

// Novo banco importado: o campo esconde a senha, a previa mostra a origem
// sem ela, e o resultado diz de onde veio
func TestInfraImportarBanco(t *testing.T) {
	c, _, _ := testCore(t, okChecker)
	setupDone(t, c, "postgres")
	m := open(t, c)
	click(t, m, "tab:Infra")
	waitFor(t, m, "Novo banco")
	click(t, m, "newdb:postgres")
	waitFor(t, m, "vazio cria o banco vazio")
	typeIn(t, m, "db-name", "loja")
	typeIn(t, m, "db-src", "mysql://antigo:S3gredo@db.antigo.com/loja")
	waitFor(t, m, `de "mysql", mas o banco`)
	m.db.src.SetValue("")
	typeIn(t, m, "db-src", "postgres://antigo:S3gredo@db.antigo.com/loja")
	waitFor(t, m, "copia de antigo@db.antigo.com:5432/loja")
	waitFor(t, m, "Criar e importar")
	if strings.Contains(screen(m), "S3gredo") {
		t.Fatal("a senha da origem aparece na tela")
	}
	click(t, m, "db-create")
	waitFor(t, m, "criado com o conteudo de antigo@db.antigo.com:5432/loja")
	if v := m.db.src.Value(); v != "" {
		t.Errorf("a string de conexao ficou no campo: %q", v)
	}
}
