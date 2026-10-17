package hub

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"
)

type JobReconfigure struct {
	LineID            string    `json:"lineId"`
	EpisodeKey        string    `json:"episodeKey"`
	Season            int       `json:"season"`
	Retry             bool      `json:"retry"`
	ExpectedUpdatedAt time.Time `json:"expectedUpdatedAt"`
}

func (s *Service) editableJobLocked(id string) (Job, error) {
	if s.active[id] != nil || s.workers[id] != nil || s.deleting[id] || s.cleaning[id] {
		return Job{}, errors.New("任务仍在运行或整理，请暂停并等待结束后再调整")
	}
	for _, job := range s.Store.Snapshot().Jobs {
		if job.ID == id {
			if job.Status != "queued" && job.Status != "paused" && job.Status != "failed" && job.Status != "canceled" {
				return Job{}, errors.New("只有尚未发布的等待、暂停或失败任务可以调整线路和季号")
			}
			return job, nil
		}
	}
	return Job{}, errors.New("任务不存在")
}

func requireUnpublishedJob(job Job) error {
	if job.Output != "" {
		return errors.New("任务已发布媒体，不能直接修改归档信息；现有文件已保留")
	}
	media, _, err := jobFileNames(job)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(job.MediaDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("无法核对媒体目录，请检查目录权限")
	}
	defer root.Close()
	// long: 发布成功但状态保存失败时任务仍可能显示失败；同时检查 MP4、收据和 NFO，避免校正季号后遗留无法管理的旧媒体。
	for _, name := range []string{media, media + ".moviehub.json", strings.TrimSuffix(media, ".mp4") + ".nfo"} {
		if err := checkJobFile(root, name); err != nil {
			return err
		}
		if _, err := root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			return errors.New("原位置或新位置已有媒体或发布记录，未调整任务，现有文件已保留")
		}
	}
	return nil
}

func (s *Service) ReconfigureJob(ctx context.Context, id string, input JobReconfigure) error {
	if input.Season < 1 || input.Season > 999 || input.LineID == "" || input.EpisodeKey == "" {
		return errors.New("请选择线路、对应分集和有效季号（1–999）")
	}
	s.mu.Lock()
	original, err := s.editableJobLocked(id)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if input.ExpectedUpdatedAt.IsZero() || !input.ExpectedUpdatedAt.Equal(original.UpdatedAt) {
		return errors.New("任务已变化，请刷新后重新打开调整窗口")
	}
	video, err := s.Detail(ctx, original.SourceID, original.VideoID)
	if err != nil {
		return err
	}
	line, err := findLine(video, input.LineID)
	if err != nil {
		return err
	}
	var episode Episode
	for _, candidate := range line.Episodes {
		if candidate.Key == input.EpisodeKey {
			episode = candidate
			break
		}
	}
	if episode.Key == "" {
		return errors.New("所选分集已从线路移除，请重新打开调整窗口")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.editableJobLocked(id)
	if err != nil {
		return err
	}
	// long: 获取最新线路时下载调度仍可前进，提交前重新核对原任务，防止用旧窗口覆盖暂停、取消或另一轮调整。
	if !current.UpdatedAt.Equal(original.UpdatedAt) || current.Status != original.Status || current.LineID != original.LineID ||
		current.Season != original.Season || current.Episode.Key != original.Episode.Key {
		return errors.New("任务已变化，请刷新后重新调整")
	}
	if err := requireUnpublishedJob(current); err != nil {
		return err
	}
	next := current
	next.LineID, next.Episode, next.Season = line.ID, episode, input.Season
	if err := requireUnpublishedJob(next); err != nil {
		return err
	}
	err = s.Store.Update(func(st *State) error {
		for _, other := range st.Jobs {
			if other.ID != id && (jobPath(other) == jobPath(next) || other.SourceID == next.SourceID && other.VideoID == next.VideoID &&
				other.Season == next.Season && other.Episode.Key == next.Episode.Key) {
				return errors.New("该季该集已有任务，请使用已有任务，未生成重复下载")
			}
		}
		for index := range st.Jobs {
			if st.Jobs[index].ID == id {
				if input.Retry {
					next.Status = "queued"
				}
				next.Attempts, next.Progress, next.Speed, next.Error = 0, 0, "", ""
				next.NextAttempt, next.UpdatedAt = time.Time{}, time.Now()
				next.WaitingForSpace = false
				st.Jobs[index] = next
				return nil
			}
		}
		return errors.New("任务不存在")
	})
	if err == nil {
		delete(s.spaceWaiting, id)
	}
	return err
}
