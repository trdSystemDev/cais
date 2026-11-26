package core

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/trdSystemDev/cais/internal/backup"
	"github.com/trdSystemDev/cais/internal/backup/onedrivetest"
	"github.com/trdSystemDev/cais/internal/config"
)

// waitBackupEmail espera o login terminar com a conta email.
func waitBackupEmail(t *testing.T, c *Core, email string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, _ := c.Snapshot(context.Background())
		if st.Backup.Email == email && st.Backup.Login == nil {
			return
		}
		if st.Backup.LoginError != "" || time.Now().After(deadline) {
			t.Fatalf("login nao terminou: %+v", st.Backup)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// o destino troca pro OneDrive (conta pessoal) e volta pro Google Drive sem
// perder o client nem o login de nenhum dos dois; backup, lista, restore e o
// login renovado (a Microsoft troca o refresh token) no OneDrive
func TestBackupNoOneDrive(t *testing.T) {
	c, drive, _, _ := newBackupCore(t)
	o := onedrivetest.New()
	t.Cleanup(o.Close)
	c.deps.MicrosoftEndpoint, c.deps.GraphAPI = o.Endpoint(), o.API()
	ctx := context.Background()
	priv := setupBackup(t, c, drive)
	if _, err := c.RunBackup(ctx); err != nil {
		t.Fatal(err)
	}

	if err := c.SetBackupProvider(ctx, "dropbox"); err == nil {
		t.Error("aceitou destino desconhecido")
	}
	if err := c.SetBackupProvider(ctx, backup.ProviderOneDrive); err != nil {
		t.Fatal(err)
	}
	st, _ := c.RefreshBackup(ctx)
	// o app e sempre o do cais: falta so o login
	if st.Provider != backup.ProviderOneDrive || st.Email != "" || st.Ready() {
		t.Fatalf("OneDrive herdou o Google: %+v", st)
	}
	if _, err := c.RunBackup(ctx); err == nil || !strings.Contains(err.Error(), "falta conectar o OneDrive") {
		t.Fatalf("backup sem configurar o OneDrive: %v", err)
	}
	login, err := c.StartDriveLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if login.Code != "MSFT-1234" || login.URL != "https://www.microsoft.com/link" {
		t.Fatalf("login %+v", login)
	}
	o.Approve()
	waitBackupEmail(t, c, "pessoa@outlook.com")

	run, err := c.RunBackup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if run.Provider != backup.ProviderOneDrive || !slices.Equal(o.Names(), []string{run.File}) {
		t.Fatalf("backup %+v, no OneDrive: %v", run, o.Names())
	}
	if len(drive.Names()) != 1 {
		t.Errorf("o backup do OneDrive foi pro Google Drive: %v", drive.Names())
	}

	// o acesso venceu: a renovacao traz outro refresh token, que e gravado
	tokenSecret := loginSecret(backup.ProviderOneDrive)
	data, _ := c.secretStore().Get(tokenSecret)
	var tok oauth2.Token
	json.Unmarshal(data, &tok)
	before := tok.RefreshToken
	tok.Expiry = time.Now().Add(-time.Hour)
	data, _ = json.Marshal(tok)
	c.secretStore().Put(tokenSecret, data)
	files, err := c.BackupFiles(ctx)
	if err != nil || len(files) != 1 || files[0].Name != run.File {
		t.Fatalf("files %+v %v", files, err)
	}
	data, _ = c.secretStore().Get(tokenSecret)
	json.Unmarshal(data, &tok)
	if tok.RefreshToken == before || tok.RefreshToken == "" {
		t.Fatalf("o refresh token novo nao foi gravado (%q)", tok.RefreshToken)
	}

	res, err := c.RestoreBackup(ctx, RestoreRequest{File: run.File, Identity: priv})
	if err != nil || !slices.Equal(res.Available, []string{"mysql/blog", "postgres/loja"}) {
		t.Fatalf("restore do OneDrive %+v %v", res, err)
	}

	// de volta pro Google: client e login continuam la
	if err := c.SetBackupProvider(ctx, backup.ProviderGoogle); err != nil {
		t.Fatal(err)
	}
	st, _ = c.RefreshBackup(ctx)
	if !st.Ready() || st.Email != "pessoa@exemplo.com" {
		t.Fatalf("o Google perdeu a config: %+v", st)
	}
	if files, err := c.BackupFiles(ctx); err != nil || len(files) != 1 || files[0].Name != drive.Names()[0] {
		t.Fatalf("files do Google %+v %v", files, err)
	}
	if info, _ := config.Load(c.deps.ConfigPath); info.Backup.Provider != "" {
		t.Errorf("Google gravado como %q (o padrao fica vazio)", info.Backup.Provider)
	}
	if len(o.Errors) > 0 {
		t.Errorf("o cais fugiu do protocolo do Graph: %v", o.Errors)
	}
}
