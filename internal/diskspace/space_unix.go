//go:build linux || darwin

package diskspace

import (
	"errors"
	"syscall"
)

func platformFull(err error) bool { return errors.Is(err, syscall.EDQUOT) }

func availableBytes(path string) (uint64, error) {
	var info syscall.Statfs_t
	if err := syscall.Statfs(path, &info); err != nil {
		return 0, err
	}
	return uint64(info.Bavail) * uint64(info.Bsize), nil
}
