package ui

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/trdSystemDev/cais/internal/backup/drivetest"
	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/docker"
)

// a aba Setup configura o backup do zero pela TUI: client, login no Drive
// pelo codigo, chave (mostrada uma vez), horario e um backup agora; a Infra
// nao tem mais o card Backups
func TestBackupConfiguradoPelaTUI(t *testing.T) {
	s := drivetest.New()
	defer s.Close()
	root := t.TempDir()
	data := filepath.Join(root, "data")
	c := core.New(core.Deps{
		CheckDocker: okChecker, ConfigPath: filepath.Join(root, "etc", "config.yaml"),
		DataDir: data, SecretsDir: filepath.Join(data, "secrets"), BackupsDir: filepath.Join(data, "backups"),
		AppsDir: filepath.Join(data, "apps"), StackDir: func(n string) string { return filepath.Join(data, "stacks", n) },
		GoogleEndpoint: s.Endpoint(), DriveAPI: s.API(), DriveUpload: s.Upload(),
		Hostname:  func() (string, error) { return "vps-teste", nil },
		ComposePS: func(context.Context, string) ([]docker.Container, error) { return nil, nil },
		ExecStream: func(_ context.Context, _ string, _ io.Reader, w io.Writer, args ...string) error {
			_, err := io.WriteString(w, "-- dump "+args[len(args)-1])
			return err
		},
	})
	os.MkdirAll(filepath.Join(root, "etc"), 0o755)
	cfg := config.Default()
	cfg.Email = "a@exemplo.com.br"
	if err := c.SaveConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	m := open(t, c)
	click(t, m, "tab:Infra")
	waitFor(t, m, "nada sobe sem ligar")
	if strings.Contains(screen(m), "Backups") {
		t.Fatalf("a Infra ainda tem o card Backups:\n%s", screen(m))
	}

	click(t, m, "tab:Setup")
	// Backups e uma sub-aba propria, fora do formulario da config
	if strings.Contains(screen(m), "OAuth client do Google") {
		t.Fatalf("Backups na sub-aba Geral:\n%s", screen(m))
	}
	click(t, m, "setup-sec:Backups")
	waitFor(t, m, "1 · OAuth client do Google")
	// o Tab entra no painel de Backups
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if !m.bk.active || m.bk.focusID(m.state.Backup) != "bk-client-id" || m.setup.typing() {
		t.Fatalf("tab na sub-aba Backups: bk ativo %v, foco %q", m.bk.active, m.bk.focusID(m.state.Backup))
	}

	// 1. client
	m.bk.clientID.SetValue("123-abc.apps.googleusercontent.com")
	m.bk.secret.SetValue("segredo")
	click(t, m, "bk-client-save")
	waitFor(t, m, "OAuth client gravado")
	if strings.Contains(screen(m), "segredo") {
		t.Fatal("client secret na tela")
	}

	// 2. login pelo codigo
	click(t, m, "bk-connect")
	waitFor(t, m, "ABCD-EFGH")
	waitFor(t, m, "https://www.google.com/device")
	s.Approve()
	waitFor(t, m, "pessoa@exemplo.com")

	// 3. chave: aparece uma vez
	click(t, m, "bk-key")
	waitFor(t, m, "AGE-SECRET-KEY-")
	waitFor(t, m, "Ela NAO fica")
	click(t, m, "bk-key-done")
	waitFor(t, m, "nao aparece mais")
	if strings.Contains(screen(m), "AGE-SECRET-KEY-") {
		t.Fatal("a chave privada continua na tela")
	}
	// gerar outra pede confirmacao
	click(t, m, "bk-key")
	waitFor(t, m, "Confirmar: gerar outra")

	// 4. horario
	m.bk.at.SetValue("03:00")
	m.bk.days.SetValue("14")
	click(t, m, "bk-schedule")
	waitFor(t, m, "todo dia as 03:00, guarda 14 dias")

	// backup agora
	click(t, m, "bk-run")
	waitFor(t, m, "no Drive (")
	waitFor(t, m, "Ultimo: ")
	if len(s.Names()) != 1 || !strings.HasPrefix(s.Names()[0], "cais-vps-teste-") {
		t.Errorf("no Drive: %v", s.Names())
	}
}
