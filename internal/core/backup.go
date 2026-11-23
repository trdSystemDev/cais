package core

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/backup"
	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/databases"
	"github.com/trdSystemDev/cais/internal/fsx"
	"github.com/trdSystemDev/cais/internal/secrets"
)

// Secrets do backup: o OAuth client que a pessoa cria no Google Cloud e o
// token do login no Drive (o refresh token renova o acesso sozinho).
const (
	secretGoogleClient = "google_oauth_client"
	secretDriveToken   = "google_drive_token"
)

// BackupStatus e o backup no estado: o que esta configurado, o login no
// Drive e o ultimo backup. Nunca leva segredo (nem o client secret nem o
// token; a chave publica nao e segredo).
type BackupStatus struct {
	// ClientID e o OAuth client informado (vazio: falta criar no Google).
	ClientID string `json:"client_id,omitempty"`
	// Email e a conta do Drive conectada (vazio: sem login).
	Email string `json:"email,omitempty"`
	// Login e o login no Drive esperando a pessoa digitar o codigo.
	Login      *DriveLogin `json:"login,omitempty"`
	LoginError string      `json:"login_error,omitempty"`
	// Recipient e a chave publica que criptografa os backups.
	Recipient     string `json:"recipient,omitempty"`
	Time          string `json:"time,omitempty"`
	RetentionDays int    `json:"retention_days"`
	// Next e o proximo backup automatico (zero: desligado ou incompleto).
	Next time.Time `json:"next,omitzero"`
	// Retry diz que o Next e a nova tentativa de um backup que nao chegou
	// no Drive (e nao o horario do dia).
	Retry   bool       `json:"retry,omitempty"`
	Running bool       `json:"running,omitempty"`
	Step    string     `json:"step,omitempty"`
	Last    *BackupRun `json:"last,omitempty"`
	Error   string     `json:"error,omitempty"`
}

// Ready diz se o backup tem tudo pra rodar: client, login e chave.
func (b BackupStatus) Ready() bool { return b.ClientID != "" && b.Email != "" && b.Recipient != "" }

// DriveLogin e o codigo que a pessoa digita em URL pra liberar o Drive.
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
	// File e o nome no Drive; Size em bytes (criptografado e compactado).
	File      string `json:"file,omitempty"`
	Size      int64  `json:"size,omitempty"`
	Databases int    `json:"databases"`
	Files     int    `json:"files"`
	// Errors sao os databases que nao entraram (o resto entrou).
	Errors []string `json:"errors,omitempty"`
	// Error e o que impediu o backup de chegar no Drive.
	Error string `json:"error,omitempty"`
	// Local e o arquivo que ficou na VPS porque o envio falhou.
	Local string `json:"local,omitempty"`
	// Removed sao os backups velhos apagados do Drive pela retencao.
	Removed int `json:"removed,omitempty"`
}

// OK diz se o backup chegou no Drive com todos os databases.
func (r BackupRun) OK() bool { return r.Error == "" && len(r.Errors) == 0 }

// backupDisk e o que o cais guarda do backup fora da config: a pasta no
// Drive (o cais cria e so ve ela), a conta e o ultimo backup.
type backupDisk struct {
	Folder string     `json:"folder,omitempty"`
	Email  string     `json:"email,omitempty"`
	Last   *BackupRun `json:"last,omitempty"`
}

// BackupGap e o que o backup nao cobre, de proposito por enquanto: aparece
// no painel do backup, no CLI, no card do Garage e nos buckets, pra ninguem
// achar que esta coberto.
const BackupGap = "Fora do backup: os arquivos do Garage (buckets S3) e os volumes dos apps. Perder a VPS (ou o disco) perde esses dados."

// uploadRetryDelay e a espera entre tentativas de envio pro Drive.
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

