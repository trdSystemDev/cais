package backup

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trdSystemDev/cais/internal/backup/drivetest"
)

func login(t *testing.T, s *drivetest.Server) *Drive {
	t.Helper()
	cfg := Client{ID: "id", Secret: "segredo"}.OAuth(s.Endpoint())
	ctx := context.Background()
	da, err := cfg.DeviceAuth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if da.UserCode != "ABCD-EFGH" || da.VerificationURI != "https://www.google.com/device" {
		t.Fatalf("codigo %+v", da)
	}
	go func() { time.Sleep(50 * time.Millisecond); s.Approve() }()
	tok, err := cfg.DeviceAccessToken(ctx, da)
	if err != nil {
		t.Fatal(err)
	}
	if tok.RefreshToken != "renova-1" {
		t.Fatalf("token %+v", tok)
	}
	// o token expirado e renovado sozinho
	tok.Expiry = time.Now().Add(-time.Hour)
	d := NewDrive(ctx, cfg, tok)
	d.API, d.Upload = s.API(), s.Upload()
	return d
}

func TestDriveLoginEnvioListaEExclusao(t *testing.T) {
	s := drivetest.New()
	defer s.Close()
	d := login(t, s)
	ctx := context.Background()

	if email, err := d.Email(ctx); err != nil || email != "pessoa@exemplo.com" {
		t.Fatalf("email %q %v", email, err)
	}
	folder, err := d.Folder(ctx, "", "cais-backups")
	if err != nil || folder == "" {
		t.Fatal(folder, err)
	}
	// pasta existente e reaproveitada
	if again, err := d.Folder(ctx, folder, "cais-backups"); err != nil || again != folder {
		t.Fatalf("pasta %q %v", again, err)
	}
	data := []byte("conteudo criptografado")
	f, err := d.Put(ctx, folder, "b1.age", bytes.NewReader(data), int64(len(data)))
	if err != nil || f.Name != "b1.age" || f.Size != int64(len(data)) {
		t.Fatalf("envio %+v %v", f, err)
	}
	list, err := d.List(ctx, folder)
	if err != nil || len(list) != 1 || list[0].ID != f.ID {
		t.Fatalf("lista %+v %v", list, err)
	}
	var got bytes.Buffer
	if err := d.Get(ctx, f.ID, &got); err != nil || got.String() != string(data) {
		t.Fatalf("download %q %v", got.String(), err)
	}
	if err := d.Delete(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(s.Names(), nil) {
		t.Errorf("sobrou %v", s.Names())
	}
	if err := d.Get(ctx, f.ID, &got); !errors.Is(err, ErrNotFound) {
		t.Errorf("baixou excluido: %v", err)
	}

	// pasta apagada no Drive: cria outra
	s.TrashAll()
	if again, err := d.Folder(ctx, folder, "cais-backups"); err != nil || again == folder {
		t.Fatalf("pasta na lixeira reaproveitada: %q %v", again, err)
	}
}

func TestDriveErroDoGoogleNaMensagem(t *testing.T) {
	s := drivetest.New()
	defer s.Close()
	d := login(t, s)
	s.Fail = "GET /drive/v3/about"
	if _, err := d.Email(context.Background()); err == nil || !strings.Contains(err.Error(), "falha de teste") {
		t.Errorf("erro %v", err)
	}
}
