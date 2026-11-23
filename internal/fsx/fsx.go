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
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Chmod(perm)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return Rename(tmp.Name(), path)
}

// Rename renomeia e faz fsync das pastas envolvidas. Sem isso o rename pode
// ainda nao estar no disco quando a maquina cai logo depois (ext4, xfs): o
// arquivo, ou a pasta do app na troca do deploy, volta pro estado antigo.
func Rename(oldpath, newpath string) error {
	if err := os.Rename(oldpath, newpath); err != nil {
		return err
	}
	if err := SyncDir(filepath.Dir(newpath)); err != nil {
		return err
	}
	if od := filepath.Dir(oldpath); od != filepath.Dir(newpath) {
		return SyncDir(od)
	}
	return nil
}

// Remove apaga o arquivo e faz fsync da pasta, pra remocao sobreviver a uma
// queda logo depois.
func Remove(path string) error {
	if err := os.Remove(path); err != nil {
		return err
	}
	return SyncDir(filepath.Dir(path))
}

// SyncDir grava no disco as entradas da pasta (criacao, rename, remocao).
func SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}
