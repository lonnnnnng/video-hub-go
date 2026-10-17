package hub

import (
	"errors"
	"movie-hub-go/internal/diskspace"
	"os"
	"path/filepath"
	"time"
)

func (s *Service) requireSpace(path string, reserve uint64) error {
	if s.spaceCheck != nil {
		return s.spaceCheck(path, reserve)
	}
	return diskspace.Require(path, reserve)
}

func discardSpaceAttempt(job Job, staging string) error {
	_, temporary, err := jobFileNames(job)
	if err != nil {
		return err
	}
	name, err := filepath.Rel(job.MediaDir, staging)
	if err != nil || !filepath.IsLocal(name) || filepath.Dir(name) != temporary {
		return errors.New("空间等待的临时目录不属于本次任务，未清理")
	}
	root, err := os.OpenRoot(job.MediaDir)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := checkJobFile(root, name); err != nil {
		return err
	}
	// long: 空间等待会重新解析源片单；先回收本次未发布的分片，避免每轮重试重复占盘。历史尝试和正式文件不在此范围内。
	return root.RemoveAll(name)
}

func (s *Service) finishAttemptError(job Job, staging string, err error) {
	if diskspace.IsWaiting(err) {
		if cleanupErr := discardSpaceAttempt(job, staging); cleanupErr != nil {
			err = errors.New("空间等待的临时文件清理失败，请检查目录权限后重试")
		}
	}
	s.finishError(job, err)
}

func (s *Service) restoreSpaceWaitLocked() bool {
	for id, message := range s.spaceWaiting {
		// long: 磁盘满可能同时阻止状态写入；worker 已退出时保留内存待办，调度器在存储恢复后补写，避免任务卡在 downloading。
		if err := s.Store.Update(func(st *State) error {
			for i := range st.Jobs {
				job := &st.Jobs[i]
				if job.ID != id || job.Status != "downloading" && job.Status != "queued" {
					continue
				}
				if job.Status == "downloading" && job.Attempts > 0 {
					job.Attempts--
				}
				job.Status, job.WaitingForSpace, job.Error = "queued", true, message
				job.NextAttempt = time.Now()
			}
			return nil
		}); err != nil {
			return false
		}
		delete(s.spaceWaiting, id)
	}
	return true
}
