package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/trdSystemDev/cais/internal/databases"
)

// ShellRequest pede um terminal interativo: o shell de um container de um
// app (App), ou o console de um banco da infra (Engine: psql ou mysql).
type ShellRequest struct {
	App string `json:"app,omitempty"`
	// Engine (postgres ou mysql) abre o console do banco como
	// administrador, no Database (vazio: o padrao).
	Engine   string `json:"engine,omitempty"`
	Database string `json:"database,omitempty"`
	// Service e o service do compose ou o nome do container; vazio serve
	// quando o app tem um container so rodando.
	Service string `json:"service,omitempty"`
	// Term e o TERM do terminal de quem abre; Rows e Cols, o tamanho.
	Term string `json:"term,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
	Cols uint16 `json:"cols,omitempty"`
}

// ShellOpener abre o shell de um container e devolve o terminal (o mestre
// de um pty). Fica fora da core.API porque o resultado e um arquivo aberto:
// pelo socket ele vai como descritor (SCM_RIGHTS), nao como JSON. Fica fora
// do MCP: e interativo.
type ShellOpener interface {
	OpenShell(ctx context.Context, req ShellRequest) (*os.File, error)
}

var _ ShellOpener = (*Core)(nil)

// OpenShell abre um shell (bash, ou sh) num container rodando do app, ou o
// console do banco (psql, mysql) com Engine.
func (c *Core) OpenShell(ctx context.Context, req ShellRequest) (*os.File, error) {
	if req.Engine != "" {
		return c.openConsole(ctx, req)
	}
	name, err := c.shellContainer(req.App, req.Service)
	if err != nil {
		return nil, err
	}
	if _, err := c.deps.Exec(ctx, name, "sh", "-c", "exit 0"); err != nil {
		return nil, fmt.Errorf("o container %s nao tem shell (sh): imagem minima, como distroless ou scratch? (%s)", name, firstLineErr(err))
	}
	f, err := c.deps.StartTerminal(name, cleanTerm(req.Term), req.Rows, req.Cols)
	if err != nil {
		return nil, err
	}
	c.deps.Logger.Info("shell aberto", "acao", "shell", "app", req.App, "container", name)
	return f, nil
}

// shellContainer escolhe o container do app: pelo service (ou nome), ou o
// unico rodando.
func (c *Core) shellContainer(app, service string) (string, error) {
	st, _ := c.Snapshot(context.Background())
	for _, a := range st.Apps.List {
		if a.Name != app {
			continue
		}
		var running []string
		var services []string
		for _, ct := range a.Containers {
			if ct.State != "running" {
				continue
			}
			if service != "" && (ct.Service == service || ct.Name == service) {
				return ct.Name, nil
			}
			running = append(running, ct.Name)
			services = append(services, ct.Service)
		}
		switch {
		case len(running) == 0:
			return "", fmt.Errorf("o app %s nao tem container rodando", app)
		case service != "":
			return "", fmt.Errorf("o app %s nao tem o service %s rodando (rodando: %s)", app, service, strings.Join(services, ", "))
		case len(running) > 1:
			return "", fmt.Errorf("o app %s tem %d containers rodando: escolha o service (%s)", app, len(running), strings.Join(services, ", "))
		}
		return running[0], nil
	}
	if app == "" {
		return "", errors.New("falta o nome do app")
	}
	return "", fmt.Errorf("app %s nao encontrado", app)
}

// cleanTerm deixa so um TERM valido (vai pro docker exec -e).
func cleanTerm(t string) string {
	for _, r := range t {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_.+", r)) {
			return ""
		}
	}
	return t
}

// openConsole abre o psql ou o mysql no container do banco, como
// administrador.
func (c *Core) openConsole(ctx context.Context, req ShellRequest) (*os.File, error) {
	eng, err := databases.Get(req.Engine)
	if err != nil {
		return nil, err
	}
	args, err := databases.ConsoleCommand(eng.Name, req.Database)
	if err != nil {
		return nil, err
	}
	if _, err := c.deps.Exec(ctx, eng.Container, "true"); err != nil {
		return nil, fmt.Errorf("o %s nao esta no ar: ligue ou inicie na aba Infra (%s)", eng.Name, firstLineErr(err))
	}
	f, err := c.deps.StartTerminal(eng.Container, cleanTerm(req.Term), req.Rows, req.Cols, args...)
	if err != nil {
		return nil, err
	}
	c.deps.Logger.Info("console do banco aberto", "acao", "console", "banco", eng.Name, "database", req.Database)
	return f, nil
}
