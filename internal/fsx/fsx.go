// Package fsx tem utilitarios de disco usados por quem grava estado do cais.
package fsx

import (
	"os"
	"path/filepath"
)

// WriteAtomic grava data em path de forma atomica: escreve num temporario
// da mesma pasta, faz fsync e renomeia por cima. Quem le nunca ve o arquivo
// pela metade, e uma queda no meio deixa o arquivo antigo intacto. A pasta
// precisa existir.
func WriteAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op depois do rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
