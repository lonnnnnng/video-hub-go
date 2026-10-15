package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

type Service struct {
	Store    *Store
	CMS      *CMS
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	active   map[string]context.CancelFunc
	started  bool
	wg       sync.WaitGroup
	followMu sync.Mutex
	embyMu   sync.Mutex
	checkMu  sync.Mutex
}

var errJobChanged = errors.New("任务状态已变化")

func NewService(store *Store) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{Store: store, CMS: NewCMS(), ctx: ctx, cancel: cancel, active: map[string]context.CancelFunc{}}
}
func uid() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func (s *Service) Start() error {
	s.mu.Lock()
	if s.started || s.ctx.Err() != nil {
		s.mu.Unlock()
		return errors.New("服务只能启动一次")
	}
	s.started = true
	s.mu.Unlock()
	recovered := 0
	if err := s.Store.Update(func(st *State) error {
		for i := range st.Jobs {
			j := &st.Jobs[i]
			if j.Status == "downloading" {
				recovered++
				j.Status = "queued"
				j.Error = "服务重启，重新解析资源后继续"
				j.UpdatedAt = time.Now()
			}
		}
		return nil
	}); err != nil {
		s.mu.Lock()
		s.started = false
		s.mu.Unlock()
		return err
	}
	slog.Info("后台调度已启动", "recovered_jobs", recovered)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if len(s.Store.Snapshot().Sources) == 0 {
			if err := s.RefreshSources(s.ctx); err != nil {
				slog.Warn("首次站源导入失败，可在页面重试")
			}
		}
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				s.dispatch()
				s.wg.Add(2)
				go func() { defer s.wg.Done(); s.checkDue() }()
				go func() { defer s.wg.Done(); s.syncEmby(s.ctx) }()
			}
		}
	}()
	return nil
}
func (s *Service) Close() {
	s.cancel()
	s.mu.Lock()
	for _, cancel := range s.active {
		cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}
func (s *Service) RefreshSources(ctx context.Context) error {
	started := time.Now()
	st := s.Store.Snapshot()
	sources, err := s.CMS.Manifest(ctx, st.Settings.SourceURL, st.Sources)
	if err != nil {
		slog.Warn("站源清单刷新失败", "duration_ms", time.Since(started).Milliseconds())
		return err
	}
	err = s.Store.Update(func(current *State) error {
		for i := range sources {
			for _, old := range current.Sources {
				if sources[i].ID == old.ID {
					sources[i].Enabled = old.Enabled
					sources[i].Headers = old.Headers
				}
			}
		}
		current.Sources = sources
		current.SourcesUpdatedAt = time.Now()
		return nil
	})
	if err != nil {
		slog.Error("站源清单保存失败", "count", len(sources))
		return err
	}
	slog.Info("站源清单已刷新", "count", len(sources), "duration_ms", time.Since(started).Milliseconds())
	return nil
}

type SourceCheckSummary struct {
	Total         int    `json:"total"`
	Available     int    `json:"available"`
	Disabled      int    `json:"disabled"`
	DefaultSource string `json:"defaultSource,omitempty"`
}

func (s *Service) CheckSources(ctx context.Context) (SourceCheckSummary, error) {
	s.checkMu.Lock()
	defer s.checkMu.Unlock()
	started := time.Now()
	sources := s.Store.Snapshot().Sources
	if len(sources) == 0 {
		return SourceCheckSummary{}, errors.New("请先刷新站源清单")
	}
	probes := make(map[string]SourceProbe, len(sources))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for _, source := range sources {
		wg.Add(1)
		go func(source Source) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			probe := s.CMS.ProbeSource(probeCtx, source)
			mu.Lock()
			probes[source.ID] = probe
			mu.Unlock()
		}(source)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return SourceCheckSummary{}, err
	}
	summary := SourceCheckSummary{Total: len(sources)}
	err := s.Store.Update(func(st *State) error {
		for i := range st.Sources {
			st.Sources[i].Default = false
		}
		for i := range st.Sources {
			src := &st.Sources[i]
			probe, ok := probes[src.ID]
			if !ok {
				continue
			}
			// long: 检测期间清单可能更新，旧地址的结果不能禁用新地址。
			var sameAPI bool
			for _, original := range sources {
				if original.ID == src.ID && original.API == src.API {
					sameAPI = true
					break
				}
			}
			if !sameAPI {
				continue
			}
			src.CheckStatus, src.CheckError, src.LatencyMS, src.CheckedAt = probe.Status, probe.Error, probe.LatencyMS, time.Now()
			if probe.Status == "ok" {
				summary.Available++
				if src.DisabledByCheck {
					src.Enabled = true
					src.DisabledByCheck = false
				}
			} else {
				summary.Disabled++
				src.Enabled, src.DisabledByCheck = false, true
			}
		}
		sortSources(st.Sources)
		for i := range st.Sources {
			if st.Sources[i].Enabled && st.Sources[i].CheckStatus == "ok" {
				st.Sources[i].Default = true
				summary.DefaultSource = st.Sources[i].Name
				break
			}
		}
		return nil
	})
	if err != nil {
		slog.Error("站源检测结果保存失败", "total", summary.Total)
		return summary, err
	}
	slog.Info("站源检测完成", "total", summary.Total, "available", summary.Available, "disabled", summary.Disabled, "duration_ms", time.Since(started).Milliseconds())
	return summary, nil
}

