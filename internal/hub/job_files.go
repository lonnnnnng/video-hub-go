package hub

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func jobFileNames(job Job) (media, temporary string, err error) {
	component := func(value string) bool {
		return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, "/\\\x00")
	}
	if !filepath.IsAbs(job.MediaDir) || !component(job.ID) || !component(job.Episode.Key) {
		return "", "", errors.New("任务文件路径无效，未操作文件")
	}
	media, err = filepath.Rel(job.MediaDir, jobPath(job))
	if err != nil || !filepath.IsLocal(media) || (job.Output != "" && filepath.Clean(job.Output) != jobPath(job)) {
		return "", "", errors.New("任务文件位置与归档规则不一致，未操作文件")
	}
	return media, filepath.Join(".moviehub", job.ID), nil
}

func checkJobFile(root *os.Root, name string) error {
	part := ""
	for _, component := range strings.Split(name, string(filepath.Separator)) {
		part = filepath.Join(part, component)
		info, err := root.Lstat(part)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return errors.New("无法检查任务文件，请检查媒体目录权限")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("任务文件路径包含符号链接，未操作文件")
		}
		if part == name && !info.Mode().IsRegular() && !info.IsDir() {
			return errors.New("任务文件类型无效，未操作文件")
		}
	}
	return nil
}

func removeJobFiles(job Job) (bool, error) {
	media, temporary, err := jobFileNames(job)
	if err != nil {
		return false, err
	}
	root, err := os.OpenRoot(job.MediaDir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, errors.New("无法打开媒体目录，未删除任务文件")
	}
	defer root.Close()
	nfo, proof := strings.TrimSuffix(media, ".mp4")+".nfo", media+".moviehub.json"
	// long: 只处理本集及本任务的临时目录；共享 tvshow.nfo 和其他集的文件必须保留。
	for _, name := range []string{media, nfo, proof, temporary} {
		if err := checkJobFile(root, name); err != nil {
			return false, err
		}
		if info, err := root.Lstat(name); err == nil && name != temporary && !info.Mode().IsRegular() {
			return false, errors.New("媒体或元数据不是普通文件，未删除")
		}
	}
	data, proofErr := root.ReadFile(proof)
	if proofErr == nil {
		var owner receipt
		if json.Unmarshal(data, &owner) != nil || owner.JobID != job.ID {
			return false, errors.New("入库凭据不属于本任务，未删除媒体文件")
		}
	} else {
		if !errors.Is(proofErr, os.ErrNotExist) {
			return false, errors.New("无法读取入库凭据，未删除媒体文件")
		}
		for _, name := range []string{media, nfo} {
			if _, err := root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
				return false, errors.New("文件缺少本任务的入库凭据，未删除媒体文件")
			}
		}
	}
	removed := false
	for _, name := range []string{media, nfo, proof} {
		if err := root.Remove(name); err == nil {
			removed = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return removed, errors.New("部分任务文件未能删除，请检查权限后重试")
		}
	}
	if err := root.RemoveAll(temporary); err != nil {
		return removed, errors.New("临时分片未能删除，请检查权限后重试")
	}
	return removed, nil
}

func (s *Service) DeleteJob(ctx context.Context, id string, deleteFiles bool) error {
	s.mu.Lock()
	if s.deleting[id] {
		s.mu.Unlock()
		return errors.New("任务正在删除，请稍候")
	}
	if s.cleaning[id] {
		s.mu.Unlock()
		return errors.New("临时分片正在校验清理，请稍后重试删除")
	}
	var job Job
	for _, candidate := range s.Store.Snapshot().Jobs {
		if candidate.ID == id {
			job = candidate
			break
		}
	}
	if job.ID == "" {
		s.mu.Unlock()
		return errors.New("任务不存在")
	}
	if deleteFiles {
		if _, _, err := jobFileNames(job); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	// long: 先把可调度任务改成取消，再等待 worker 退出；删除失败或请求中断也不会继续写文件。
	err := s.Store.Update(func(st *State) error {
		for i := range st.Jobs {
			if st.Jobs[i].ID == id && (st.Jobs[i].Status == "queued" || st.Jobs[i].Status == "downloading") {
				st.Jobs[i].Status = "canceled"
				st.Jobs[i].Speed = ""
				st.Jobs[i].UpdatedAt = time.Now()
			}
		}
		return nil
	})
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.deleting[id] = true
	done := s.workers[id]
	if cancel := s.active[id]; cancel != nil {
		cancel()
	}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.deleting, id); s.mu.Unlock() }()
	if done != nil {
		waitCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		select {
		case <-done:
		case <-waitCtx.Done():
			return errors.New("下载仍在停止，任务已保留，请稍后重试删除")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := false
	var fileErr error
	if deleteFiles {
		removed, fileErr = removeJobFiles(job)
	}
	err = s.Store.Update(func(st *State) error {
		if removed {
			st.Emby.Pending = true
			st.Emby.Revision++
			st.Emby.NextAttempt = time.Now().Add(5 * time.Second)
		}
		for i := range st.Jobs {
			if st.Jobs[i].ID == id {
				if fileErr != nil {
					st.Jobs[i].Error = fileErr.Error()
					st.Jobs[i].UpdatedAt = time.Now()
				} else {
					st.Jobs = append(st.Jobs[:i], st.Jobs[i+1:]...)
				}
				break
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if fileErr != nil {
		slog.Warn("下载任务文件删除失败，保留记录供重试", "job_id", id)
		return fileErr
	}
	slog.Info("下载任务已删除", "job_id", id, "delete_files", deleteFiles)
	return nil
}

func (s *Service) serveJobMedia(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	var job Job
	for _, candidate := range s.Store.Snapshot().Jobs {
		if candidate.ID == r.PathValue("id") && candidate.Status == "completed" && !s.deleting[candidate.ID] {
			job = candidate
			break
		}
	}
	file, err := openJobMedia(job)
	s.mu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "已下载视频不存在或不可读取"})
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "已下载视频不可读取"})
		return
	}
	// long: 仅按已完成任务定位本地 MP4，ServeContent 提供拖动进度需要的 Range/HEAD 支持，不接受客户端文件路径。
	w.Header().Set("Content-Type", "video/mp4")
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

func openJobMedia(job Job) (*os.File, error) {
	media, _, err := jobFileNames(job)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(job.MediaDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := checkJobFile(root, media); err != nil {
		return nil, err
	}
	return root.Open(media)
}
