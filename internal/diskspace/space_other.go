//go:build !linux && !darwin && !windows

package diskspace

import "errors"

func platformFull(error) bool { return false }

func availableBytes(string) (uint64, error) {
	return 0, errors.New("当前系统不支持磁盘空间查询")
}
