package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestMakeSegueQuandoUmBancoFalha(t *testing.T) {
	pub, priv, _ := NewKey()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("domain: x\n"), 0o600)
	job := Job{
		Recipient: pub,
		DBs:       []DB{{"postgres", "loja"}, {"mysql", "quebrado"}, {"mysql", "blog"}},
		Dump: func(_ context.Context, db DB, w io.Writer) error {
			if db.Name == "quebrado" {
				return errors.New("mysqldump: Access denied")
			}
			_, err := io.WriteString(w, "-- dump "+db.Name)
			return err
		},
		Dirs: []Dir{{Prefix: "state/etc", Path: dir}, {Prefix: "state/nada", Path: filepath.Join(dir, "nao-existe")}},
		Tmp:  t.TempDir(),
		Now:  time.Now(),
	}
	var out bytes.Buffer
	res, err := Make(context.Background(), job, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Files, []string{"postgres/loja.sql", "mysql/blog.sql", "state/etc/config.yaml"}) {
		t.Errorf("arquivos %v", res.Files)
	}
	if len(res.Errors) != 1 || !bytes.Contains([]byte(res.Errors[0]), []byte("mysql/quebrado: mysql/quebrado.sql: mysqldump: Access denied")) {
		t.Errorf("erros %v", res.Errors)
	}
	if left, _ := os.ReadDir(job.Tmp); len(left) != 0 {
		t.Errorf("sobrou temporario: %v", left)
	}
	r, err := NewReader(&out, priv)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := r.Next()
	data, _ := io.ReadAll(r)
	if h.Name != "postgres/loja.sql" || string(data) != "-- dump loja" {
		t.Errorf("%s: %q", h.Name, data)
	}
}

func TestExpiredGuardaOMaisNovo(t *testing.T) {
	now := time.Date(2026, 11, 13, 3, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return now.AddDate(0, 0, -n) }
	files := []File{
		{ID: "a", Name: "cais-vps-2026-09-01_0300.tar.gz.age", Created: day(73)},
		{ID: "b", Name: "cais-vps-2026-10-13_0300.tar.gz.age", Created: day(31)},
		{ID: "c", Name: "cais-vps-2026-10-20_0300.tar.gz.age", Created: day(24)},
		{ID: "x", Name: "notas.txt", Created: day(90)}, // nao e backup: fica
	}
	var ids []string
	for _, f := range Expired(files, 30, now) {
		ids = append(ids, f.ID)
	}
	if !slices.Equal(ids, []string{"a", "b"}) {
		t.Errorf("expirados %v", ids)
	}
	// backup parado ha meses: o mais novo fica
	ids = nil
	for _, f := range Expired(files[:2], 30, now) {
		ids = append(ids, f.ID)
	}
	if !slices.Equal(ids, []string{"a"}) {
		t.Errorf("expirados %v", ids)
	}
	if Expired(files, 0, now) != nil {
		t.Error("retencao 0 apagou")
	}
}

func TestFileName(t *testing.T) {
	got := FileName("VPS_1.Exemplo.com", time.Date(2026, 11, 13, 3, 4, 0, 0, time.UTC))
	if got != "cais-vps-1.exemplo.com-2026-11-13_0304.tar.gz.age" || !IsBackup(got) {
		t.Errorf("%s", got)
	}
}
