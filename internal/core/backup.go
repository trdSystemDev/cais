package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/trdSystemDev/cais/internal/backup"
	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/fsx"
	"github.com/trdSystemDev/cais/internal/secrets"
)

// Secrets do backup: o token do login de cada destino (o refresh token
// renova o acesso sozinho). O client e sempre o app do cais, embutido.
const (
	secretDriveToken    = "google_drive_token"
	secretOneDriveToken = "onedrive_token"
)

// loginSecret e o secret do login do destino p.
func loginSecret(p string) string {
	if p == backup.ProviderOneDrive {
		return secretOneDriveToken
	}
	return secretDriveToken
}

// providerOf e o destino da config (vazio: Google Drive).
func providerOf(cfg config.Config) string {
	if cfg.Backup.Provider == backup.ProviderOneDrive {
		return backup.ProviderOneDrive
	}
	return backup.ProviderGoogle
}

// backupProvider e o destino dos backups agora.
func (c *Core) backupProvider() string {
	cfg, _ := config.Load(c.deps.ConfigPath)
	return providerOf(cfg)
}

// BackupStatus e o backup no estado: o destino (Google Drive ou OneDrive),
// o login e o ultimo backup. Nunca leva segredo (nem o token; a chave
// publica nao e segredo). O client e sempre o app do cais.
type BackupStatus struct {
	// Provider e o destino: google (Google Drive) ou onedrive.
	Provider string `json:"provider"`
	// Email e a conta conectada no destino (vazio: sem login).
	Email string `json:"email,omitempty"`
	// Login e o login esperando a pessoa digitar o codigo.
	Login      *DriveLogin `json:"login,omitempty"`
	LoginError string      `json:"login_error,omitempty"`
	// Recipient e a chave publica que criptografa os backups.
	Recipient     string `json:"recipient,omitempty"`
	Time          string `json:"time,omitempty"`
	RetentionDays int    `json:"retention_days"`
	// Next e o proximo backup automatico (zero: desligado ou incompleto).
	Next time.Time `json:"next,omitzero"`
	// Retry diz que o Next e a nova tentativa de um backup que nao chegou
	// na nuvem (e nao o horario do dia).
	Retry   bool       `json:"retry,omitempty"`
	Running bool       `json:"running,omitempty"`
	Step    string     `json:"step,omitempty"`
	Last    *BackupRun `json:"last,omitempty"`
	Error   string     `json:"error,omitempty"`
}

// Ready diz se o backup tem tudo pra rodar: client, login e chave.
func (b BackupStatus) Ready() bool { return b.Email != "" && b.Recipient != "" }

// DriveLogin e o codigo que a pessoa digita em URL pra liberar o destino
// (Google Drive ou OneDrive).
type DriveLogin struct {
	Code    string    `json:"code"`
	URL     string    `json:"url"`
	Expires time.Time `json:"expires"`
}

// BackupRun e um backup feito (ou tentado).
type BackupRun struct {
	At       time.Time     `json:"at"`
	Duration time.Duration `json:"duration"`
	Auto     bool          `json:"auto,omitempty"`
	// Provider e pra onde foi (google ou onedrive).
	Provider string `json:"provider,omitempty"`
	// File e o nome na nuvem; Size em bytes (criptografado e compactado).
	File      string `json:"file,omitempty"`
	Size      int64  `json:"size,omitempty"`
	Databases int    `json:"databases"`
	Files     int    `json:"files"`
	// Errors sao os databases que nao entraram (o resto entrou).
	Errors []string `json:"errors,omitempty"`
	// Error e o que impediu o backup de chegar na nuvem.
	Error string `json:"error,omitempty"`
	// Local e o arquivo que ficou na VPS porque o envio falhou.
	Local string `json:"local,omitempty"`
	// Removed sao os backups velhos apagados da nuvem pela retencao.
	Removed int `json:"removed,omitempty"`
}

// OK diz se o backup chegou na nuvem com todos os databases.
func (r BackupRun) OK() bool { return r.Error == "" && len(r.Errors) == 0 }

// backupDisk e o que o cais guarda do backup fora da config: a pasta em
// cada destino (o cais cria e so ve ela), a conta e o ultimo backup. Folder
// e Email sao do Google Drive (os nomes de antes do OneDrive).
type backupDisk struct {
	Folder   string     `json:"folder,omitempty"`
	Email    string     `json:"email,omitempty"`
	OneDrive cloudDisk  `json:"onedrive,omitzero"`
	Last     *BackupRun `json:"last,omitempty"`
}

