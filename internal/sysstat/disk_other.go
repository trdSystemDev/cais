//go:build !linux

package sysstat

import "errors"

func statDisk(path string) (Disk, uint64, error) {
	return Disk{}, 0, errors.New("uso do disco so no Linux")
}
