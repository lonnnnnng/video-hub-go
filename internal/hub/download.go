package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
)

func command(ctx context.Context, bin string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 3 * time.Second
	return cmd
}
func safeName(raw string) string {
	var b strings.Builder
	n := 0
	for _, r := range raw {
		if n >= 70 {
			break
		}
		if unicode.IsControl(r) || strings.ContainsRune(`/\:*?"<>|`, r) {
			b.WriteByte('_')
		} else {
			b.WriteRune(r)
		}
		n++
	}
	v := strings.Trim(b.String(), " .")
	if v == "" {
		return "未命名"
	}
	return v
}
func jobPath(j Job) string {
	series := safeName(j.Title) + " [" + identity(j.SourceID+":"+j.VideoID) + "]"
	return filepath.Join(j.MediaDir, series, fmt.Sprintf("Season %02d", j.Season), fmt.Sprintf("%s S%02dE%02d [%s].mp4", safeName(j.Title), j.Season, j.Episode.Number, j.Episode.Key))
}
func probe(ctx context.Context, bin, path string) error {
	info, e := os.Stat(path)
	if e != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return errors.New("输出媒体文件不存在或为空")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	b, e := command(ctx, bin, "-v", "error", "-show_entries", "format=duration:stream=codec_type", "-of", "json", path).Output()
	if e != nil {
		return errors.New("ffprobe 未能验证媒体，请检查工具路径及源文件")
	}
	var p struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			Type string `json:"codec_type"`
		} `json:"streams"`
	}
	if json.Unmarshal(b, &p) != nil {
		return errors.New("媒体检查返回无效结果")
	}
	d, _ := strconv.ParseFloat(p.Format.Duration, 64)
	video := false
	for _, s := range p.Streams {
		if s.Type == "video" {
			video = true
		}
	}
	if !video || d <= 0 {
		return errors.New("输出缺少视频轨道或有效时长")
	}
	return nil
}
func hashFile(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type receipt struct {
	JobID  string `json:"jobId"`
	SHA256 string `json:"sha256"`
}

func existingMedia(j Job) (bool, error) {
	path := jobPath(j)
	_, e := os.Stat(path)
	if errors.Is(e, os.ErrNotExist) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	var r receipt
	b, e := os.ReadFile(path + ".moviehub.json")
	if e != nil || json.Unmarshal(b, &r) != nil || r.JobID != j.ID {
		return false, errors.New("目标已存在但缺少本任务的入库凭据，未覆盖文件")
	}
	hash, e := hashFile(path)
	if e != nil {
		return false, e
	}
	if hash != r.SHA256 {
		return false, errors.New("已入库文件校验不一致，未覆盖文件")
	}
	return true, nil
}

func recoverStaleReceipt(j Job) error {
	dest := jobPath(j)
	b, err := os.ReadFile(dest + ".moviehub.json")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var r receipt
	if json.Unmarshal(b, &r) != nil || r.JobID != j.ID {
		return errors.New("目标凭据属于其他任务，未删除")
	}
	// long: 只清理本任务在媒体硬链接前留下的凭据，避免重试时被旧崩溃现场永久阻塞。
	return os.Remove(dest + ".moviehub.json")
}
func (s *Service) runJob(ctx context.Context, j Job) {
	started := time.Now()
	cfg := s.Store.Snapshot().Settings
	output := jobPath(j)
	ready, e := existingMedia(j)
	if e == nil && !ready {
		if _, statErr := os.Stat(output); errors.Is(statErr, os.ErrNotExist) {
			if recoverErr := recoverStaleReceipt(j); recoverErr != nil {
				e = recoverErr
			}
		}
	}
	if e != nil {
		if _, statErr := os.Stat(output); errors.Is(statErr, os.ErrNotExist) {
			if recoverErr := recoverStaleReceipt(j); recoverErr == nil {
				e = nil
			}
		}
	}
	if e != nil {
		s.finishError(j, e)
		return
	}
	if ready {
		slog.Info("发现已发布媒体，验证后恢复任务", "job_id", j.ID)
		e = probe(ctx, cfg.FFprobe, output)
		if e == nil {
			e = s.publish(ctx, j, "", cfg)
		}
		if e != nil {
			s.finishError(j, e)
		}
		return
	}
	v, e := s.Detail(ctx, j.SourceID, j.VideoID)
	if e != nil {
		s.finishError(j, e)
		return
	}
	line, e := findLine(v, j.LineID)
	if e != nil {
		s.finishError(j, e)
		return
	}
	var ep Episode
	for _, candidate := range line.Episodes {
		if candidate.Key == j.Episode.Key {
			ep = candidate
			break
		}
	}
	if ep.URL == "" {
		s.finishError(j, errors.New("该分集已从资源站移除，请重新选择线路"))
		return
	}
	src, e := s.source(j.SourceID)
	if e != nil {
		s.finishError(j, e)
		return
	}
	// long: 每次尝试独立保存分片，签名 URL 或片单变化后不会误用上一次的媒体数据。
	staging := filepath.Join(j.MediaDir, ".moviehub", j.ID, uid())
	if e = os.MkdirAll(staging, 0700); e != nil {
		s.finishError(j, errors.New("无法创建下载目录，请检查权限和磁盘"))
		return
	}
	attemptCtx, cancel := context.WithTimeout(ctx, 24*time.Hour)
	defer cancel()
	selected := filepath.Join(staging, "media.mp4")
	// long: HLS 网络下载留在本服务进程；FFmpeg 只读取本次任务的本地片单并封装 MP4。
	slog.Info("HLS 下载与封装开始", "job_id", j.ID, "threads", cfg.Threads)
	e = downloadHLS(attemptCtx, ep.URL, src.Headers, staging, selected, cfg.FFmpeg, cfg.Threads, func(p float64, speed string) {
		s.progress(j.ID, p, speed)
	})
	if e != nil {
		slog.Warn("HLS 下载与封装失败", "job_id", j.ID, "duration_ms", time.Since(started).Milliseconds())
		s.finishError(j, e)
		return
	}
	slog.Info("HLS 下载与封装完成", "job_id", j.ID, "duration_ms", time.Since(started).Milliseconds())
	if e = probe(ctx, cfg.FFprobe, selected); e != nil {
		slog.Warn("媒体校验失败", "job_id", j.ID)
		s.finishError(j, e)
		return
	}
	if e = s.publish(ctx, j, selected, cfg); e != nil {
		s.finishError(j, e)
	}
}
func (s *Service) publish(ctx context.Context, j Job, from string, cfg Settings) error {
	// long: 最终入库与暂停操作共用互斥锁，防止用户刚暂停就出现已发布但任务仍为暂停的状态。
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return errors.New("任务已停止")
	}
	current := s.Store.Snapshot()
	running := false
	for _, job := range current.Jobs {
		if job.ID == j.ID && job.Status == "downloading" {
			running = true
		}
	}
	if !running {
		return errors.New("任务状态已变化")
	}
	dest := jobPath(j)
	if e := os.MkdirAll(filepath.Dir(dest), 0755); e != nil {
		return errors.New("创建媒体目录失败")
	}
	if from != "" {
		hash, e := hashFile(from)
		if e != nil {
			return e
		}
		// long: 凭据先以不覆盖方式落盘，崩溃后可识别未发布媒体；硬链接失败时只删除本次创建的凭据。
		if e = writeReceiptNoClobber(dest+".moviehub.json", receipt{JobID: j.ID, SHA256: hash}); e != nil {
			return e
		}
		if e = os.Link(from, dest); e != nil {
			_ = os.Remove(dest + ".moviehub.json")
			return errors.New("发布媒体失败，目标可能已存在或磁盘不支持硬链接")
		}
		f, e := os.Open(dest)
		if e != nil {
			return e
		}
		e = f.Sync()
		f.Close()
		if e != nil {
			return e
		}
		_ = os.Chmod(dest, 0644)
	}
	if e := writeNFO(j); e != nil {
		return e
	}
	info, err := os.Stat(dest)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("无法读取已发布媒体的文件大小")
	}
	e := s.Store.Update(func(st *State) error {
		for i := range st.Jobs {
			if st.Jobs[i].ID == j.ID {
				st.Jobs[i].Status = "completed"
				st.Jobs[i].Output = dest
				st.Jobs[i].SizeBytes = info.Size()
				st.Jobs[i].Progress = 1
				st.Jobs[i].Speed = ""
				st.Jobs[i].Error = ""
				st.Jobs[i].UpdatedAt = time.Now()
			}
		}
		st.Emby.Pending = true
		st.Emby.Revision++
		st.Emby.NextAttempt = time.Now().Add(5 * time.Second)
		return nil
	})
	if e != nil {
		return e
	}
	delete(s.active, j.ID)
	slog.Info("媒体与 NFO 已发布", "job_id", j.ID, "emby_revision", s.Store.Snapshot().Emby.Revision)
	return nil
}

