package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestNewLogger(t *testing.T) {
	var b bytes.Buffer
	l, err := newLogger(&b, "text", "info", false)
	if err != nil {
		t.Fatal(err)
	}
	l.Info("deploy comecou", "acao", "deploy", "app", "loja")
	l.Debug("nao aparece no nivel info")
	if got := b.String(); !strings.HasPrefix(got, "time=") || !strings.Contains(got, `msg="deploy comecou" acao=deploy app=loja`) || strings.Contains(got, "nao aparece") {
		t.Fatalf("texto: %q", got)
	}

	// no journald o horario sai (o journal ja tem o dele)
	b.Reset()
	l, _ = newLogger(&b, "text", "info", true)
	l.Info("x", "app", "loja")
	if got := b.String(); strings.Contains(got, "time=") || !strings.HasPrefix(got, "level=INFO") {
		t.Fatalf("journal: %q", got)
	}

	b.Reset()
	l, _ = newLogger(&b, "json", "warn", false)
	l.Info("abaixo do nivel")
	l.Warn("aviso", "app", "loja")
	var m map[string]any
	if err := json.Unmarshal(b.Bytes(), &m); err != nil || m["app"] != "loja" || m["level"] != "WARN" {
		t.Fatalf("json: %q (%v)", b.String(), err)
	}

	for _, bad := range [][2]string{{"xml", "info"}, {"text", "verboso"}} {
		if _, err := newLogger(&b, bad[0], bad[1], false); err == nil {
			t.Errorf("aceitou %v", bad)
		}
	}
}
