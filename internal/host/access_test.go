package host

import (
	"context"
	"slices"
	"strings"
	"testing"
)

const (
	keyPatrick = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl patrick@note"
	keyMaria   = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHnD6ePWgnF2tO2Tu7NfQ8cFTaPhrYm2pXkF0hm8iEh3 maria@pc"
	keyJoao    = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBk4wXnWdS7nV8jYqE3cT2oK9pL5mR1hU6aZ0xF4gQ7e joao@pc"
)

// uma VPS preparada: o admin deploy com a chave do patrick (tambem no root
// e no cais), SSH sem o root
func accessVPS(t *testing.T) *fakeSys {
	t.Helper()
	f := newFake()
	f.users["deploy"], f.users[CaisUser] = true, true
	f.groups["deploy"] = []string{"sudo"}
	f.pw["deploy"] = true
	f.files[caisSSHDropin] = "Match User cais\n"
	f.files[SSHDropin] = sshdHarden
	f.files[rootKeys] = keyPatrick + "\n"
	f.files["/home/deploy/.ssh/authorized_keys"] = keyPatrick + "\n"
	f.files["/home/cais/.ssh/authorized_keys"] = keyPatrick + "\n"
	return f
}

func fp(t *testing.T, line string) string {
	t.Helper()
	_, k, err := ParseKey(line)
	if err != nil {
		t.Fatal(err)
	}
	return k.Fingerprint
}

func TestParseKey(t *testing.T) {
	line, k, err := ParseKey("  " + keyPatrick + "  ")
	if err != nil || line != keyPatrick || k.Type != "ssh-ed25519" || k.Comment != "patrick@note" || !strings.HasPrefix(k.Fingerprint, "SHA256:") {
		t.Fatalf("%q %+v %v", line, k, err)
	}
	for in, want := range map[string]string{
		"-----BEGIN OPENSSH PRIVATE KEY-----\nb3Blbn...": "PRIVADA",
		"ola mundo":                  "nao parece uma chave publica",
		"ssh-ed25519 nao-e-base64!":  "incompleta",
		keyPatrick + "\n" + keyMaria: "uma chave por vez",
	} {
		if _, _, err := ParseKey(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", in, err)
		}
	}
}

func TestAcessos(t *testing.T) {
	ctx := context.Background()
	f := accessVPS(t)

	a, err := ListAccess(ctx, f)
	if err != nil || !a.TUI || len(a.Keys) != 1 || !slices.Equal(a.Keys[0].Users, []string{"root", "deploy", "cais"}) ||
		len(a.Admins) != 1 || a.Admins[0] != (Admin{Name: "deploy", Keys: 1}) {
		t.Fatalf("%+v %v", a, err)
	}

	// so a TUI: a chave no cais, com dono e permissao
	if _, err := AddAccess(ctx, f, AccessRequest{Key: keyJoao}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.files["/home/cais/.ssh/authorized_keys"], keyJoao) || strings.Contains(f.files["/home/deploy/.ssh/authorized_keys"], keyJoao) {
		t.Errorf("tui: %q", f.files)
	}
	f.index(t, "chown -R cais: /home/cais/.ssh")
	// de novo: nada muda
	before := f.files["/home/cais/.ssh/authorized_keys"]
	AddAccess(ctx, f, AccessRequest{Key: keyJoao + " outro-comentario"})
	if f.files["/home/cais/.ssh/authorized_keys"] != before {
		t.Error("duplicou a chave")
	}

	// admin novo: precisa da senha; cria, poe no sudo, a chave nele e no cais
	if _, err := AddAccess(ctx, f, AccessRequest{Key: keyMaria, Admin: "maria"}); err == nil || !strings.Contains(err.Error(), "senha do sudo") {
		t.Fatalf("sem senha: %v", err)
	}
	if _, err := AddAccess(ctx, f, AccessRequest{Key: keyMaria, Admin: "root", Password: "s3nha-forte"}); err == nil {
		t.Error("aceitou o root")
	}
	if _, err := AddAccess(ctx, f, AccessRequest{Key: keyMaria, Admin: "maria", Password: "s3nha-forte"}); err != nil {
		t.Fatal(err)
	}
	if !f.users["maria"] || !slices.Contains(f.groups["maria"], "sudo") || !strings.Contains(f.files["/home/maria/.ssh/authorized_keys"], keyMaria) ||
		!strings.Contains(f.files["/home/cais/.ssh/authorized_keys"], keyMaria) || !slices.Contains(f.inputs, "maria:s3nha-forte\n") {
		t.Fatalf("admin: %+v %q", f.groups, f.files)
	}

	// tirar a chave do joao: sai do cais
	if _, err := RemoveKey(ctx, f, fp(t, keyJoao)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.files["/home/cais/.ssh/authorized_keys"], "joao@pc") {
		t.Error("a chave do joao ficou")
	}

	// tirar a admin maria: sai do sudo, expira, as chaves dela saem dela e
	// do cais; a do patrick fica
	if _, err := RemoveAdmin(ctx, f, "maria"); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(f.groups["maria"], "sudo") || !f.expired["maria"] || f.files["/home/maria/.ssh/authorized_keys"] != "" ||
		strings.Contains(f.files["/home/cais/.ssh/authorized_keys"], "maria@pc") || !strings.Contains(f.files["/home/cais/.ssh/authorized_keys"], "patrick@note") {
		t.Errorf("remover admin: %+v %v %q", f.groups, f.expired, f.files)
	}
	// volta: a conta deixa de expirar
	if _, err := AddAccess(ctx, f, AccessRequest{Key: keyMaria, Admin: "maria"}); err != nil || f.expired["maria"] {
		t.Errorf("de volta: %v %v", err, f.expired)
	}

	// trancado fora: a ultima chave de admin nao sai
	if _, err := RemoveAdmin(ctx, f, "maria"); err != nil {
		t.Fatal(err)
	}
	if _, err := RemoveKey(ctx, f, fp(t, keyPatrick)); err == nil || !strings.Contains(err.Error(), "trancado fora") {
		t.Errorf("tirou a ultima: %v", err)
	}
	if _, err := RemoveAdmin(ctx, f, "deploy"); err == nil || !strings.Contains(err.Error(), "trancado fora") {
		t.Errorf("tirou o ultimo admin: %v", err)
	}
}
