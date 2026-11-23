package rpc

import (
	"context"
	"testing"

	"github.com/trdSystemDev/cais/internal/apps"
)

// Custo de uma chamada pelo socket: sem parametros (state.get) e com
// parametros (app.deploy).
func BenchmarkChamadaSemParametros(b *testing.B) {
	fake, c := startContract(b)
	fake.out["Snapshot"] = newFiller().value(typeOfState, "State").Interface()
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.Snapshot(ctx); err != nil {
			b.Fatal(err)
		}
		fake.take()
	}
}

func BenchmarkChamadaComParametros(b *testing.B) {
	fake, c := startContract(b)
	fake.out["DeployApp"] = apps.DeployResult{App: "loja", Commit: "c0ffee"}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.DeployApp(ctx, "loja", "main"); err != nil {
			b.Fatal(err)
		}
		fake.take()
	}
}
