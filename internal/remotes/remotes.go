// Package remotes e a lista de VPS com o cais, no computador de quem
// administra: nome e destino do SSH (cais@vps). Serve pro cais -r NOME (a
// TUI ou um comando de uma VPS) e pro cais mcp --remotes (um MCP so pra
// todas). O acesso continua sendo o SSH de cada uma.
package remotes

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/trdSystemDev/cais/internal/fsx"
)

// Remote e uma VPS: o nome curto e o destino do SSH.
type Remote struct {
	Name   string `yaml:"name" json:"name"`
	Target string `yaml:"target" json:"target"`
}

type file struct {
	Remotes []Remote `yaml:"remotes"`
}

var (
	validName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	// usuario@host, host ou um Host do ~/.ssh/config; nunca comecando com
	// "-" (viraria opcao do ssh, ex: -oProxyCommand=...)
	validTarget = regexp.MustCompile(`^[A-Za-z0-9_.][A-Za-z0-9_.@:%\[\]-]{0,252}$`)
)

// Validate confere o nome e o destino.
func (r Remote) Validate() error {
	if !validName.MatchString(r.Name) {
		return fmt.Errorf("nome invalido %q: minusculas, numeros e -, ate 32 (ex: prod)", r.Name)
	}
	if !validTarget.MatchString(r.Target) {
		return fmt.Errorf("destino invalido %q: o do ssh, ex: cais@vps.exemplo.com.br ou cais@srv.tailnet.ts.net", r.Target)
	}
	return nil
}

// DefaultPath e o arquivo da lista: CAIS_REMOTES, ou remotes.yaml na pasta
// de config do usuario (~/.config/cais no Linux).
func DefaultPath() (string, error) {
	if p := os.Getenv("CAIS_REMOTES"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "cais", "remotes.yaml"), nil
}

// Load le a lista; sem o arquivo, ela esta vazia.
func Load(path string) ([]Remote, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f file
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, r := range f.Remotes {
		if err := r.Validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	return f.Remotes, nil
}

// Save grava a lista (so o dono le: os destinos dizem onde estao as VPS).
func Save(path string, list []Remote) error {
	b, err := yaml.Marshal(file{Remotes: list})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return fsx.WriteAtomic(path, append([]byte("# VPS com o cais (cais remote add/remove)\n"), b...), 0o600)
}

// Add poe a VPS na lista; o mesmo nome troca o destino. Diz se ja existia.
func Add(list []Remote, r Remote) ([]Remote, bool, error) {
	if err := r.Validate(); err != nil {
		return list, false, err
	}
	if i := slices.IndexFunc(list, func(x Remote) bool { return x.Name == r.Name }); i >= 0 {
		list = slices.Clone(list)
		list[i] = r
		return list, true, nil
	}
	return append(slices.Clone(list), r), false, nil
}

// Remove tira a VPS da lista; diz se ela estava la.
func Remove(list []Remote, name string) ([]Remote, bool) {
	out := slices.DeleteFunc(slices.Clone(list), func(x Remote) bool { return x.Name == name })
	return out, len(out) != len(list)
}

// Find acha a VPS pelo nome.
func Find(list []Remote, name string) (Remote, bool) {
	i := slices.IndexFunc(list, func(x Remote) bool { return x.Name == name })
	if i < 0 {
		return Remote{}, false
	}
	return list[i], true
}

// Names sao os nomes, na ordem da lista.
func Names(list []Remote) []string {
	var out []string
	for _, r := range list {
		out = append(out, r.Name)
	}
	return out
}

// SSHArgs sao os argumentos do ssh pra rodar command na VPS. batch: sem
// perguntar senha nem confirmar a chave do host (o MCP nao tem terminal);
// sem batch, com terminal (-t) pra TUI. O "--" separa as opcoes do destino.
func SSHArgs(target string, batch bool, command ...string) []string {
	args := []string{"-o", "ConnectTimeout=15", "-o", "ServerAliveInterval=30"}
	if batch {
		args = append(args, "-o", "BatchMode=yes", "-T")
	} else {
		args = append(args, "-t")
	}
	args = append(args, "--", target)
	return append(args, command...)
}

// Describe e a VPS numa linha (cais remote list).
func (r Remote) Describe() string {
	return fmt.Sprintf("%-16s %s", r.Name, r.Target)
}

// List e a lista numa string, uma por linha.
func List(list []Remote) string {
	var b strings.Builder
	for _, r := range list {
		b.WriteString(r.Describe() + "\n")
	}
	return b.String()
}
