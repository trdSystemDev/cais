// Package host confere e prepara a seguranca da VPS onde o cais roda:
// usuario admin so com chave, SSH sem root e sem senha, firewall, fail2ban,
// atualizacoes automaticas e portas expostas. Check so le; Harden aplica (como
// root, pelo cais host harden). Tudo que toca a maquina passa pelo System,
// trocado nos testes.
package host

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// System e a maquina: comandos e arquivos.
type System interface {
	// Run roda o comando e devolve o stdout; no erro, a mensagem e o stderr.
	Run(ctx context.Context, name string, args ...string) (string, error)
	// RunInput e o Run com stdin (senha nunca vai em argumento).
	RunInput(ctx context.Context, input, name string, args ...string) (string, error)
	ReadFile(path string) ([]byte, error)
	// WriteFile grava inteiro (troca atomica) com a permissao perm.
	WriteFile(path string, data []byte, perm fs.FileMode) error
	Remove(path string) error
	Exists(path string) bool
	Geteuid() int
}

// Local e a maquina de verdade.
func Local() System { return local{} }

type local struct{}

// CmdError e a falha de um comando: a mensagem e o stderr.
type CmdError struct {
	Cmd    string
	Stderr string
	Err    error
}

func (e *CmdError) Error() string {
	if e.Stderr != "" {
		return e.Cmd + ": " + e.Stderr
	}
	return e.Cmd + ": " + e.Err.Error()
}

func (e *CmdError) Unwrap() error { return e.Err }

func (local) Run(ctx context.Context, name string, args ...string) (string, error) {
	return local{}.RunInput(ctx, "", name, args...)
}

func (local) RunInput(ctx context.Context, input, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	if err := cmd.Run(); err != nil {
		return strings.TrimSpace(stdout.String()), &CmdError{
			Cmd: strings.Join(append([]string{name}, args...), " "), Stderr: strings.TrimSpace(stderr.String()), Err: err}
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (local) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (local) WriteFile(path string, data []byte, perm fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (local) Remove(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (local) Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (local) Geteuid() int { return os.Geteuid() }

// osRelease le o ID, a versao e o nome do sistema (/etc/os-release).
type osRelease struct{ ID, Codename, Pretty string }

const osReleasePath = "/etc/os-release"

func readOSRelease(sys System) (osRelease, error) {
	b, err := sys.ReadFile(osReleasePath)
	if err != nil {
		return osRelease{}, err
	}
	var r osRelease
	for _, l := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"'`)
		switch k {
		case "ID":
			r.ID = v
		case "VERSION_CODENAME":
			r.Codename = v
		case "PRETTY_NAME":
			r.Pretty = v
		}
	}
	return r, nil
}

// Supported diz se o harden sabe preparar esse sistema.
func (r osRelease) Supported() bool { return r.ID == "ubuntu" || r.ID == "debian" }

func (r osRelease) String() string {
	if r.Pretty != "" {
		return r.Pretty
	}
	return r.ID
}

// sshdConfig e a config efetiva do sshd (sshd -T), em minusculas.
func sshdConfig(ctx context.Context, sys System, conn string) (map[string]string, error) {
	args := []string{"-T"}
	if conn != "" {
		args = append(args, "-C", conn)
	}
	out, err := sys.Run(ctx, "sshd", args...)
	if err != nil {
		return nil, err
	}
	cfg := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(l), " ")
		if k != "" {
			if _, seen := cfg[k]; !seen {
				cfg[k] = strings.ToLower(strings.TrimSpace(v))
			}
		}
	}
	return cfg, nil
}

func firstLine(err error) string {
	s := strings.TrimSpace(err.Error())
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}