func writeReceiptNoClobber(path string, value receipt) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".receipt-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		f.Close()
		_ = os.Remove(tmp)
	}()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if _, err = f.Write(append(b, '\n')); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Link(tmp, path); err != nil {
		return errors.New("入库凭据已存在，未覆盖")
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func writeNFO(j Job) error {
	type episodeNFO struct {
		XMLName xml.Name `xml:"episodedetails"`
		Title   string   `xml:"title"`
		Show    string   `xml:"showtitle"`
		Season  int      `xml:"season"`
		Episode int      `xml:"episode"`
		Plot    string   `xml:"plot"`
	}
	type showNFO struct {
		XMLName xml.Name `xml:"tvshow"`
		Title   string   `xml:"title"`
		Year    string   `xml:"year,omitempty"`
		Plot    string   `xml:"plot"`
	}
	ep, e := xml.MarshalIndent(episodeNFO{Title: j.Episode.Name, Show: j.Title, Season: j.Season, Episode: j.Episode.Number, Plot: j.Plot}, "", "  ")
	if e != nil {
		return e
	}
	dest := jobPath(j)
	if e = atomicWrite(strings.TrimSuffix(dest, ".mp4")+".nfo", append([]byte(xml.Header), ep...), 0644); e != nil {
		return e
	}
	show, e := xml.MarshalIndent(showNFO{Title: j.Title, Year: j.Year, Plot: j.Plot}, "", "  ")
	if e != nil {
		return e
	}
	return atomicWrite(filepath.Join(filepath.Dir(filepath.Dir(dest)), "tvshow.nfo"), append([]byte(xml.Header), show...), 0644)
}
