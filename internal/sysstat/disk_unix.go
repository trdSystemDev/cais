//go:build linux

package sysstat

import "syscall"

func statDisk(path string) (Disk, uint64, error) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err != nil {
		return Disk{}, 0, err
	}
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return Disk{}, 0, err
	}
	bs := uint64(fs.Bsize)
	total := fs.Blocks * bs
	// usado como o df: o que nao esta livre (os blocos reservados do root
	// contam como usados pra quem nao e root)
	used := total - fs.Bfree*bs
	return Disk{Path: path, Total: total, Used: used}, uint64(st.Dev), nil
}
