package diskspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestReserveAndFullDiskClassification(t *testing.T) {
	for _, test := range []struct {
		available, reserve uint64
		wait               bool
	}{
		{MinFreeBytes - 1, 0, true}, {MinFreeBytes, 0, false},
		{MinFreeBytes + 9, 10, true}, {MinFreeBytes + 10, 10, false},
		{^uint64(0), ^uint64(0), true},
	} {
		if got := IsWaiting(Check(test.available, test.reserve)); got != test.wait {
			t.Fatalf("space=%d reserve=%d: waiting=%v", test.available, test.reserve, got)
		}
	}
	if !IsWaiting(fmt.Errorf("copy: %w", syscall.ENOSPC)) {
		t.Fatal("wrapped write failure did not wait")
	}
	for _, message := range []string{"No space left on device", "Disk quota exceeded", "There is not enough space on the disk"} {
		if !IsWaiting(FFmpegError(message)) {
			t.Fatalf("FFmpeg disk-full error was lost: %s", message)
		}
	}
	if FFmpegError("invalid video data") != nil {
		t.Fatal("invalid media classified as disk-full")
	}
}

func TestTargetDiskAndWatchCancellation(t *testing.T) {
	dir := t.TempDir()
	if available, err := Available(filepath.Join(dir, "new", "series", "video.mp4")); err != nil || available == 0 {
		t.Fatal("nonexistent output failed to resolve its parent volume", available, err)
	}
	file := filepath.Join(dir, "file.mp4")
	if err := os.WriteFile(file, []byte("media"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Available(file); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := Watch(ctx, dir, cancel)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("normal cancellation reported space failure", err)
		}
	case <-time.After(time.Second):
		t.Fatal("disk watcher outlived its canceled operation")
	}
}
