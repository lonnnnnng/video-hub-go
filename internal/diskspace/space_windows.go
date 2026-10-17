package diskspace

import (
	"errors"
	"runtime"
	"syscall"
	"unsafe"
)

var getDiskFreeSpaceEx = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")

func platformFull(err error) bool {
	// long: Windows 文件写入返回卷满或句柄空间不足的原生错误码，不能只识别 Unix 的 ENOSPC。
	return errors.Is(err, syscall.Errno(112)) || errors.Is(err, syscall.Errno(39))
}

func availableBytes(path string) (uint64, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var available uint64
	// long: 使用当前服务账号可用的字节数，Windows 配额下不能把整个卷的剩余空间误作可写额度。
	ok, _, callErr := getDiskFreeSpaceEx.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&available)), 0, 0)
	runtime.KeepAlive(name)
	if ok == 0 {
		return 0, callErr
	}
	return available, nil
}
