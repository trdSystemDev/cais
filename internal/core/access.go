package core

import (
	"context"
	"errors"

	"github.com/trdSystemDev/cais/internal/host"
)

// Os acessos por SSH (Setup > Acessos): quem entra na VPS e com qual chave.
// Fora do MCP: a IA nao da nem tira acesso.

// RemoveAccessRequest tira o acesso: uma chave de todo lugar
// (Fingerprint) ou um admin inteiro (Admin).
type RemoveAccessRequest struct {
	Fingerprint string `json:"fingerprint,omitempty"`
	Admin       string `json:"admin,omitempty"`
}

// HostAccess le quem entra pelo SSH.
func (c *Core) HostAccess(ctx context.Context) (host.Access, error) {
	return c.deps.ListAccess(ctx)
}

// AddAccess da acesso a uma chave: so a TUI ou como admin.
func (c *Core) AddAccess(ctx context.Context, req host.AccessRequest) (string, error) {
	return c.changeAccess(ctx, func(ctx context.Context) (string, error) { return c.deps.AddAccess(ctx, req) })
}

// RemoveAccess tira uma chave de todo lugar ou um admin.
func (c *Core) RemoveAccess(ctx context.Context, req RemoveAccessRequest) (string, error) {
	switch {
	case (req.Fingerprint == "") == (req.Admin == ""):
		return "", errors.New("diga a chave (fingerprint) ou o admin a tirar")
	case req.Admin != "":
		return c.changeAccess(ctx, func(ctx context.Context) (string, error) { return c.deps.RemoveAdmin(ctx, req.Admin) })
	}
	return c.changeAccess(ctx, func(ctx context.Context) (string, error) { return c.deps.RemoveKey(ctx, req.Fingerprint) })
}

func (c *Core) changeAccess(ctx context.Context, fn func(context.Context) (string, error)) (string, error) {
	if !c.infra.TryLock() {
		return "", ErrBusy
	}
	defer c.infra.Unlock()
	ctx = context.WithoutCancel(ctx)
	detail, err := fn(ctx)
	if err != nil {
		c.deps.Logger.Warn("acessos: nao mudou", "erro", err)
	} else {
		c.deps.Logger.Info("acessos", "mudou", detail)
	}
	// a chave do ssh cais@vps aparece na Seguranca
	c.CheckEnvironment(ctx)
	return detail, err
}
