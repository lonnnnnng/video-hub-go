package hub

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxPosterBytes = 8 << 20

func posterSeries(j Job) string { return filepath.Dir(filepath.Dir(jobPath(j))) }

func (s *Service) posterLoop() {
	// long: 海报独立于下载队列重试，图片站故障不能把已合成的 MP4 判为下载失败。
	retryAt := map[string]time.Time{}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		s.syncPosters(s.ctx, retryAt)
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) syncPosters(ctx context.Context, retryAt map[string]time.Time) {
	groups := map[string][]Job{}
	order := []string{}
	for _, j := range s.Store.Snapshot().Jobs {
		if j.Status != "completed" {
			continue
		}
		key := posterSeries(j)
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], j)
	}
	// long: 已删除的剧集不保留重试记录；每轮限制数量，避免历史任务集中补图挤占网络。
	for key := range retryAt {
		if _, ok := groups[key]; !ok {
			delete(retryAt, key)
		}
	}
	attempts := 0
	for _, key := range order {
		if ctx.Err() != nil {
			return
		}
		if time.Now().Before(retryAt[key]) {
			continue
		}
		if attempts >= 20 {
			break
		}
		written, err := s.syncPoster(ctx, groups[key])
		if written > 0 || err != nil {
			attempts++
		}
		retryAt[key] = time.Now().Add(time.Minute)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			retryAt[key] = time.Now().Add(5 * time.Minute)
			// long: 错误仅记录业务原因，不输出可能带签名参数的上游图片地址。
			slog.Warn("剧集海报补齐失败，五分钟后重试", "job_id", groups[key][0].ID, "error", err)
		} else if written > 0 {
			slog.Info("剧集海报已补齐，Emby 刷新已排队", "job_id", groups[key][0].ID, "images", written)
		}
	}
}

// long: 只为仍有已完成实体媒体的剧集补图，不创建已删除任务留下的空目录。
func posterTargets(jobs []Job) (*os.Root, []string, error) {
	if len(jobs) == 0 {
		return nil, nil, nil
	}
	root, err := os.OpenRoot(jobs[0].MediaDir)
	if err != nil {
		return nil, nil, errors.New("无法打开海报所在媒体目录")
	}
	targets, seen := []string{}, map[string]bool{}
	for _, j := range jobs {
		media, _, err := jobFileNames(j)
		if err != nil {
			root.Close()
			return nil, nil, err
		}
		if err := checkJobFile(root, media); err != nil {
			root.Close()
			return nil, nil, err
		}
		info, err := root.Stat(media)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			root.Close()
			return nil, nil, errors.New("已完成媒体不可读取，未写入海报")
		}
		for _, dir := range []string{filepath.Dir(filepath.Dir(media)), filepath.Dir(media)} {
			if seen[dir] {
				continue
			}
			seen[dir] = true
			exists, err := hasPoster(root, dir)
			if err != nil {
				root.Close()
				return nil, nil, err
			}
			// long: Emby 也支持把季海报放在剧目录，不能用站源图片覆盖这种手工设置。
			if !exists && dir == filepath.Dir(media) {
				exists, err = hasPosterNamed(root, filepath.Dir(dir), []string{fmt.Sprintf("season%02d-poster", j.Season)})
				if err != nil {
					root.Close()
					return nil, nil, err
				}
			}
			if !exists {
				targets = append(targets, dir)
			}
		}
	}
	return root, targets, nil
}

func hasPoster(root *os.Root, dir string) (bool, error) {
	return hasPosterNamed(root, dir, []string{"folder", "poster", "cover", "default", "show"})
}