type cloudDisk struct {
	Folder string `json:"folder,omitempty"`
	Email  string `json:"email,omitempty"`
}

// at sao a pasta e a conta do destino p.
func (d *backupDisk) at(p string) (folder, email *string) {
	if p == backup.ProviderOneDrive {
		return &d.OneDrive.Folder, &d.OneDrive.Email
	}
	return &d.Folder, &d.Email
}

// BackupGap e o que o backup nao cobre, de proposito por enquanto: aparece
// no painel do backup, no CLI, no card do Garage e nos buckets, pra ninguem
// achar que esta coberto.
const BackupGap = "Fora do backup: os arquivos do Garage (buckets S3) e os volumes dos apps. Perder a VPS (ou o disco) perde esses dados."

// uploadRetryDelay e a espera entre tentativas de envio pra nuvem.
var uploadRetryDelay = 5 * time.Second

type backupRuntime struct {
	mu       sync.Mutex // um backup ou restore por vez
	diskMu   sync.Mutex // state.json
	loginMu  sync.Mutex
	cancelLn context.CancelFunc
}

func (c *Core) backupStatePath() string { return filepath.Join(c.deps.BackupsDir, "state.json") }

func (c *Core) loadBackupDisk() backupDisk {
	var d backupDisk
	if data, err := os.ReadFile(c.backupStatePath()); err == nil {
		json.Unmarshal(data, &d)
	}
	return d
}

// changeBackupDisk le, muda e grava o state.json.
func (c *Core) changeBackupDisk(fn func(*backupDisk)) error {
	c.backup.diskMu.Lock()
	defer c.backup.diskMu.Unlock()
	d := c.loadBackupDisk()
	fn(&d)
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.deps.BackupsDir, 0o700); err != nil {
		return err
	}
	return fsx.WriteAtomic(c.backupStatePath(), data, 0o600)
}

// oauthConfig e o login do destino p com o app do cais (o do Google vem
// no build do release).
func (c *Core) oauthConfig(p string) (*oauth2.Config, error) {
	if p == backup.ProviderOneDrive {
		return backup.OneDriveOAuth(backup.DefaultOneDriveClient, c.deps.MicrosoftEndpoint), nil
	}
	cl, err := backup.GoogleClient()
	if err != nil {
		return nil, err
	}
	return cl.OAuth(c.deps.GoogleEndpoint), nil
}

// store abre o destino p com o token do login. O token renovado volta pro
// secret (a Microsoft troca o refresh token a cada renovacao), a nao ser
// que o login tenha mudado no meio (desconectou ou entrou de novo).
func (c *Core) store(p string) (backup.Store, error) {
	tokenSecret := loginSecret(p)
	name := backup.ProviderName(p)
	data, err := c.secretStore().Get(tokenSecret)
	if errors.Is(err, secrets.ErrNotFound) {
		return nil, fmt.Errorf("o %s nao esta conectado (Setup -> Backups -> Conectar)", name)
	}
	if err != nil {
		return nil, err
	}
	var tok oauth2.Token
	if err := json.Unmarshal(data, &tok); err != nil {
		return nil, fmt.Errorf("login do %s invalido, conecte de novo: %w", name, err)
	}
	var mu sync.Mutex
	prev := tok.RefreshToken
	save := func(t *oauth2.Token) error {
		mu.Lock()
		defer mu.Unlock()
		cur, err := c.secretStore().Get(tokenSecret)
		if errors.Is(err, secrets.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		var saved oauth2.Token
		if json.Unmarshal(cur, &saved) != nil || saved.RefreshToken != prev {
			return nil
		}
		data, _ := json.Marshal(t)
		if err := c.secretStore().Put(tokenSecret, data); err != nil {
			return err
		}
		prev = t.RefreshToken
		return nil
	}
	// o oauth2 renova o acesso sozinho; o contexto e o do cliente HTTP,
	// que vive mais que uma chamada
	ctx := context.Background()
	cfg, err := c.oauthConfig(p)
	if err != nil {
		return nil, err
	}
	src := backup.SavingSource(cfg.TokenSource(ctx, &tok), tok.RefreshToken, save)
	if p == backup.ProviderOneDrive {
		o := backup.NewOneDrive(ctx, src)
		o.API = c.deps.GraphAPI
		return o, nil
	}
	d := backup.NewDrive(ctx, src)
	d.API, d.Upload = c.deps.DriveAPI, c.deps.DriveUpload
	return d, nil
}

// RefreshBackup le config, secrets e o state.json e publica em
// State.Backup.
func (c *Core) RefreshBackup(context.Context) (BackupStatus, error) {
	st := c.readBackup()
	c.update(func(s *State) {
		st.Login, st.LoginError, st.Running, st.Step = s.Backup.Login, s.Backup.LoginError, s.Backup.Running, s.Backup.Step
		s.Backup = st
	})
	return c.snapshotBackup(), nil
}

func (c *Core) snapshotBackup() BackupStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.Backup
}

