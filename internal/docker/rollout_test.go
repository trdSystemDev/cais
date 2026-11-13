package docker

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// rolloutRunner simula o docker do rollout: containers do service antes e
// depois do up, hash e imagem atuais, e grava os comandos.
type rolloutRunner struct {
	calls   []string
	old     []string // rodando antes
	after   []string // todos depois do up --scale
	hash    string   // config --hash atual
	image   string   // ID da imagem atual
	running string   // "hash imagem" de cada container antigo
	failUp  bool
}

func (r *rolloutRunner) run(_ context.Context, name string, args ...string) (string, error) {
	cmd := strings.Join(args, " ")
	r.calls = append(r.calls, cmd)
	switch {
	case strings.Contains(cmd, "ps -q --status running web"):
		return strings.Join(r.old, "\n"), nil
	case strings.Contains(cmd, "ps -q -a web"):
		return strings.Join(r.after, "\n"), nil
	case strings.Contains(cmd, "config --hash web"):
		return "web " + r.hash + "\n", nil
	case strings.HasPrefix(cmd, "image inspect"):
		return r.image + "\n", nil
	case strings.HasPrefix(cmd, "inspect"):
		var lines []string
		for range r.old {
			lines = append(lines, r.running)
		}
		return strings.Join(lines, "\n"), nil
	case strings.Contains(cmd, " up "):
		if r.failUp {
			return "", errors.New("container app-web-2 is unhealthy")
		}
	}
	return "", nil
}

func init() { rolloutPause = 0 }

var proj = Project{Name: "app", Files: []string{"/apps/app/src/compose.yml"}}

func TestRolloutSobeONovoAntesDeTirarOAntigo(t *testing.T) {
	r := &rolloutRunner{old: []string{"a1"}, hash: "h2", image: "i2", running: "h1 i1"}
	if err := rollout(context.Background(), r.run, proj, "web", "app-web"); err != nil {
		t.Fatal(err)
	}
	var steps []string
	for _, c := range r.calls {
		switch {
		case strings.Contains(c, " up "):
			steps = append(steps, "up")
			if !strings.Contains(c, "--no-recreate --scale web=2 web") || !strings.Contains(c, "--wait") {
				t.Errorf("up %q", c)
			}
		case c == "stop a1", c == "rm a1":
			steps = append(steps, c)
		case strings.HasPrefix(c, "rm"), strings.HasPrefix(c, "stop"):
			t.Errorf("comando inesperado %q", c)
		}
	}
	if !slices.Equal(steps, []string{"up", "stop a1", "rm a1"}) {
		t.Errorf("ordem %v (chamadas %v)", steps, r.calls)
	}
}

func TestRolloutSemMudancaNaoTrocaNada(t *testing.T) {
	r := &rolloutRunner{old: []string{"a1"}, hash: "h1", image: "i1", running: "h1 i1"}
	if err := rollout(context.Background(), r.run, proj, "web", "app-web"); err != nil {
		t.Fatal(err)
	}
	for _, c := range r.calls {
		if strings.Contains(c, " up ") || strings.HasPrefix(c, "stop") || strings.HasPrefix(c, "rm") {
			t.Errorf("mexeu no container igual: %q", c)
		}
	}
	// so a imagem mudou (rebuild com codigo novo, mesmo compose): troca
	r = &rolloutRunner{old: []string{"a1"}, hash: "h1", image: "i2", running: "h1 i1"}
	rollout(context.Background(), r.run, proj, "web", "app-web")
	if !slices.Contains(r.calls, "stop a1") {
		t.Errorf("imagem nova nao trocou: %v", r.calls)
	}
}

func TestRolloutSemContainerDeixaProUp(t *testing.T) {
	r := &rolloutRunner{}
	if err := rollout(context.Background(), r.run, proj, "web", "app-web"); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 1 {
		t.Errorf("chamadas %v", r.calls)
	}
}

func TestRolloutNovoDoenteSaiEOAntigoFica(t *testing.T) {
	r := &rolloutRunner{old: []string{"a1"}, after: []string{"a1", "n2"}, hash: "h2", image: "i2", running: "h1 i1", failUp: true}
	err := rollout(context.Background(), r.run, proj, "web", "app-web")
	if err == nil || !strings.Contains(err.Error(), "unhealthy") {
		t.Fatalf("erro %v", err)
	}
	if !slices.Contains(r.calls, "rm -f n2") {
		t.Errorf("nao removeu o novo: %v", r.calls)
	}
	for _, c := range r.calls {
		if strings.Contains(c, "a1") && (strings.HasPrefix(c, "stop") || strings.HasPrefix(c, "rm")) {
			t.Errorf("mexeu no antigo: %q", c)
		}
	}
}
