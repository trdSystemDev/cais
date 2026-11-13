package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"
)

// DB e um database a copiar.
type DB struct {
	Engine string // postgres ou mysql
	Name   string
}

// Dir e uma pasta do estado do cais que entra no backup em Prefix/.
type Dir struct {
	Prefix string
	Path   string
	Skip   func(rel string, d fs.DirEntry) bool
}

// Job e o que um backup copia.
type Job struct {
	Recipient string
	DBs       []DB
	// Dump escreve em w o dump SQL do database.
	Dump func(ctx context.Context, db DB, w io.Writer) error
	Dirs []Dir
	// Tmp e a pasta (do root) dos dumps temporarios.
	Tmp string
	Now time.Time
}

// Result e o que entrou no backup. Errors sao os databases que falharam:
// o resto entra mesmo assim (um banco quebrado nao deixa os outros sem
// backup).
type Result struct {
	Files  []string
	Errors []string
}

// Make escreve o backup criptografado em w. O erro devolvido e o que
// impede o arquivo de existir (chave, disco); dump que falha vai pra
// Result.Errors.
func Make(ctx context.Context, job Job, w io.Writer) (Result, error) {
	bw, err := NewWriter(w, job.Recipient, job.Tmp, job.Now)
	if err != nil {
		return Result{}, err
	}
	var res Result
	for _, db := range job.DBs {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		name := path.Join(db.Engine, db.Name+".sql")
		err := bw.AddStream(ctx, name, func(ctx context.Context, w io.Writer) error { return job.Dump(ctx, db, w) })
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s/%s: %v", db.Engine, db.Name, err))
		}
	}
	for _, d := range job.Dirs {
		if err := bw.AddDir(d.Prefix, d.Path, d.Skip); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return res, fmt.Errorf("copiando %s: %w", d.Path, err)
		}
	}
	res.Files = bw.Files()
	return res, bw.Close()
}

// FileName e o nome do arquivo do backup no Drive.
func FileName(host string, now time.Time) string {
	host = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.' {
			return r
		}
		return '-'
	}, strings.ToLower(host))
	return fmt.Sprintf("cais-%s-%s.tar.gz.age", host, now.Format("2006-01-02_1504"))
}

// IsBackup diz se o arquivo da pasta e um backup do cais (a retencao so
// apaga esses).
func IsBackup(name string) bool {
	return strings.HasPrefix(name, "cais-") && strings.HasSuffix(name, ".tar.gz.age")
}

// Expired sao os backups mais velhos que days dias. O mais novo nunca sai:
// com o backup parado ha semanas, apagar tudo deixaria o Drive vazio.
func Expired(files []File, days int, now time.Time) []File {
	if days <= 0 {
		return nil
	}
	var backups []File
	for _, f := range files {
		if IsBackup(f.Name) {
			backups = append(backups, f)
		}
	}
	newest := -1
	for i, f := range backups {
		if newest < 0 || f.Created.After(backups[newest].Created) {
			newest = i
		}
	}
	limit := now.AddDate(0, 0, -days)
	var out []File
	for i, f := range backups {
		if i != newest && f.Created.Before(limit) {
			out = append(out, f)
		}
	}
	return out
}
