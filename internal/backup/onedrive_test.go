package backup

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/trdSystemDev/cais/internal/backup/onedrivetest"
)

// loginOneDrive faz o login de dispositivo da Microsoft (cliente publico) e
// devolve o OneDrive com o token ja vencido, pra renovar na primeira
// chamada; saved recebe cada login renovado.
func loginOneDrive(t *testing.T, s *onedrivetest.Server, saved *[]string) *OneDrive {
	t.Helper()
	cfg := OneDriveOAuth("00000000-0000-0000-0000-000000000001", s.Endpoint())
	ctx := context.Background()
	da, err := cfg.DeviceAuth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if da.UserCode != "MSFT-1234" || da.VerificationURI != "https://www.microsoft.com/link" {
		t.Fatalf("codigo %+v", da)
	}
	go func() { time.Sleep(50 * time.Millisecond); s.Approve() }()
	tok, err := cfg.DeviceAccessToken(ctx, da)
	if err != nil {
		t.Fatal(err)
	}
	tok.Expiry = time.Now().Add(-time.Hour)
	src := SavingSource(cfg.TokenSource(ctx, tok), tok.RefreshToken, func(t *oauth2.Token) error {
		*saved = append(*saved, t.RefreshToken)
		return nil
	})
	o := NewOneDrive(ctx, src)
	o.API, o.Chunk = s.API(), 320*1024 // pedacos pequenos: o envio vai em varios
	return o
}

func TestOneDriveLoginEnvioListaEExclusao(t *testing.T) {
	s := onedrivetest.New()
	defer s.Close()
	var saved []string
	o := loginOneDrive(t, s, &saved)
	ctx := context.Background()

	if email, err := o.Email(ctx); err != nil || email != "pessoa@outlook.com" {
		t.Fatalf("email %q %v", email, err)
	}
	// a Microsoft troca o refresh token na renovacao: o novo e gravado
	if len(saved) != 1 || saved[0] != "renova-2" {
		t.Fatalf("login renovado nao foi gravado: %v", saved)
	}

	folder, err := o.Folder(ctx, "", "cais-backups-vps")
	if err != nil || folder == "" {
		t.Fatal(folder, err)
	}
	if again, err := o.Folder(ctx, folder, "cais-backups-vps"); err != nil || again != folder {
		t.Fatalf("pasta pelo id %q %v", again, err)
	}
	// sem o id (estado perdido), acha a pasta pelo nome em vez de criar outra
	if again, err := o.Folder(ctx, "", "cais-backups-vps"); err != nil || again != folder {
		t.Fatalf("pasta pelo nome %q %v", again, err)
	}

	// 3 pedacos de 320 KiB e um resto
	data := bytes.Repeat([]byte("backup!"), 3*320*1024/7+1000)
	f, err := o.Put(ctx, folder, "b1.age", bytes.NewReader(data), int64(len(data)))
	if err != nil || f.Name != "b1.age" || f.Size != int64(len(data)) {
		t.Fatalf("envio %+v %v", f, err)
	}
	if !bytes.Equal(s.Data("b1.age"), data) {
		t.Fatal("conteudo enviado diferente")
	}

	// lista paginada, do mais velho pro mais novo
	s.PageSize = 2
	old := time.Now().Add(-48 * time.Hour)
	s.Add(folder, "b0.age", []byte("velho"), old)
	s.Add(folder, "bx.age", []byte("mais velho"), old.Add(-time.Hour))
	list, err := o.List(ctx, folder)
	if err != nil || len(list) != 3 || list[0].Name != "bx.age" || list[1].Name != "b0.age" || list[2].ID != f.ID {
		t.Fatalf("lista %+v %v", list, err)
	}

	var got bytes.Buffer
	if err := o.Get(ctx, f.ID, &got); err != nil || !bytes.Equal(got.Bytes(), data) {
		t.Fatalf("download (%d bytes) %v", got.Len(), err)
	}
	if err := o.Delete(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	if err := o.Get(ctx, f.ID, &got); !errors.Is(err, ErrNotFound) {
		t.Errorf("baixou excluido: %v", err)
	}

	// pasta apagada no OneDrive: cria outra
	s.DeleteAll()
	if again, err := o.Folder(ctx, folder, "cais-backups-vps"); err != nil || again == folder || again == "" {
		t.Fatalf("pasta apagada reaproveitada: %q %v", again, err)
	}
	if len(s.Errors) > 0 {
		t.Errorf("o cliente fugiu do protocolo do Graph: %v", s.Errors)
	}
}

func TestOneDriveErroDoGraphNaMensagem(t *testing.T) {
	s := onedrivetest.New()
	defer s.Close()
	var saved []string
	o := loginOneDrive(t, s, &saved)
	_, err := o.Put(context.Background(), "nao-existe", "b.age", strings.NewReader("x"), 1)
	if err == nil || !strings.Contains(err.Error(), "itemNotFound") && !errors.Is(err, ErrNotFound) {
		t.Errorf("erro %v", err)
	}
}
