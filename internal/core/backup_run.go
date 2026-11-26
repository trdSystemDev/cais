package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/backup"
	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/databases"
	"github.com/trdSystemDev/cais/internal/fsx"
)

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
		if !errors.Is(err, ErrBusy) {
			c.notifyBackup(st.Last, run, err)
		}
	}
}

// RunBackup faz um backup agora: dumps dos bancos e o estado do cais,
// criptografados, pra nuvem (Google Drive ou OneDrive); depois apaga os que
// passaram da retencao.
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
	p, name := st.Provider, backup.ProviderName(st.Provider)
	run = BackupRun{At: start, Auto: auto, Provider: p}
	c.update(func(s *State) { s.Backup.Running, s.Backup.Step = true, "comecando" })
	l := c.deps.Logger.With("acao", "backup", "automatico", auto, "destino", p)
	l.Info("backup comecou")
	began := time.Now()
	defer func() {
		run.Duration = c.deps.Now().Sub(start)
		fields := []any{"duracao", time.Since(began).Round(time.Millisecond), "databases", run.Databases, "arquivos", run.Files}
		if err != nil {
			run.Error = err.Error()
			l.Error("backup falhou", append(fields, "erro", err)...)
		} else {
			l.Info("backup terminou", append(fields, "arquivo", run.File, "tamanho", run.Size, "databases_com_erro", len(run.Errors))...)
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
	d, err := c.store(p)
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
	if err := fsx.Rename(local+".part", local); err != nil {
		return run, err
	}
	if info, err := os.Stat(local); err == nil {
		run.Size = info.Size()
	}

	c.backupStep("enviando pro " + name)
	disk := c.loadBackupDisk()
	saved, _ := disk.at(p)
	folder, err := d.Folder(ctx, *saved, "cais-backups-"+c.vpsName())
	if err == nil && folder != *saved {
		err = c.changeBackupDisk(func(d *backupDisk) { f, _ := d.at(p); *f = folder })
	}
	if err == nil {
		err = upload(ctx, d, folder, local, run.File)
	}
	if err != nil {
		run.Local = local
		return run, fmt.Errorf("enviando pro %s (o arquivo ficou em %s): %w", name, local, err)
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
		return run, fmt.Errorf("backup no %s, mas sem %d databases: %s", name, len(run.Errors), strings.Join(run.Errors, "; "))
	}
	return run, nil
}

func missingBackup(st BackupStatus) string {
	var miss []string
	if st.Email == "" {
		miss = append(miss, "conectar o "+backup.ProviderName(st.Provider))
	}
	if st.Recipient == "" {
		miss = append(miss, "a chave do backup")
	}
	return strings.Join(miss, ", ")
}

// upload envia o arquivo, com ate 3 tentativas (rede instavel).
func upload(ctx context.Context, d backup.Store, folder, local, name string) error {
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

// BackupFiles lista os backups no destino atual, do mais novo pro mais
// velho.
func (c *Core) BackupFiles(ctx context.Context) ([]backup.File, error) {
	p := c.backupProvider()
	d, err := c.store(p)
	if err != nil {
		return nil, err
	}
	disk := c.loadBackupDisk()
	folder, _ := disk.at(p)
	if *folder == "" {
		return nil, nil
	}
	files, err := d.List(ctx, *folder)
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
