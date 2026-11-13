package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/trdSystemDev/cais/internal/backup"
)

func TestBackupOpenExtraiComPermissoes(t *testing.T) {
	pub, priv, _ := backup.NewKey()
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "secrets"), 0o700)
	os.WriteFile(filepath.Join(src, "secrets", "jwt"), []byte("segredo"), 0o444)
	var arch bytes.Buffer
	w, _ := backup.NewWriter(&arch, pub, t.TempDir(), time.Now())
	w.AddStream(context.Background(), "postgres/loja.sql", func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, "SELECT 1;")
		return err
	})
	w.AddDir("state/data", src, nil)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "b.tar.gz.age")
	os.WriteFile(file, arch.Bytes(), 0o600)
	key := filepath.Join(dir, "chave.txt")
	os.WriteFile(key, []byte(priv+"\n"), 0o600)

	to := filepath.Join(dir, "out")
	if err := backupOpen([]string{file}, key, to); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(to, "postgres", "loja.sql")); string(data) != "SELECT 1;" {
		t.Errorf("dump %q", data)
	}
	info, err := os.Stat(filepath.Join(to, "state", "data", "secrets", "jwt"))
	if err != nil || info.Mode().Perm() != 0o444 {
		t.Errorf("secret %v %v", info, err)
	}
}

// um arquivo malicioso (caminho com ..) nao escreve fora da pasta
func TestBackupOpenNaoSaiDaPasta(t *testing.T) {
	pub, priv, _ := backup.NewKey()
	r, _ := age.ParseX25519Recipient(pub)
	var arch bytes.Buffer
	enc, _ := age.Encrypt(&arch, r)
	gz := newGzip(enc)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "../fora.txt", Mode: 0o644, Size: 3, Typeflag: tar.TypeReg})
	tw.Write([]byte("mal"))
	tw.Close()
	gz.Close()
	enc.Close()
	dir := t.TempDir()
	file := filepath.Join(dir, "b.age")
	os.WriteFile(file, arch.Bytes(), 0o600)
	key := filepath.Join(dir, "k")
	os.WriteFile(key, []byte(priv), 0o600)
	if err := backupOpen([]string{file}, key, filepath.Join(dir, "out")); err == nil {
		t.Error("extraiu caminho com ..")
	}
	if _, err := os.Stat(filepath.Join(dir, "fora.txt")); err == nil {
		t.Fatal("escreveu fora da pasta")
	}
}

func newGzip(w io.Writer) *gzip.Writer { return gzip.NewWriter(w) }
