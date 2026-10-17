package diskspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const MinFreeBytes uint64 = 2 << 30

type WaitingError struct {
	Available, Required uint64
	Reason              string
}

func (e *WaitingError) Error() string {
	if e.Reason != "" {
		return "等待磁盘空间：" + e.Reason
	}
	return fmt.Sprintf("等待磁盘空间：可用 %.2f GiB，需要至少 %.2f GiB；空间恢复后自动继续", float64(e.Available)/(1<<30), float64(e.Required)/(1<<30))
}

func IsWaiting(err error) bool {
	var waiting *WaitingError
	return errors.As(err, &waiting) || errors.Is(err, syscall.ENOSPC) || platformFull(err)
}

func FFmpegError(stderr string) error {
	text := strings.ToLower(stderr)
	// long: FFmpeg 可能在定时检查前就写满磁盘并退出；将它的明确写盘错误保留为空间等待，不能耗尽普通重试次数。
	for _, message := range []string{"no space left on device", "disk quota exceeded", "not enough space on the disk"} {
		if strings.Contains(text, message) {
			return &WaitingError{Reason: "FFmpeg 写入空间不足，空间恢复后自动继续"}
		}
	}
	return nil
}

func Check(available, reserve uint64) error {
	if reserve > ^uint64(0)-MinFreeBytes {
		return &WaitingError{Available: available, Required: ^uint64(0)}
	}
	if available < MinFreeBytes+reserve {
		return &WaitingError{Available: available, Required: MinFreeBytes + reserve}
	}
	return nil
}

func Available(path string) (uint64, error) {
	if path == "" {
		path = "."
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return 0, err
	}
	// long: 新剧目录尚未创建时仍需检查目标盘；沿父目录找已存在的目录，不能误查程序所在磁盘。
	for {
		info, err := os.Stat(absolute)
		if err == nil {
			if !info.IsDir() {
				absolute = filepath.Dir(absolute)
			}
			return availableBytes(absolute)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
		parent := filepath.Dir(absolute)
		if parent == absolute {
			return 0, err
		}
		absolute = parent
	}
}

func Require(path string, reserve uint64) error {
	available, err := Available(path)
	if err != nil {
		return &WaitingError{Reason: "无法查询目标磁盘，请检查目录和挂载"}
	}
	return Check(available, reserve)
}

type Reader struct {
	Reader  io.Reader
	Path    string
	checked uint64
}

func (r *Reader) Read(p []byte) (int, error) {
	// long: 复制期间其他任务也会占盘，每 16 MiB 重新核对安全余量，避免只在开始时检查。
	if r.checked == 0 || r.checked >= 16<<20 {
		if err := Require(r.Path, 0); err != nil {
			return 0, err
		}
		r.checked = 0
	}
	n, err := r.Reader.Read(p)
	r.checked += uint64(n)
	return n, err
}

func Watch(ctx context.Context, path string, cancel context.CancelFunc) <-chan error {
	result := make(chan error, 1)
	go func() {
		defer close(result)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := Require(path, 0); err != nil {
					result <- err
					cancel()
					return
				}
			}
		}
	}()
	return result
}