func hasPosterNamed(root *os.Root, dir string, names []string) (bool, error) {
	for _, name := range names {
		for _, ext := range []string{".jpg", ".jpeg", ".png", ".tbn", ".webp"} {
			path := filepath.Join(dir, name+ext)
			if err := checkJobFile(root, path); err != nil {
				return false, err
			}
			info, err := root.Lstat(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil || !info.Mode().IsRegular() {
				return false, errors.New("已有海报不是可读取的普通文件")
			}
			// long: 即使已有图片为空也不擅自覆盖，避免更改用户自行放置的文件。
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) syncPoster(ctx context.Context, jobs []Job) (int, error) {
	root, targets, err := posterTargets(jobs)
	if root != nil {
		root.Close()
	}
	if err != nil || len(targets) == 0 {
		return 0, err
	}
	j := jobs[0]
	for _, candidate := range jobs {
		if candidate.Poster != "" {
			j = candidate
			break
		}
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	previousPoster := j.Poster
	data, ext, err := s.CMS.poster(ctx, j.Poster)
	if err != nil {
		// long: 旧版任务没有海报快照，失效的图片签名也需通过原站源详情恢复。
		v, detailErr := s.Detail(ctx, j.SourceID, j.VideoID)
		if detailErr != nil {
			return 0, errors.New("无法从原站源补查海报")
		}
		j.Poster = v.Poster
		if j.Poster == previousPoster && j.Poster != "" {
			return 0, err
		}
		data, ext, err = s.CMS.poster(ctx, j.Poster)
		if err != nil {
			return 0, err
		}
	}
	// long: 网络请求完成后重新核对删除标记和实体媒体，与删除任务共用锁，防止补图写回已删除目录。
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	live := []Job{}
	for _, current := range s.Store.Snapshot().Jobs {
		if current.Status == "completed" && !s.deleting[current.ID] && posterSeries(current) == posterSeries(j) {
			live = append(live, current)
		}
	}
	root, targets, err = posterTargets(live)
	if err != nil || root == nil {
		return 0, err
	}
	defer root.Close()
	written := 0
	for _, dir := range targets {
		if err = writePoster(root, dir, ext, data); err != nil {
			break
		}
		written++
	}
	storeErr := s.Store.Update(func(st *State) error {
		for i := range st.Jobs {
			if st.Jobs[i].SourceID == j.SourceID && st.Jobs[i].VideoID == j.VideoID && (st.Jobs[i].Poster == "" || st.Jobs[i].Poster == previousPoster) {
				st.Jobs[i].Poster = j.Poster
			}
		}
		if written > 0 {
			st.Emby.Pending = true
			st.Emby.Revision++
			st.Emby.NextAttempt = time.Now().Add(5 * time.Second)
		}
		return nil
	})
	return written, errors.Join(err, storeErr)
}

func (c *CMS) poster(ctx context.Context, raw string) ([]byte, string, error) {
	u, err := webURL(strings.TrimSpace(raw))
	if err != nil {
		return nil, "", errors.New("站源未提供有效海报地址")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", errors.New("海报请求地址无效")
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 VideoHubGo")
	res, err := c.Client.Do(req)
	if err != nil {
		return nil, "", errors.New("海报连接失败或超时")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("海报返回 HTTP %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxPosterBytes+1))
	if err != nil || len(data) > maxPosterBytes {
		return nil, "", errors.New("海报读取失败或超过 8 MiB")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png") || cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > 16_000_000 {
		return nil, "", errors.New("海报不是有效 JPEG/PNG 图片或尺寸超过 1600 万像素")
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		return nil, "", errors.New("海报图片内容不完整")
	}
	ext := ".jpg"
	if format == "png" {
		ext = ".png"
	}
	return data, ext, nil
}

func writePoster(root *os.Root, dir, ext string, data []byte) error {
	// long: 同目录临时文件完成同步后用硬链接发布，Emby 不会读到半张图，也不能覆盖已有同名海报。
	if err := checkJobFile(root, dir); err != nil {
		return err
	}
	tmp := filepath.Join(dir, ".moviehub-poster-"+uid()+".tmp")
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return errors.New("创建临时海报失败")
	}
	defer root.Remove(tmp)
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return errors.New("写入海报失败")
	}
	if err := f.Sync(); err != nil {
		return errors.New("同步海报失败")
	}
	if err := f.Close(); err != nil {
		return errors.New("关闭海报失败")
	}
	if err := root.Link(tmp, filepath.Join(dir, "poster"+ext)); err != nil {
		return errors.New("发布海报失败，目标可能已存在")
	}
	d, err := root.Open(dir)
	if err != nil {
		return errors.New("打开海报目录失败")
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return errors.New("同步海报目录失败")
	}
	return nil
}
