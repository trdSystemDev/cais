package core

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunMaintenanceLimpaOCacheDeBuild(t *testing.T) {
	var n atomic.Int32
	c := New(Deps{PruneBuildCache: func(context.Context) error { n.Add(1); return nil }})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.RunMaintenance(ctx, time.Millisecond, 5*time.Millisecond); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for n.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if n.Load() < 2 {
		t.Errorf("limpou %d vezes", n.Load())
	}
}
