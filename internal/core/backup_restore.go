package core

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/trdSystemDev/cais/internal/backup"
	"github.com/trdSystemDev/cais/internal/databases"
)

// RestoreRequest pede a volta de databases de um backup na nuvem.
type RestoreRequest struct {
	// File e o nome do backup no destino atual (BackupFiles).
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
	if len(req.Databases) == 0 { // so lista o que o backup tem: nada muda
		return c.restoreBackup(ctx, req)
	}
	// a chave (req.Identity) nunca vai pro log
	l := c.deps.Logger.With("acao", "restore", "arquivo", req.File, "databases", strings.Join(req.Databases, ","))
	l.Warn("restore comecou: os databases pedidos serao substituidos")
	began := time.Now()
	res, err := c.restoreBackup(ctx, req)
	took := time.Since(began).Round(time.Millisecond)
	if err != nil {
		l.Error("restore falhou", "duracao", took, "restaurados", strings.Join(res.Restored, ","), "erro", err)
	} else {
		l.Info("restore terminou", "duracao", took, "restaurados", strings.Join(res.Restored, ","))
	}
	return res, err
}

func (c *Core) restoreBackup(ctx context.Context, req RestoreRequest) (RestoreResult, error) {
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
	p := c.backupProvider()
	d, err := c.store(p)
	if err != nil {
		return RestoreResult{}, err
	}
	disk := c.loadBackupDisk()
	folder, _ := disk.at(p)
	files, err := d.List(ctx, *folder)
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
		return RestoreResult{}, fmt.Errorf("backup %s nao encontrado no %s", req.File, backup.ProviderName(p))
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