func (c *Core) readBackup() BackupStatus {
	var st BackupStatus
	cfg, err := config.Load(c.deps.ConfigPath)
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		st.Error = err.Error()
	}
	b := cfg.Backup
	st.Provider = providerOf(cfg)
	st.Recipient, st.Time, st.RetentionDays = b.Recipient, b.Time, b.Retention()
	disk := c.loadBackupDisk()
	tokenSecret := loginSecret(st.Provider)
	if _, err := c.secretStore().Get(tokenSecret); err == nil {
		_, email := disk.at(st.Provider)
		st.Email = cmpOr(*email, "(conta conectada)")
	}
	st.Last = disk.Last
	if st.Ready() {
		st.Next, st.Retry = nextBackup(b, disk.Last, c.deps.Now())
	}
	return st
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// backupRetry e a espera pra tentar de novo um backup que nao chegou na
// nuvem (nuvem fora do ar, rede, disco).
var backupRetry = time.Hour

// nextBackup e o proximo horario do backup automatico. Se o de hoje ja
// passou sem backup (VPS desligada as 03:00), e agora. Se o ultimo nao
// chegou na nuvem, tenta de novo backupRetry depois dele (retry), sem
// esperar o horario do dia seguinte. Backup que subiu sem algum database
// nao repete: o database quebrado falharia de novo, e cada tentativa sobe
// um arquivo inteiro.
func nextBackup(b config.Backup, last *BackupRun, now time.Time) (next time.Time, retry bool) {
	at, ok := b.At(now)
	if !ok {
		return time.Time{}, false
	}
	switch {
	case now.Before(at):
		next = at
	case last == nil || last.At.Before(at):
		next = now
	default:
		next = at.AddDate(0, 0, 1)
	}
	if last != nil && last.Error != "" {
		if r := last.At.Add(backupRetry); r.Before(next) {
			return later(r, now), true
		}
	}
	return next, false
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// SetBackupProvider troca o destino dos backups (google ou onedrive). O
// client e o login de cada um ficam guardados: voltar pro outro nao pede
// login de novo. Um login pendente e cancelado (era do outro destino).
func (c *Core) SetBackupProvider(ctx context.Context, p string) error {
	if !slices.Contains(backup.Providers, p) {
		return fmt.Errorf("destino %q: use %s", p, strings.Join(backup.Providers, " ou "))
	}
	if p == c.backupProvider() {
		return nil
	}
	c.CancelDriveLogin(ctx)
	err := c.changeConfig(func(cfg *config.Config) {
		cfg.Backup.Provider = p
		if p == backup.ProviderGoogle {
			cfg.Backup.Provider = "" // o padrao
		}
	})
	if err != nil {
		return err
	}
	_, err = c.RefreshBackup(ctx)
	return err
}

// StartDriveLogin comeca o login no destino atual pelo fluxo de
// dispositivo: o codigo aparece em State.Backup.Login e o daemon espera a
// pessoa liberar (google.com/device ou microsoft.com/link). O resultado
// chega pelo estado.
func (c *Core) StartDriveLogin(ctx context.Context) (DriveLogin, error) {
	p := c.backupProvider()
	name := backup.ProviderName(p)
	cfg, err := c.oauthConfig(p)
	if err != nil {
		return DriveLogin{}, err
	}
	da, err := cfg.DeviceAuth(ctx)
	if err != nil {
		return DriveLogin{}, fmt.Errorf("pedindo o codigo pro login no %s: %w", name, err)
	}
	login := DriveLogin{Code: da.UserCode, URL: da.VerificationURI, Expires: da.Expiry}
	if login.Expires.IsZero() {
		login.Expires = c.deps.Now().Add(30 * time.Minute)
	}

	c.backup.loginMu.Lock()
	if c.backup.cancelLn != nil {
		c.backup.cancelLn() // um login por vez: o novo codigo vale
	}
	lctx, cancel := context.WithDeadline(context.Background(), login.Expires)
	c.backup.cancelLn = cancel
	c.backup.loginMu.Unlock()

	c.update(func(s *State) { s.Backup.Login, s.Backup.LoginError = &login, "" })
	go func() {
		defer cancel()
		tok, err := cfg.DeviceAccessToken(lctx, da)
		if errors.Is(lctx.Err(), context.Canceled) {
			return // outro login comecou ou foi cancelado
		}
		msg := ""
		if err == nil {
			err = c.saveDriveLogin(lctx, p, tok)
		}
		if err != nil {
			msg = "login no " + name + ": " + err.Error()
			if errors.Is(err, context.DeadlineExceeded) {
				msg = "o codigo expirou sem login; peca outro"
			}
		}
		c.update(func(s *State) { s.Backup.Login, s.Backup.LoginError = nil, msg })
		c.RefreshBackup(context.Background())
	}()
	return login, nil
}

func (c *Core) saveDriveLogin(ctx context.Context, p string, tok *oauth2.Token) error {
	if tok.RefreshToken == "" {
		return fmt.Errorf("o %s nao devolveu o refresh token", backup.ProviderName(p))
	}
	tokenSecret := loginSecret(p)
	data, _ := json.Marshal(tok)
	if err := c.secretStore().Put(tokenSecret, data); err != nil {
		return err
	}
	email := ""
	if s, err := c.store(p); err == nil {
		email, _ = s.Email(ctx)
	}
	return c.changeBackupDisk(func(d *backupDisk) {
		folder, old := d.at(p)
		if *old != email {
			*folder = "" // outra conta: a pasta da anterior nao serve
		}
		*old = email
	})
}

// CancelDriveLogin desiste do login pendente.
func (c *Core) CancelDriveLogin(context.Context) error {
	c.backup.loginMu.Lock()
	if c.backup.cancelLn != nil {
		c.backup.cancelLn()
		c.backup.cancelLn = nil
	}
	c.backup.loginMu.Unlock()
	c.update(func(s *State) { s.Backup.Login, s.Backup.LoginError = nil, "" })
	return nil
}

// DisconnectDrive esquece o login no destino atual (os backups ficam la).
func (c *Core) DisconnectDrive(ctx context.Context) error {
	p := c.backupProvider()
	tokenSecret := loginSecret(p)
	if err := c.secretStore().Delete(tokenSecret); err != nil && !errors.Is(err, secrets.ErrNotFound) {
		return err
	}
	if err := c.changeBackupDisk(func(d *backupDisk) {
		folder, email := d.at(p)
		*folder, *email = "", ""
	}); err != nil {
		return err
	}
	_, err := c.RefreshBackup(ctx)
	return err
}

// NewBackupKey gera o par de chaves do backup: grava a publica na config e
// devolve a privada, que nao fica guardada em lugar nenhum da VPS. Os
// backups feitos com a chave anterior so abrem com a privada anterior.
func (c *Core) NewBackupKey(ctx context.Context) (string, error) {
	pub, priv, err := backup.NewKey()
	if err != nil {
		return "", err
	}
	if err := c.changeConfig(func(cfg *config.Config) { cfg.Backup.Recipient = pub }); err != nil {
		return "", err
	}
	c.RefreshBackup(ctx)
	return priv, nil
}

// SetBackupSchedule troca o horario diario (HH:MM; vazio desliga o
// automatico) e a retencao em dias.
func (c *Core) SetBackupSchedule(ctx context.Context, at string, days int) error {
	err := c.changeConfig(func(cfg *config.Config) {
		cfg.Backup.Time, cfg.Backup.RetentionDays = strings.TrimSpace(at), days
	})
	if err != nil {
		return err
	}
	_, err = c.RefreshBackup(ctx)
	return err
}

func (c *Core) changeConfig(fn func(*config.Config)) error {
	cfg, err := config.Load(c.deps.ConfigPath)
	if err != nil {
		return err
	}
	fn(&cfg)
	if err := cfg.Validate(); err != nil {
		return err
	}
	return config.Save(c.deps.ConfigPath, cfg)
}
