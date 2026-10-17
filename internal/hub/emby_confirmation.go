package hub

import (
	"context"
	"log/slog"
	"movie-hub-go/internal/embycheck"
	"path/filepath"
	"time"
)

func (s *Service) confirmEmbyMedia(ctx context.Context) {
	if !s.embyMu.TryLock() {
		return
	}
	defer s.embyMu.Unlock()
	state := s.Store.Snapshot()
	if !state.Settings.EmbyEnabled || state.Settings.EmbyURL == "" || state.Settings.EmbyKey == "" || state.Emby.Pending {
		return
	}
	targets := []embycheck.Target{}
	for _, job := range state.Jobs {
		if job.Status != "completed" || job.EmbyMedia.NextCheckAt.After(time.Now()) {
			continue
		}
		suffix := filepath.Join(filepath.Base(filepath.Dir(filepath.Dir(job.Output))), filepath.Base(filepath.Dir(job.Output)), filepath.Base(job.Output))
		targets = append(targets, embycheck.Target{ID: job.ID, LocalPath: job.Output, Suffix: suffix, Previous: job.EmbyMedia})
	}
	if len(targets) == 0 {
		return
	}
	results, _ := embycheck.Check(ctx, state.Settings.EmbyURL, state.Settings.EmbyKey, targets)
	if ctx.Err() != nil {
		return
	}
	if err := s.Store.Update(func(current *State) error {
		// long: 新下载、换服务器或修改凭据产生新一轮扫描后，旧查询不能把新媒体误记为已识别。
		if current.Emby.Revision != state.Emby.Revision {
			return nil
		}
		for i := range current.Jobs {
			job := &current.Jobs[i]
			if result, ok := results[job.ID]; ok && job.Status == "completed" {
				for _, target := range targets {
					if target.ID == job.ID && target.LocalPath == job.Output {
						job.EmbyMedia = result
						break
					}
				}
			}
		}
		return nil
	}); err != nil {
		slog.Warn("Emby 入库确认状态保存失败")
	}
}
