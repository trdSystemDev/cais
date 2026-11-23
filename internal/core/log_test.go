package core

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/trdSystemDev/cais/internal/apps"
)

// syncBuffer e o destino do log nos testes (o deploy automatico e o aviso
// escrevem de outras goroutines).
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// captureLog troca o log do core por um em texto (chave=valor) num buffer.
func captureLog(c *Core) *syncBuffer {
	buf := &syncBuffer{}
	c.deps.Logger = slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return buf
}

// linha devolve a linha do log com a mensagem msg (vazia se nao houver).
func linha(log, msg string) string {
	for _, l := range strings.Split(log, "\n") {
		if strings.Contains(l, `msg="`+msg+`"`) {
			return l
		}
	}
	return ""
}

func temCampos(t *testing.T, l string, campos ...string) {
	t.Helper()
	for _, c := range campos {
		if !strings.Contains(l, c) {
			t.Errorf("linha sem %s:\n%s", c, l)
		}
	}
}

// O ciclo de um deploy aparece no log com os campos padrao, e o valor de
// um secret do app nunca.
func TestLogDoDeploy(t *testing.T) {
	c, _, gh := newAppsCore(t)
	buf := captureLog(c)
	ctx := context.Background()
	if _, err := c.CreateApp(ctx, apps.App{Name: "meu-app", Repo: "trdSystemDev/meu-app"}); err != nil {
		t.Fatal(err)
	}
	const segredo = "valor-super-secreto-do-jwt-123"
	if err := c.SetAppSecret(ctx, "meu-app", "jwt", segredo); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DeployApp(ctx, "meu-app", ""); err != nil {
		t.Fatal(err)
	}
	log := buf.String()
	temCampos(t, linha(log, "deploy comecou"), "level=INFO", "acao=deploy", "app=meu-app")
	commit := gh.Commit("trdSystemDev/meu-app")[:7]
	temCampos(t, linha(log, "deploy terminou"), "acao=deploy", "app=meu-app", "commit="+commit, "duracao=")

	if _, err := c.DeployApp(ctx, "meu-app", "nao-existe"); err == nil {
		t.Fatal("deploy de ref que nao existe")
	}
	temCampos(t, linha(buf.String(), "deploy falhou"), "level=ERROR", "acao=deploy", "app=meu-app", "erro=")

	if strings.Contains(buf.String(), segredo) {
		t.Fatalf("o valor do secret foi pro log:\n%s", buf)
	}
}

// Backup e restore aparecem no log; a chave privada do backup e as senhas
// dos bancos, nunca.
func TestLogDoBackupERestore(t *testing.T) {
	c, s, _, _ := newBackupCore(t)
	ctx := context.Background()
	priv := setupBackup(t, c, s)
	buf := captureLog(c)

	run, err := c.RunBackup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	log := buf.String()
	temCampos(t, linha(log, "backup comecou"), "acao=backup", "automatico=false", "destino=google")
	temCampos(t, linha(log, "backup terminou"), "acao=backup", "arquivo="+run.File, "databases=2", "duracao=")

	// so listar nao muda nada: nao vai pro log
	if _, err := c.RestoreBackup(ctx, RestoreRequest{File: run.File, Identity: priv}); err != nil {
		t.Fatal(err)
	}
	if linha(buf.String(), "restore comecou") != "" {
		t.Fatal("listar o backup foi registrado como restore")
	}
	if _, err := c.RestoreBackup(ctx, RestoreRequest{File: run.File, Identity: priv, Databases: []string{"postgres/loja"}}); err != nil {
		t.Fatal(err)
	}
	log = buf.String()
	temCampos(t, linha(log, "restore comecou: os databases pedidos serao substituidos"), "level=WARN", "acao=restore",
		"arquivo="+run.File, "databases=postgres/loja")
	temCampos(t, linha(log, "restore terminou"), "restaurados=postgres/loja")

	for _, segredo := range []string{priv, "senhaLoja123456789", "senhaBlog123456789"} {
		if strings.Contains(log, segredo) {
			t.Fatalf("segredo no log (%.12s...):\n%s", segredo, log)
		}
	}
}
