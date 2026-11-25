package remotes

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestLista(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cais", "remotes.yaml")
	list, err := Load(path)
	if err != nil || len(list) != 0 {
		t.Fatalf("sem arquivo: %v %v", list, err)
	}
	list, existed, err := Add(list, Remote{Name: "prod", Target: "cais@prod.tail1234.ts.net"})
	if err != nil || existed {
		t.Fatal(err, existed)
	}
	list, _, _ = Add(list, Remote{Name: "dev", Target: "cais@100.90.100.127"})
	list, existed, _ = Add(list, Remote{Name: "prod", Target: "cais@prod.exemplo.com.br"})
	if !existed || len(list) != 2 || list[0].Target != "cais@prod.exemplo.com.br" {
		t.Fatalf("trocar o destino: %v", list)
	}
	if err := Save(path, list); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("permissao %v", fi.Mode().Perm())
	}
	got, err := Load(path)
	if err != nil || !slices.Equal(got, list) || !slices.Equal(Names(got), []string{"prod", "dev"}) {
		t.Fatalf("relido: %v %v", got, err)
	}
	if r, ok := Find(got, "dev"); !ok || r.Target != "cais@100.90.100.127" {
		t.Errorf("find: %v", r)
	}
	got, ok := Remove(got, "prod")
	if !ok || !slices.Equal(Names(got), []string{"dev"}) {
		t.Errorf("remove: %v", got)
	}
	if _, ok := Remove(got, "nao-existe"); ok {
		t.Error("removeu o que nao existe")
	}
}

// o destino vai pro ssh: nada que vire opcao dele
func TestValida(t *testing.T) {
	for _, r := range []Remote{
		{"prod", "-oProxyCommand=touch /tmp/x"},
		{"prod", "cais@vps; rm -rf /"},
		{"prod", "cais@vps com espaco"},
		{"prod", ""},
		{"Prod", "cais@vps"},
		{"-prod", "cais@vps"},
		{"", "cais@vps"},
	} {
		if err := r.Validate(); err == nil {
			t.Errorf("aceitou %+v", r)
		}
	}
	for _, r := range []Remote{{"prod", "cais@vps.exemplo.com.br"}, {"dev-2", "vps-dev"}, {"x", "cais@fd7a:115c:a1e0::1"}} {
		if err := r.Validate(); err != nil {
			t.Errorf("recusou %+v: %v", r, err)
		}
	}
	args := SSHArgs("cais@vps", true, "cais", "mcp")
	if i := slices.Index(args, "--"); i < 0 || args[i+1] != "cais@vps" || !slices.Contains(args, "BatchMode=yes") {
		t.Errorf("ssh: %v", args)
	}
}
