package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
)

// Writer monta o arquivo do backup: tar -> gzip -> age -> w.
type Writer struct {
	enc  io.WriteCloser
	gz   *gzip.Writer
	tw   *tar.Writer
	tmp  string // pasta dos temporarios (dumps antes de entrar no tar)
	now  time.Time
	list []string
}

// NewWriter comeca um backup criptografado pra recipient (chave publica
// age1...). tmp e uma pasta so do root pros dumps temporarios.
func NewWriter(w io.Writer, recipient, tmp string, now time.Time) (*Writer, error) {
	r, err := age.ParseX25519Recipient(strings.TrimSpace(recipient))
	if err != nil {
		return nil, fmt.Errorf("chave publica do backup invalida: %w", err)
	}
	enc, err := age.Encrypt(w, r)
	if err != nil {
		return nil, err
	}
	gz := gzip.NewWriter(enc)
	return &Writer{enc: enc, gz: gz, tw: tar.NewWriter(gz), tmp: tmp, now: now}, nil
}

// Files sao os arquivos ja gravados, na ordem.
func (b *Writer) Files() []string { return b.list }

// AddStream grava em name o que produce escrever (um dump). O conteudo
// passa por um temporario em tmp: o tar precisa do tamanho antes.
func (b *Writer) AddStream(ctx context.Context, name string, produce func(ctx context.Context, w io.Writer) error) error {
	f, err := os.CreateTemp(b.tmp, "dump-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := produce(ctx, f); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	size, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := b.tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: size, ModTime: b.now, Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	if _, err := io.Copy(b.tw, f); err != nil {
		return err
	}
	b.list = append(b.list, name)
	return nil
}

// AddDir grava a pasta dir em prefix/, com as permissoes. skip (caminho
// relativo com /) pula arquivos e pastas inteiras. Symlinks entram como
// symlinks; o resto (socket, device) fica de fora.
func (b *Writer) AddDir(prefix, dir string, skip func(rel string, d fs.DirEntry) bool) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel != "." && skip != nil && skip(rel, d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		name := path.Join(prefix, rel)
		var link string
		switch {
		case d.IsDir():
			name += "/"
		case info.Mode()&fs.ModeSymlink != 0:
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		case !info.Mode().IsRegular():
			return nil
		}
		h, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		h.Name, h.Uname, h.Gname = name, "", ""
		if err := b.tw.WriteHeader(h); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			_, err = io.Copy(b.tw, f)
			f.Close()
			if err != nil {
				return err
			}
			b.list = append(b.list, name)
		}
		return nil
	})
}

// Close fecha o tar, o gzip e a criptografia. Sem Close o arquivo nao abre.
func (b *Writer) Close() error {
	return errors.Join(b.tw.Close(), b.gz.Close(), b.enc.Close())
}

// Reader le um backup: age -> gzip -> tar.
type Reader struct {
	*tar.Reader
	gz *gzip.Reader
}

// NewReader abre um backup com a chave privada (AGE-SECRET-KEY-...).
func NewReader(r io.Reader, identity string) (*Reader, error) {
	ids, err := age.ParseIdentities(strings.NewReader(identity))
	if err != nil {
		return nil, fmt.Errorf("chave privada do backup invalida: %w", err)
	}
	dec, err := age.Decrypt(r, ids...)
	if err != nil {
		return nil, fmt.Errorf("abrindo o backup (a chave e a certa?): %w", err)
	}
	gz, err := gzip.NewReader(dec)
	if err != nil {
		return nil, err
	}
	return &Reader{Reader: tar.NewReader(gz), gz: gz}, nil
}

func (r *Reader) Close() error { return r.gz.Close() }

// NewKey gera um par de chaves: a publica fica na VPS (config), a privada
// e mostrada uma vez pra pessoa guardar.
func NewKey() (public, private string, err error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return "", "", err
	}
	return id.Recipient().String(), id.String(), nil
}

// CheckRecipient confere uma chave publica age1...
func CheckRecipient(s string) error {
	_, err := age.ParseX25519Recipient(strings.TrimSpace(s))
	return err
}
