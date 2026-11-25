package core

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestUpdateCais(t *testing.T) {
	var started []string
	c := New(Deps{
		Version:       "v0.5.3",
		LatestRelease: func(context.Context) (string, error) { return "v0.6.0", nil },
		SelfUpdate: func(_ context.Context, v string) error {
			started = append(started, v)
			return nil
		},
	})
	ctx := context.Background()
	if r, err := c.UpdateCais(ctx, ""); err != nil || !r.Started || r.From != "v0.5.3" || r.To != "v0.6.0" {
		t.Fatalf("mais novo: %+v %v", r, err)
	}
	// uma tag especifica, inclusive mais velha (voltar de versao)
	if r, err := c.UpdateCais(ctx, "v0.4.0"); err != nil || !r.Started || r.To != "v0.4.0" {
		t.Fatalf("voltar: %+v %v", r, err)
	}
	if r, err := c.UpdateCais(ctx, "v0.5.3"); err != nil || r.Started {
		t.Fatalf("mesma versao: %+v %v", r, err)
	}
	if _, err := c.UpdateCais(ctx, "latest; rm -rf /"); err == nil || !strings.Contains(err.Error(), "versao invalida") {
		t.Fatalf("tag invalida: %v", err)
	}
	if strings.Join(started, " ") != "v0.6.0 v0.4.0" {
		t.Fatalf("atualizacoes: %v", started)
	}

	// ja na mais nova: nada comeca
	started = nil
	c = New(Deps{Version: "v0.6.0",
		LatestRelease: func(context.Context) (string, error) { return "v0.6.0", nil },
		SelfUpdate:    func(_ context.Context, v string) error { started = append(started, v); return nil }})
	if r, err := c.UpdateCais(ctx, ""); err != nil || r.Started || r.To != "v0.6.0" || len(started) > 0 {
		t.Fatalf("ja na mais nova: %+v %v %v", r, err, started)
	}

	// GitHub fora do ar; cais de desenvolvimento
	c = New(Deps{Version: "v0.5.3", LatestRelease: func(context.Context) (string, error) { return "", errors.New("timeout") }})
	if _, err := c.UpdateCais(ctx, ""); err == nil || !strings.Contains(err.Error(), "release mais novo") {
		t.Fatalf("sem GitHub: %v", err)
	}
	c = New(Deps{Version: "dev"})
	if _, err := c.UpdateCais(ctx, ""); err == nil || !strings.Contains(err.Error(), "versao dev") {
		t.Fatalf("dev: %v", err)
	}
}