func (c *Core) backupClient() (backup.Client, error) {
	data, err := c.secretStore().Get(secretGoogleClient)
	if errors.Is(err, secrets.ErrNotFound) {
		return backup.Client{}, errors.New("falta o OAuth client do Google (Setup -> Backups)")
	}
	if err != nil {
		return backup.Client{}, err
	}
	var cl backup.Client
	if err := json.Unmarshal(data, &cl); err != nil || cl.ID == "" {
		return backup.Client{}, errors.New("OAuth client do Google invalido: informe de novo (Setup -> Backups)")
	}
	return cl, nil
}

// drive abre o Drive com o token do login.
func (c *Core) drive() (*backup.Drive, error) {
	cl, err := c.backupClient()
	if err != nil {
		return nil, err
	}
	data, err := c.secretStore().Get(secretDriveToken)
	if errors.Is(err, secrets.ErrNotFound) {
		return nil, errors.New("o Drive nao esta conectado (Setup -> Backups -> Conectar)")
	}
	if err != nil {
		return nil, err
	}
	var tok oauth2.Token
	if err := json.Unmarshal(data, &tok); err != nil {
		return nil, fmt.Errorf("login do Drive invalido, conecte de novo: %w", err)
	}
	// o oauth2 renova o acesso sozinho; o contexto e o do cliente HTTP,
	// que vive mais que uma chamada
	d := backup.NewDrive(context.Background(), cl.OAuth(c.deps.GoogleEndpoint), &tok)
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
	st.Recipient, st.Time, st.RetentionDays = b.Recipient, b.Time, b.Retention()
	if cl, err := c.backupClient(); err == nil {
		st.ClientID = cl.ID
	}
	disk := c.loadBackupDisk()
	if _, err := c.secretStore().Get(secretDriveToken); err == nil {
		st.Email = cmpOr(disk.Email, "(conta conectada)")
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

// backupRetry e a espera pra tentar de novo um backup que nao chegou no
// Drive (Drive fora do ar, rede, disco).
var backupRetry = time.Hour

// nextBackup e o proximo horario do backup automatico. Se o de hoje ja
// passou sem backup (VPS desligada as 03:00), e agora. Se o ultimo nao
// chegou no Drive, tenta de novo backupRetry depois dele (retry), sem
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

// SetBackupClient grava o OAuth client do Google (tipo "TVs e dispositivos
// de entrada limitada"). Trocar o client desfaz o login: o token e do
// client antigo.
func (c *Core) SetBackupClient(ctx context.Context, id, secret string) error {
	id, secret = strings.TrimSpace(id), strings.TrimSpace(secret)
	if !strings.HasSuffix(id, ".apps.googleusercontent.com") {
		return errors.New("client ID invalido: copie o ID do OAuth client (termina em .apps.googleusercontent.com)")
	}
	if secret == "" {
		return errors.New("informe o client secret")
	}
	if old, err := c.backupClient(); err != nil || old.ID != id {
		if err := c.secretStore().Delete(secretDriveToken); err != nil && !errors.Is(err, secrets.ErrNotFound) {
			return err
		}
	}
	data, _ := json.Marshal(backup.Client{ID: id, Secret: secret})
	if err := c.secretStore().Put(secretGoogleClient, data); err != nil {
		return err
	}
	_, err := c.RefreshBackup(ctx)
	return err
}

// StartDriveLogin comeca o login no Drive pelo fluxo de dispositivo: o
// codigo aparece em State.Backup.Login e o daemon espera a pessoa liberar
// em google.com/device. O resultado chega pelo estado.
func (c *Core) StartDriveLogin(ctx context.Context) (DriveLogin, error) {
	cl, err := c.backupClient()
	if err != nil {
		return DriveLogin{}, err
	}
	cfg := cl.OAuth(c.deps.GoogleEndpoint)
	da, err := cfg.DeviceAuth(ctx)
	if err != nil {
		return DriveLogin{}, fmt.Errorf("pedindo o codigo ao Google: %w", err)
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
			err = c.saveDriveLogin(lctx, tok)
		}
		if err != nil {
			msg = "login no Drive: " + err.Error()
			if errors.Is(err, context.DeadlineExceeded) {
				msg = "o codigo expirou sem login; peca outro"
			}
		}
		c.update(func(s *State) { s.Backup.Login, s.Backup.LoginError = nil, msg })
		c.RefreshBackup(context.Background())
	}()
	return login, nil
}

func (c *Core) saveDriveLogin(ctx context.Context, tok *oauth2.Token) error {
	if tok.RefreshToken == "" {
		return errors.New("o Google nao devolveu o refresh token")
	}
	data, _ := json.Marshal(tok)
	if err := c.secretStore().Put(secretDriveToken, data); err != nil {
		return err
	}
	email := ""
	if d, err := c.drive(); err == nil {
		email, _ = d.Email(ctx)
	}
	return c.changeBackupDisk(func(d *backupDisk) {
		if d.Email != email {
			d.Folder = "" // outra conta: a pasta da anterior nao serve
		}
		d.Email = email
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

// DisconnectDrive esquece o login no Drive (os backups ficam la).
func (c *Core) DisconnectDrive(ctx context.Context) error {
	if err := c.secretStore().Delete(secretDriveToken); err != nil && !errors.Is(err, secrets.ErrNotFound) {
		return err
	}
	if err := c.changeBackupDisk(func(d *backupDisk) { d.Email, d.Folder = "", "" }); err != nil {
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

// RunBackups faz o backup automatico no horario da config. Confere a cada
// minuto; um backup que passou da hora (VPS ou daemon fora do ar) sai assim
// que der. Para quando ctx acaba.
func (c *Core) RunBackups(ctx context.Context, every time.Duration) {
	c.RefreshBackup(ctx)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		st := c.readBackup()
		if st.Next.IsZero() || c.deps.Now().Before(st.Next) {
			c.update(func(s *State) { s.Backup.Next, s.Backup.Retry = st.Next, st.Retry })
			continue
		}
		// o backup nao ve o desligamento do daemon (o Close espera): um
		// backup cortado so voltaria amanha
		if !c.work.Begin() {
			return
		}
		run, err := c.runBackup(context.WithoutCancel(ctx), true)
		c.work.End()
		if err != nil && !errors.Is(err, ErrBusy) {
			log.Printf("backup automatico: %v", err)
		}
		if !errors.Is(err, ErrBusy) {
			c.notifyBackup(st.Last, run, err)
		}
	}
}

// RunBackup faz um backup agora: dumps dos bancos e o estado do cais,
// criptografados, pro Drive; depois apaga os que passaram da retencao.
func (c *Core) RunBackup(ctx context.Context) (BackupRun, error) {
	return c.runBackup(ctx, false)
}

func (c *Core) backupStep(step string) {
	c.update(func(s *State) { s.Backup.Step = step })
}

func (c *Core) runBackup(ctx context.Context, auto bool) (run BackupRun, err error) {
	if !c.backup.mu.TryLock() {
		return BackupRun{}, ErrBusy
	}
	defer c.backup.mu.Unlock()
	st := c.readBackup()
	if !st.Ready() {
		return BackupRun{}, errors.New("backup incompleto: falta " + missingBackup(st))
	}
	start := c.deps.Now()
	run = BackupRun{At: start, Auto: auto}
	c.update(func(s *State) { s.Backup.Running, s.Backup.Step = true, "comecando" })
	defer func() {
		run.Duration = c.deps.Now().Sub(start)
		if err != nil {
			run.Error = err.Error()
		}
		saved := run
		c.changeBackupDisk(func(d *backupDisk) { d.Last = &saved })
		c.update(func(s *State) { s.Backup.Running, s.Backup.Step = false, "" })
		c.RefreshBackup(context.Background())
	}()

	cfg, err := config.Load(c.deps.ConfigPath)
	if err != nil {
		return run, err
	}
	d, err := c.drive()
	if err != nil {
		return run, err
	}
	tmp := filepath.Join(c.deps.BackupsDir, "tmp")
	os.RemoveAll(tmp) // sobra de um backup interrompido
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return run, err
	}
	defer os.RemoveAll(tmp)

	// arquivos de um envio que falhou antes: so o ultimo fica
	c.removeLocalBackups()

	dbs, err := c.backupDBs()
	if err != nil {
		return run, err
	}
	run.Databases = len(dbs)
	run.File = backup.FileName(c.vpsName(), start)
	local := filepath.Join(c.deps.BackupsDir, run.File)
	f, err := os.OpenFile(local+".part", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return run, err
	}
	c.backupStep(fmt.Sprintf("copiando %d databases e o estado do cais", len(dbs)))
	res, err := backup.Make(ctx, backup.Job{
		Recipient: cfg.Backup.Recipient,
		DBs:       dbs,
		Dump:      c.dumpDB,
		Dirs:      c.stateDirs(),
		Tmp:       tmp,
		Now:       start,
	}, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	run.Errors, run.Files = res.Errors, len(res.Files)
	if err != nil {
		os.Remove(local + ".part")
		return run, err
	}
	if err := os.Rename(local+".part", local); err != nil {
		return run, err
	}
	if info, err := os.Stat(local); err == nil {
		run.Size = info.Size()
	}

	c.backupStep("enviando pro Google Drive")
	disk := c.loadBackupDisk()
	folder, err := d.Folder(ctx, disk.Folder, "cais-backups-"+c.vpsName())
	if err == nil && folder != disk.Folder {
		err = c.changeBackupDisk(func(d *backupDisk) { d.Folder = folder })
	}
	if err == nil {
		err = upload(ctx, d, folder, local, run.File)
	}
	if err != nil {
		run.Local = local
		return run, fmt.Errorf("enviando pro Drive (o arquivo ficou em %s): %w", local, err)
	}
	os.Remove(local)

	c.backupStep("apagando os backups velhos")
	files, err := d.List(ctx, folder)
	if err != nil {
		return run, fmt.Errorf("o backup subiu, mas a lista pra retencao falhou: %w", err)
	}
	for _, old := range backup.Expired(files, cfg.Backup.Retention(), c.deps.Now()) {
		if err := d.Delete(ctx, old.ID); err != nil {
			return run, fmt.Errorf("o backup subiu, mas apagar %s falhou: %w", old.Name, err)
		}
		run.Removed++
	}
	if len(run.Errors) > 0 {
		return run, fmt.Errorf("backup no Drive, mas sem %d databases: %s", len(run.Errors), strings.Join(run.Errors, "; "))
	}
	return run, nil
}

func missingBackup(st BackupStatus) string {
	var miss []string
	if st.ClientID == "" {
		miss = append(miss, "o OAuth client do Google")
	}
	if st.Email == "" {
		miss = append(miss, "conectar o Drive")
	}
	if st.Recipient == "" {
		miss = append(miss, "a chave do backup")
	}
	return strings.Join(miss, ", ")
}

// upload envia o arquivo, com ate 3 tentativas (rede instavel).
func upload(ctx context.Context, d *backup.Drive, folder, local, name string) error {
	var err error
	for i := 0; i < 3; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(uploadRetryDelay):
			}
		}
		var f *os.File
		if f, err = os.Open(local); err != nil {
			return err
		}
		info, _ := f.Stat()
		_, err = d.Put(ctx, folder, name, f, info.Size())
		f.Close()
		if err == nil {
			return nil
		}
	}
	return err
}

func (c *Core) removeLocalBackups() {
	entries, _ := os.ReadDir(c.deps.BackupsDir)
	for _, e := range entries {
		if backup.IsBackup(strings.TrimSuffix(e.Name(), ".part")) {
			os.Remove(filepath.Join(c.deps.BackupsDir, e.Name()))
		}
	}
}

// backupDBs sao os databases criados pelo cais.
func (c *Core) backupDBs() ([]backup.DB, error) {
	names, err := c.secretStore().List()
	if err != nil {
		return nil, err
	}
	var out []backup.DB
	for _, n := range names {
		for _, engine := range []string{databases.Postgres, databases.MySQL} {
			if name, ok := strings.CutPrefix(n, "db_"+engine+"_"); ok {
				out = append(out, backup.DB{Engine: engine, Name: name})
			}
		}
	}
	slices.SortFunc(out, func(a, b backup.DB) int { return strings.Compare(a.Engine+"/"+a.Name, b.Engine+"/"+b.Name) })
	return out, nil
}

func (c *Core) dumpDB(ctx context.Context, db backup.DB, w io.Writer) error {
	e, err := databases.Get(db.Engine)
	if err != nil {
		return err
	}
	cmd, err := databases.DumpCommand(e, db.Name)
	if err != nil {
		return err
	}
	return c.deps.ExecStream(ctx, e.Container, nil, w, cmd...)
}

// stateDirs e o estado do cais que entra no backup: a config e o
// /var/lib/cais sem o codigo dos apps (volta no proximo deploy) e sem os
// proprios backups.
func (c *Core) stateDirs() []backup.Dir {
	data := c.deps.DataDir
	skipBackups, _ := filepath.Rel(data, c.deps.BackupsDir)
	skip := func(rel string, d fs.DirEntry) bool {
		if rel == filepath.ToSlash(skipBackups) {
			return true
		}
		// apps/<app>/src, src.prev, src.next, src.failed e a marca da troca
		// de um deploy em andamento (restaurada, viraria uma recuperacao
		// falsa)
		parts := strings.Split(rel, "/")
		if len(parts) != 3 || parts[0] != "apps" {
			return false
		}
		return d.IsDir() && strings.HasPrefix(parts[2], "src") || !d.IsDir() && parts[2] == apps.SwapFile
	}
	return []backup.Dir{
		{Prefix: "state/etc", Path: filepath.Dir(c.deps.ConfigPath)},
		{Prefix: "state/data", Path: data, Skip: skip},
	}
}

// BackupFiles lista os backups no Drive, do mais novo pro mais velho.
func (c *Core) BackupFiles(ctx context.Context) ([]backup.File, error) {
	d, err := c.drive()
	if err != nil {
		return nil, err
	}
	folder := c.loadBackupDisk().Folder
	if folder == "" {
		return nil, nil
	}
	files, err := d.List(ctx, folder)
	if err != nil {
		return nil, err
	}
	var out []backup.File
	for i := len(files) - 1; i >= 0; i-- {
		if backup.IsBackup(files[i].Name) {
			out = append(out, files[i])
		}
	}
	return out, nil
}

// RestoreRequest pede a volta de databases de um backup do Drive.
type RestoreRequest struct {
	// File e o nome do backup no Drive (BackupFiles).
	File string `json:"file"`
	// Identity e a chave privada do backup (AGE-SECRET-KEY-...): so passa
	// pela memoria do daemon durante o restore.
	Identity string `json:"identity"`
	// Databases sao os que voltam (engine/nome); vazio so lista o que o
	// backup tem.
	Databases []string `json:"databases,omitempty"`
}

// RestoreResult e o que o backup tem e o que voltou.
type RestoreResult struct {
	Available []string `json:"available"`
	Restored  []string `json:"restored,omitempty"`
}

// RestoreBackup baixa o backup, abre com a chave e devolve cada database
// pedido ao estado do backup: o database atual e apagado (as conexoes
// abertas caem) e recriado com o mesmo usuario e senha antes do dump
// entrar. O database tem que existir no cais (a senha do usuario e a dele).
func (c *Core) RestoreBackup(ctx context.Context, req RestoreRequest) (RestoreResult, error) {
	if !c.backup.mu.TryLock() {
		return RestoreResult{}, ErrBusy
	}
	defer c.backup.mu.Unlock()
	if !backup.IsBackup(req.File) {
		return RestoreResult{}, fmt.Errorf("%q nao e um backup do cais", req.File)
	}
	want := map[string]bool{}
	for _, db := range req.Databases {
		engine, name, ok := strings.Cut(db, "/")
		if !ok {
			return RestoreResult{}, fmt.Errorf("database %q: use engine/nome (ex: postgres/loja)", db)
		}
		if _, _, err := c.dbCredentials(engine, name); err != nil {
			return RestoreResult{}, err
		}
		want[db] = true
	}
	d, err := c.drive()
	if err != nil {
		return RestoreResult{}, err
	}
	folder := c.loadBackupDisk().Folder
	files, err := d.List(ctx, folder)
	if err != nil {
		return RestoreResult{}, err
	}
	id := ""
	for _, f := range files {
		if f.Name == req.File {
			id = f.ID
		}
	}
	if id == "" {
		return RestoreResult{}, fmt.Errorf("backup %s nao encontrado no Drive", req.File)
	}
	tmp := filepath.Join(c.deps.BackupsDir, "restore")
	os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return RestoreResult{}, err
	}
	defer os.RemoveAll(tmp)
	f, err := os.Create(filepath.Join(tmp, req.File))
	if err != nil {
		return RestoreResult{}, err
	}
	defer f.Close()
	if err := d.Get(ctx, id, f); err != nil {
		return RestoreResult{}, fmt.Errorf("baixando %s: %w", req.File, err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return RestoreResult{}, err
	}
	r, err := backup.NewReader(f, req.Identity)
	if err != nil {
		return RestoreResult{}, err
	}
	defer r.Close()
	var res RestoreResult
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return res, fmt.Errorf("lendo o backup: %w", err)
		}
		db, ok := dumpName(h)
		if !ok {
			continue
		}
		res.Available = append(res.Available, db)
		if !want[db] {
			continue
		}
		if err := c.restoreDB(ctx, db, r); err != nil {
			return res, fmt.Errorf("%s: %w", db, err)
		}
		res.Restored = append(res.Restored, db)
		delete(want, db)
	}
	if len(want) > 0 {
		var miss []string
		for db := range want {
			miss = append(miss, db)
		}
		slices.Sort(miss)
		return res, fmt.Errorf("o backup nao tem %s", strings.Join(miss, ", "))
	}
	return res, nil
}

// dumpName e o database de uma entrada postgres/<db>.sql ou mysql/<db>.sql.
func dumpName(h *tar.Header) (string, bool) {
	dir, file := path.Split(h.Name)
	engine := strings.TrimSuffix(dir, "/")
	if h.Typeflag != tar.TypeReg || (engine != databases.Postgres && engine != databases.MySQL) || !strings.HasSuffix(file, ".sql") {
		return "", false
	}
	return engine + "/" + strings.TrimSuffix(file, ".sql"), true
}

func (c *Core) restoreDB(ctx context.Context, db string, dump io.Reader) error {
	engine, name, _ := strings.Cut(db, "/")
	_, pw, err := c.dbCredentials(engine, name)
	if err != nil {
		return err
	}
	e, _ := databases.Get(engine)
	if err := databases.Drop(ctx, c.deps.ExecInput, e, name); err != nil {
		return err
	}
	if err := databases.Provision(ctx, c.deps.ExecInput, e, name, []byte(pw), nil); err != nil {
		return err
	}
	cmd, err := databases.RestoreCommand(e, name)
	if err != nil {
		return err
	}
	return c.deps.ExecStream(ctx, e.Container, dump, io.Discard, cmd...)
}