func (s *Service) Home(ctx context.Context, page int, typeID string) (HomeResult, error) {
	sources := s.Store.Snapshot().Sources
	for _, source := range sources {
		if source.Default && source.Enabled {
			return s.CMS.Home(ctx, source, page, typeID)
		}
	}
	return HomeResult{}, errors.New("没有可用的默认站源，请到资源站页面检测")
}
func (s *Service) source(id string) (Source, error) {
	for _, src := range s.Store.Snapshot().Sources {
		if src.ID == id {
			return src, nil
		}
	}
	return Source{}, errors.New("站源不存在，请先刷新站源")
}
func (s *Service) Detail(ctx context.Context, sourceID, id string) (Video, error) {
	src, err := s.source(sourceID)
	if err != nil {
		return Video{}, err
	}
	return s.CMS.Detail(ctx, src, id)
}

type Selection struct {
	SourceID     string   `json:"sourceId"`
	VideoID      string   `json:"videoId"`
	LineID       string   `json:"lineId"`
	Keys         []string `json:"keys"`
	Season       int      `json:"season"`
	AutoDownload bool     `json:"autoDownload"`
	Backfill     bool     `json:"backfill"`
}

func validateSelection(sel Selection) error {
	if sel.SourceID == "" || sel.VideoID == "" || sel.Season < 1 || sel.Season > 999 {
		return errors.New("请选择影片和有效季号（1–999）")
	}
	return nil
}
func enqueue(st *State, v Video, l Line, eps []Episode, season int) int {
	n := 0
	for _, ep := range eps {
		exists := false
		for _, j := range st.Jobs {
			if j.SourceID == v.SourceID && j.VideoID == v.ID && j.Season == season && j.Episode.Key == ep.Key {
				exists = true
				break
			}
		}
		if exists {
			continue
		}
		now := time.Now()
		st.Jobs = append(st.Jobs, Job{ID: uid(), SourceID: v.SourceID, VideoID: v.ID, Title: v.Title, Year: v.Year, Plot: v.Plot, LineID: l.ID, Episode: ep, Season: season, Status: "queued", MediaDir: st.Settings.MediaDir, CreatedAt: now, UpdatedAt: now})
		n++
	}
	return n
}
func (s *Service) Queue(ctx context.Context, sel Selection) (int, error) {
	if e := validateSelection(sel); e != nil {
		return 0, e
	}
	v, e := s.Detail(ctx, sel.SourceID, sel.VideoID)
	if e != nil {
		return 0, e
	}
	l, e := findLine(v, sel.LineID)
	if e != nil {
		return 0, e
	}
	wanted := map[string]bool{}
	for _, key := range sel.Keys {
		wanted[key] = true
	}
	eps := []Episode{}
	for _, ep := range l.Episodes {
		if wanted[ep.Key] {
			eps = append(eps, ep)
		}
	}
	if len(eps) == 0 || len(eps) != len(wanted) {
		return 0, errors.New("分集清单已变化，请重新打开详情并选择")
	}
	n := 0
	e = s.Store.Update(func(st *State) error { n = enqueue(st, v, l, eps, sel.Season); return nil })
	if e == nil {
		slog.Info("下载任务已入队", "source_id", sel.SourceID, "requested", len(eps), "queued", n)
	}
	return n, e
}
func (s *Service) AddFollow(ctx context.Context, sel Selection) error {
	if e := validateSelection(sel); e != nil {
		return e
	}
	v, e := s.Detail(ctx, sel.SourceID, sel.VideoID)
	if e != nil {
		return e
	}
	l, e := findLine(v, sel.LineID)
	if e != nil {
		return e
	}
	backfilled := 0
	err := s.Store.Update(func(st *State) error {
		for _, f := range st.Follows {
			if f.Video.SourceID == v.SourceID && f.Video.ID == v.ID && f.Season == sel.Season {
				return errors.New("该剧本季已在追更列表")
			}
		}
		known := []string{}
		for _, ep := range l.Episodes {
			known = append(known, ep.Key)
		}
		now := time.Now()
		st.Follows = append(st.Follows, Follow{ID: uid(), Video: v, LineID: l.ID, Season: sel.Season, AutoDownload: sel.AutoDownload, Enabled: true, Known: known, CreatedAt: now, LastCheck: now})
		if sel.Backfill {
			backfilled = enqueue(st, v, l, l.Episodes, sel.Season)
		}
		return nil
	})
	if err == nil {
		slog.Info("追更已添加", "source_id", sel.SourceID, "season", sel.Season, "auto_download", sel.AutoDownload, "backfilled_jobs", backfilled)
	}
	return err
}
func (s *Service) checkDue() {
	if !s.followMu.TryLock() {
		return
	}
	defer s.followMu.Unlock()
	st := s.Store.Snapshot()
	for _, f := range st.Follows {
		if s.ctx.Err() != nil {
			return
		}
		if f.Enabled && time.Since(f.LastCheck) >= time.Duration(st.Settings.CheckMinutes)*time.Minute {
			s.checkFollow(s.ctx, f)
		}
	}
}
func (s *Service) CheckFollows(ctx context.Context) error {
	if !s.followMu.TryLock() {
		return errors.New("正在检查追更，请稍后")
	}
	defer s.followMu.Unlock()
	checked := 0
	for _, f := range s.Store.Snapshot().Follows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if f.Enabled {
			s.checkFollow(ctx, f)
			checked++
		}
	}
	slog.Info("手动追更检查完成", "checked", checked)
	return nil
}
func (s *Service) checkFollow(ctx context.Context, f Follow) {
	v, err := s.Detail(ctx, f.Video.SourceID, f.Video.ID)
	var l Line
	if err == nil {
		l, err = findLine(v, f.LineID)
	}
	addedCount, queuedCount := 0, 0
	e := s.Store.Update(func(st *State) error {
		for i := range st.Follows {
			current := &st.Follows[i]
			if current.ID != f.ID || !current.Enabled {
				continue
			}
			current.LastCheck = time.Now()
			if err != nil {
				current.LastError = err.Error()
				return nil
			}
			seen := map[string]bool{}
			for _, key := range current.Known {
				seen[key] = true
			}
			added := []Episode{}
			for _, ep := range l.Episodes {
				if !seen[ep.Key] {
					current.Known = append(current.Known, ep.Key)
					seen[ep.Key] = true
					added = append(added, ep)
				}
			}
			current.NewCount += len(added)
			addedCount = len(added)
			current.Video = v
			current.LastError = ""
			if current.AutoDownload {
				queuedCount = enqueue(st, v, l, added, current.Season)
			}
			break
		}
		return nil
	})
	if e != nil {
		slog.Error("保存追更状态失败", "follow_id", f.ID)
	} else if err != nil {
		slog.Warn("追更检查失败", "follow_id", f.ID)
	} else if addedCount > 0 {
		slog.Info("追更发现新集", "follow_id", f.ID, "new_episodes", addedCount, "queued_jobs", queuedCount)
	}
}
func (s *Service) FollowAction(id, action string) error {
	return s.Store.Update(func(st *State) error {
		for i := range st.Follows {
			f := &st.Follows[i]
			if f.ID == id {
				switch action {
				case "toggle":
					f.Enabled = !f.Enabled
				case "auto":
					f.AutoDownload = !f.AutoDownload
				case "remove":
					st.Follows = append(st.Follows[:i], st.Follows[i+1:]...)
				default:
					return errors.New("未知追更操作")
				}
				return nil
			}
		}
		return errors.New("追更记录不存在")
	})
}
func (s *Service) JobAction(id, action string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.Store.Update(func(st *State) error {
		for i := range st.Jobs {
			j := &st.Jobs[i]
			if j.ID != id {
				continue
			}
			switch action {
			case "pause":
				if j.Status != "queued" && j.Status != "downloading" {
					return errors.New("当前任务不能暂停")
				}
				j.Status = "paused"
			case "cancel":
				if j.Status == "completed" {
					return errors.New("已入库文件请在媒体目录管理")
				}
				j.Status = "canceled"
			case "resume":
				if j.Status != "paused" && j.Status != "failed" && j.Status != "canceled" {
					return errors.New("当前任务不能重试")
				}
				j.Status = "queued"
				j.Attempts = 0
				j.Error = ""
				j.NextAttempt = time.Time{}
			default:
				return errors.New("未知任务操作")
			}
			j.UpdatedAt = time.Now()
			return nil
		}
		return errors.New("任务不存在")
	})
	if err == nil && (action == "pause" || action == "cancel") {
		if cancel := s.active[id]; cancel != nil {
			cancel()
		}
	}
	if err == nil {
		slog.Info("下载任务状态已调整", "job_id", id, "action", action)
	}
	return err
}
func (s *Service) dispatch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return
	}
	st := s.Store.Snapshot()
	capacity := st.Settings.Workers - len(s.active)
	for _, j := range st.Jobs {
		if capacity <= 0 {
			break
		}
		if j.Status != "queued" || j.NextAttempt.After(time.Now()) {
			continue
		}
		if _, ok := s.active[j.ID]; ok {
			continue
		}
		job := j
		found := false
		err := s.Store.Update(func(st *State) error {
			for i := range st.Jobs {
				if st.Jobs[i].ID == job.ID && st.Jobs[i].Status == "queued" {
					found = true
					st.Jobs[i].Status = "downloading"
					st.Jobs[i].Attempts++
					st.Jobs[i].UpdatedAt = time.Now()
					job = st.Jobs[i]
					break
				}
			}
			if !found {
				return errJobChanged
			}
			return nil
		})
		if err != nil {
			if !errors.Is(err, errJobChanged) {
				slog.Error("保存任务启动状态失败", "job_id", job.ID)
			}
			return
		}
		ctx, cancel := context.WithCancel(s.ctx)
		s.active[job.ID] = cancel
		slog.Info("下载任务开始", "job_id", job.ID, "attempt", job.Attempts, "active_jobs", len(s.active))
		s.wg.Add(1)
		go func() { defer s.wg.Done(); s.runJob(ctx, job); cancel() }()
		capacity--
	}
}
func (s *Service) progress(id string, p float64, speed string) {
	if p < 0 {
		p = 0
	}
	if p > 1 {
		p = 1
	}
	if len(speed) > 40 {
		speed = ""
	}
	if err := s.Store.Update(func(st *State) error {
		for i := range st.Jobs {
			j := &st.Jobs[i]
			if j.ID == id && j.Status == "downloading" {
				j.Progress = p
				j.Speed = speed
				j.UpdatedAt = time.Now()
			}
		}
		return nil
	}); err != nil {
		slog.Error("保存下载进度失败", "job_id", id)
	}
}
func (s *Service) finishError(job Job, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer delete(s.active, job.ID)
	status := "unchanged"
	var nextAttempt time.Time
	e := s.Store.Update(func(st *State) error {
		for i := range st.Jobs {
			j := &st.Jobs[i]
			if j.ID != job.ID || j.Status != "downloading" {
				continue
			}
			j.Speed = ""
			j.UpdatedAt = time.Now()
			if s.ctx.Err() != nil {
				j.Status = "queued"
				status = j.Status
				j.Error = "服务停止，等待重新启动"
				break
			}
			j.Error = err.Error()
			j.Status = "failed"
			if j.Attempts < st.Settings.MaxAttempts {
				j.Status = "queued"
				j.NextAttempt = time.Now().Add(time.Duration(j.Attempts*j.Attempts) * 30 * time.Second)
			}
			status, nextAttempt = j.Status, j.NextAttempt
			break
		}
		return nil
	})
	if e != nil {
		slog.Error("保存任务失败状态失败", "job_id", job.ID)
	} else {
		// long: 下载错误可能包含资源站签名地址或 FFmpeg 输出，日志只记任务阶段与重试状态。
		slog.Warn("下载任务未完成", "job_id", job.ID, "attempt", job.Attempts, "status", status, "next_attempt", nextAttempt)
	}
}
func (s *Service) String() string {
	return fmt.Sprintf("Movie Hub Go (%d sources)", len(s.Store.Snapshot().Sources))
}
