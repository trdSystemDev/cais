package main

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/core/coretest"
	"github.com/trdSystemDev/cais/internal/host"
)

// vpsFake e uma VPS Ubuntu com a chave do root e nada mais: o suficiente
// pro CLI (o host tem os testes dos passos).
type vpsFake struct{ calls []string }

func (v *vpsFake) Run(_ context.Context, name string, args ...string) (string, error) {
	v.calls = append(v.calls, strings.Join(append([]string{name}, args...), " "))
	return "", nil
}

func (v *vpsFake) RunInput(ctx context.Context, _, name string, args ...string) (string, error) {
	return v.Run(ctx, name, args...)
}

func (v *vpsFake) ReadFile(path string) ([]byte, error) {
	switch path {
	case "/etc/os-release":
		return []byte("ID=ubuntu\nPRETTY_NAME=\"Ubuntu 24.04 LTS\"\n"), nil
	case "/root/.ssh/authorized_keys":
		return []byte("ssh-ed25519 AAAA eu@note\n"), nil
	}
	return nil, fs.ErrNotExist
}

func (v *vpsFake) WriteFile(path string, _ []byte, _ fs.FileMode) error {
	v.calls = append(v.calls, "write "+path)
	return nil
}
func (v *vpsFake) Remove(string) error { return nil }
func (v *vpsFake) Exists(string) bool  { return false }
func (v *vpsFake) Geteuid() int        { return 0 }

func withVPS(t *testing.T) *vpsFake {
	v := &vpsFake{}
	old := hostSystem
	hostSystem = func() host.System { return v }
	t.Cleanup(func() { hostSystem = old })
	return v
}

func TestHostHardenPedeSim(t *testing.T) {
	v := withVPS(t)
	c := newTestCLI(nil)
	c.in = strings.NewReader("nao\n")
	if code := c.run([]string{"host", "harden", "-user", "deploy"}); code != 1 {
		t.Fatalf("saida %d", code)
	}
	if !strings.Contains(c.out.String(), "criar o admin deploy com sudo, docker e 1 chave(s)") || !strings.Contains(c.errb.String(), "cancelado") {
		t.Errorf("%s\n%s", c.out, c.errb)
	}
	if len(v.calls) > 3 { // so as leituras do Prepare
		t.Errorf("mudou algo sem o sim: %v", v.calls)
	}

	// sem -user: o erro diz como
	c = newTestCLI(nil)
	if code := c.run([]string{"host", "harden"}); code != 1 || !strings.Contains(c.errb.String(), "-user NOME") {
		t.Errorf("sem -user: %d %s", code, c.errb)
	}

	// senha curta pelo pipe: recusa antes de mudar algo
	v.calls = nil
	c = newTestCLI(nil)
	c.in = strings.NewReader("sim\ncurta")
	if code := c.run([]string{"host", "harden", "-user", "deploy"}); code != 1 || !strings.Contains(c.errb.String(), "senha curta") {
		t.Errorf("senha curta: %d %s", code, c.errb)
	}
	for _, call := range v.calls {
		if strings.HasPrefix(call, "env ") || strings.HasPrefix(call, "write ") {
			t.Errorf("mudou algo: %s", call)
		}
	}
}

func TestHostConfirmSoDoLoginNovo(t *testing.T) {
	withVPS(t)
	t.Setenv("SUDO_USER", "")
	c := newTestCLI(nil)
	if code := c.run([]string{"host", "confirm"}); code != 1 || !strings.Contains(c.errb.String(), "login novo") {
		t.Errorf("da sessao do root: %d %s", code, c.errb)
	}
	t.Setenv("SUDO_USER", "deploy")
	c = newTestCLI(nil)
	if code := c.run([]string{"host", "confirm"}); code != 0 || !strings.Contains(c.out.String(), "Nada pendente") {
		t.Errorf("sem volta pendente: %d %s %s", code, c.out, c.errb)
	}
}

func TestHostCheckSaiComUm(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("so no Linux")
	}
	withVPS(t)
	c := newTestCLI(nil)
	code := c.run([]string{"host"})
	if code != 1 || !strings.Contains(c.out.String(), "[ok] Sistema") || !strings.Contains(c.out.String(), "[!!] Login do root") ||
		!strings.Contains(c.errb.String(), "itens pra corrigir") {
		t.Errorf("%d\n%s\n%s", code, c.out, c.errb)
	}
}

// sem terminal (ssh sem -t, pipe) o shell nao abre e diz como
func TestAppShellPedeTerminal(t *testing.T) {
	c := newTestCLI(coretest.NewRecorder())
	if code := c.run([]string{"app", "shell", "loja"}); code != 1 || !strings.Contains(c.errb.String(), "ssh -t cais@vps app shell loja") {
		t.Errorf("%d %s", code, c.errb)
	}
}

// o cais host reboot: HH:MM agenda uma vez (o timer), auto grava o horario
// na config (sem o Setup ainda, so ele) e horario errado nem chega na VPS
func TestHostReboot(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("so no Linux")
	}
	v := withVPS(t)
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	old := hostConfigFile
	hostConfigFile = func() string { return cfg }
	t.Cleanup(func() { hostConfigFile = old })

	c := newTestCLI(nil)
	if code := c.run([]string{"host", "reboot", "1:00"}); code == 0 || slices.ContainsFunc(v.calls, func(s string) bool { return strings.HasPrefix(s, "systemd-run") }) {
		t.Fatalf("aceitou 1:00: %d %q", code, v.calls)
	}
	c = newTestCLI(nil)
	if code := c.run([]string{"host", "reboot", "01:00"}); code != 0 {
		t.Fatalf("saida %d: %s", code, c.errb.String())
	}
	if !slices.ContainsFunc(v.calls, func(s string) bool { return strings.HasPrefix(s, "systemd-run --unit="+host.RebootAtUnit) }) {
		t.Errorf("sem o timer: %q", v.calls)
	}
	c = newTestCLI(nil)
	if code := c.run([]string{"host", "reboot", "auto", "02:00"}); code != 0 {
		t.Fatalf("saida %d: %s", code, c.errb.String())
	}
	if got, err := config.Load(cfg); err != nil || got.RebootTime != "02:00" {
		t.Errorf("config: %q %v", got.RebootTime, err)
	}
	if !strings.Contains(c.out.String(), "Horario automatico: 02:00") {
		t.Errorf("saida: %s", c.out.String())
	}
}

// o cais host access le a chave do arquivo (ou da entrada), recusa a
// privada e lista o que ficou
func TestHostAccess(t *testing.T) {
	withVPS(t)
	dir := t.TempDir()
	priv := filepath.Join(dir, "id_ed25519")
	os.WriteFile(priv, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n"), 0o600)
	c := newTestCLI(nil)
	if code := c.run([]string{"host", "access", "add", priv}); code == 0 || !strings.Contains(c.errb.String(), "PRIVADA") {
		t.Fatalf("privada: %d %s", code, c.errb.String())
	}
	c = newTestCLI(nil)
	if code := c.run([]string{"host", "access", "remove"}); code != 2 {
		t.Errorf("remove sem nada: %d %s", code, c.errb.String())
	}
	c = newTestCLI(nil)
	if code := c.run([]string{"host", "access"}); code != 0 || !strings.Contains(c.out.String(), "IMPRESSAO DIGITAL") {
		t.Errorf("lista: %d %s %s", code, c.out.String(), c.errb.String())
	}
}
