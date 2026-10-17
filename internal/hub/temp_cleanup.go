package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"
)

type cleanupReader struct {
	ctx context.Context
	io.Reader
}

func (r cleanupReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func cleanupJobTemporary(ctx context.Context, job Job) (bool, error) {
	if job.Status != "completed" {
		return false, nil
	}
	media, temporary, err := jobFileNames(job)
	if err != nil {
		return false, err
	}
	root, err := os.OpenRoot(job.MediaDir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, errors.New("无法打开临时分片所在目录")
	}
	defer root.Close()
	if err := checkJobFile(root, temporary); err != nil {
		return false, err
	}
	info, err := root.Lstat(temporary)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !info.IsDir() {
		return false, errors.New("临时分片路径不是普通目录，未清理")
	}
	for _, name := range []string{media, media + ".moviehub.json", strings.TrimSuffix(media, ".mp4") + ".nfo"} {
		if err := checkJobFile(root, name); err != nil {
			return false, err
		}
		info, err := root.Lstat(name)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || name == media+".moviehub.json" && info.Size() > 4096 {
			return false, errors.New("正式 MP4、NFO 或收据未完整发布，临时分片已保留")
		}
	}
	proof, err := root.ReadFile(media + ".moviehub.json")
	var owner receipt
	if err != nil || json.Unmarshal(proof, &owner) != nil || owner.JobID != job.ID || len(owner.SHA256) != sha256.Size*2 {
		return false, errors.New("正式 MP4 缺少匹配的任务收据，临时分片已保留")
	}
	file, err := root.Open(media)
	if err != nil {
		return false, errors.New("正式 MP4 不可读取，临时分片已保留")
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() <= 0 {
		return false, errors.New("正式 MP4 无效，临时分片已保留")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, cleanupReader{ctx, file}); err != nil {
		return false, err
	}
	after, err := root.Stat(media)
	// long: 完成状态不能证明后来被修改的 MP4 仍完整；核对收据及读取期间的文件身份后，才回收可能用于恢复的分片。
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || hex.EncodeToString(hash.Sum(nil)) != owner.SHA256 {
		return false, errors.New("正式 MP4 已变化或校验不匹配，临时分片已保留")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	// long: 只移除受管理任务的隐藏临时目录；硬链接的正式 MP4、NFO、海报和任务记录均保留。
	if err := root.RemoveAll(temporary); err != nil {
		return false, errors.New("临时分片清理失败，将自动重试")
	}
	return true, nil
}

func (s *Service) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		s.cleanupCompletedTemporary(s.ctx)
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		case <-s.cleanupWake:
		}
	}
}

func (s *Service) cleanupCompletedTemporary(ctx context.Context) {
	for _, job := range s.Store.Snapshot().Jobs {
		if ctx.Err() != nil {
			return
		}
		if job.Status != "completed" {
			continue
		}
		s.mu.Lock()
		if s.deleting[job.ID] || s.cleaning[job.ID] || s.workers[job.ID] != nil {
			s.mu.Unlock()
			continue
		}
		s.cleaning[job.ID] = true
		s.mu.Unlock()
		removed, err := cleanupJobTemporary(ctx, job)
		s.mu.Lock()
		delete(s.cleaning, job.ID)
		s.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		message := ""
		if err != nil {
			message = err.Error()
		}
		if removed || message != job.CleanupError {
			if saveErr := s.Store.Update(func(st *State) error {
				for i := range st.Jobs {
					if st.Jobs[i].ID == job.ID && st.Jobs[i].Status == "completed" && st.Jobs[i].Output == job.Output {
						st.Jobs[i].CleanupError = message
					}
				}
				return nil
			}); saveErr != nil {
				slog.Warn("临时分片清理状态保存失败", "job_id", job.ID)
			}
		}
		if removed {
			slog.Info("已完成任务临时分片已清理", "job_id", job.ID)
		}
	}
}
