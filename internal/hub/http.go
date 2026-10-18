package hub

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"movie-hub-go/internal/embycheck"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

//go:embed web/*
var webFiles embed.FS

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

type apiResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *apiResponseWriter) WriteHeader(code int) {
	if w.status != 0 {
		return
	}
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *apiResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func decode(w http.ResponseWriter, r *http.Request, v any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return errors.New("请使用 application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return errors.New("请求 JSON 无效或字段不受支持")
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return errors.New("请求必须只有一份 JSON")
	}
	return nil
}
func redact(st State) State {
	st.Settings.EmbyKey = ""
	for i := range st.Sources {
		st.Sources[i].Headers = nil
	}
	for i := range st.Jobs {
		st.Jobs[i].Episode.URL = ""
	}
	for i := range st.Follows {
		for k := range st.Follows[i].Video.Lines {
			for j := range st.Follows[i].Video.Lines[k].Episodes {
				st.Follows[i].Video.Lines[k].Episodes[j].URL = ""
			}
		}
	}
	return st
}
func validSettings(c *Settings) error {
	if c.Workers < 1 || c.Workers > 6 || c.Threads < 1 || c.Threads > 32 || c.MaxAttempts < 1 || c.MaxAttempts > 10 || c.CheckMinutes < 1 || c.CheckMinutes > 1440 {
		return errors.New("并发 1–6，分片线程 1–32，重试次数 1–10，追更间隔 1–1440 分钟")
	}
	if strings.TrimSpace(c.MediaDir) == "" || c.FFmpeg == "" || c.FFprobe == "" {
		return errors.New("请填写媒体目录、FFmpeg 和 FFprobe 路径")
	}
	abs, e := filepath.Abs(c.MediaDir)
	if e != nil {
		return errors.New("媒体目录无效")
	}
	c.MediaDir = abs
	c.SourceURL = SourceListURL
	if c.EmbyURL != "" {
		c.EmbyURL, e = embyServerURL(c.EmbyURL)
		if e != nil {
			return e
		}
	}
	if len(c.EmbyKey) > 2048 || strings.ContainsAny(c.EmbyKey, "\x00\r\n") {
		return errors.New("Emby API Key 格式无效")
	}
	if c.EmbyEnabled && (c.EmbyURL == "" || c.EmbyKey == "") {
		return errors.New("启用 Emby 需要服务器地址及 API Key")
	}
	return nil
}
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	files, _ := fs.Sub(webFiles, "web")
	mux.Handle("/", http.FileServer(http.FS(files)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok", "version": Version})
	})
	api := func(pattern string, handler func(http.ResponseWriter, *http.Request) error) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			tracked := &apiResponseWriter{ResponseWriter: w}
			started := time.Now()
			if err := handler(tracked, r); err != nil {
				writeJSON(tracked, 400, map[string]string{"error": err.Error()})
			}
			// long: 请求日志只记路由模板，搜索词、播放 URL、令牌和请求体都不能写入日志。
			if pattern != "GET /api/state" {
				slog.Info("API 请求完成", "method", r.Method, "route", pattern, "status", tracked.status, "duration_ms", time.Since(started).Milliseconds())
			}
		})
	}
	api("GET /api/update", func(w http.ResponseWriter, r *http.Request) error {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		info, err := s.updates.info(ctx, false)
		if err != nil {
			return err
		}
		writeJSON(w, 200, info)
		return nil
	})
	api("POST /api/update/check", func(w http.ResponseWriter, r *http.Request) error {
		var input struct{}
		if err := decode(w, r, &input); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		info, err := s.updates.info(ctx, true)
		if err != nil {
			return err
		}
		writeJSON(w, 200, info)
		return nil
	})
	api("POST /api/update/apply", func(w http.ResponseWriter, r *http.Request) error {
		// long: 网页升级会替换程序并重启服务，只接收明确同源的 JSON 请求。
		if r.Header.Get("Origin") == "" {
			return errors.New("请从管理页面发起更新")
		}
		var input struct {
			Version string `json:"version"`
		}
		if err := decode(w, r, &input); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if err := s.updates.start(ctx, input.Version); err != nil {
			return err
		}
		writeJSON(w, 202, map[string]bool{"updating": true})
		return nil
	})
	api("GET /api/state", func(w http.ResponseWriter, r *http.Request) error {
		st := s.Store.Snapshot()
		tools := map[string]bool{}
		for name, bin := range map[string]string{"ffmpeg": st.Settings.FFmpeg, "ffprobe": st.Settings.FFprobe} {
			_, err := exec.LookPath(bin)
			tools[name] = err == nil
		}
		writeJSON(w, 200, map[string]any{"state": redact(st), "tools": tools, "embyKeySet": st.Settings.EmbyKey != ""})
		return nil
	})
	api("POST /api/settings", func(w http.ResponseWriter, r *http.Request) error {
		var input struct {
			Settings     Settings `json:"settings"`
			ClearEmbyKey bool     `json:"clearEmbyKey"`
		}
		if e := decode(w, r, &input); e != nil {
			return e
		}
		embyChanged, embyEnabled := false, false
		e := s.Store.Update(func(st *State) error {
			cfg := input.Settings
			if cfg.EmbyKey == "" && !input.ClearEmbyKey {
				cfg.EmbyKey = st.Settings.EmbyKey
			}
			if e := validSettings(&cfg); e != nil {
				return e
			}
			// long: 首次启用、换 Emby 服务器或更换凭据后，即使没有新下载，也要让 Emby 扫描已有媒体。
			changed := cfg.EmbyEnabled != st.Settings.EmbyEnabled || cfg.EmbyURL != st.Settings.EmbyURL || cfg.EmbyKey != st.Settings.EmbyKey
			embyChanged, embyEnabled = changed, cfg.EmbyEnabled
			st.Settings = cfg
			if changed {
				st.Emby.Revision++
				for i := range st.Jobs {
					st.Jobs[i].EmbyMedia = embycheck.Status{}
				}
				if cfg.EmbyEnabled {
					st.Emby.Pending = true
					st.Emby.NextAttempt = time.Now()
					st.Emby.Error = ""
				}
			}
			return nil
		})
		if e != nil {
			return e
		}
		if embyChanged {
			slog.Info("Emby 配置已更新", "enabled", embyEnabled)
		}
		writeJSON(w, 200, map[string]bool{"saved": true})
		return nil
	})
	api("POST /api/sources/refresh", func(w http.ResponseWriter, r *http.Request) error {
		if e := s.RefreshSources(r.Context()); e != nil {
			return e
		}
		writeJSON(w, 200, map[string]bool{"refreshed": true})
		return nil
	})
	api("POST /api/sources/check", func(w http.ResponseWriter, r *http.Request) error {
		summary, err := s.CheckSources(r.Context())
		if err != nil {
			return err
		}
		writeJSON(w, 200, summary)
		return nil
	})
	api("POST /api/sources/{id}", func(w http.ResponseWriter, r *http.Request) error {
		var input struct {
			Enabled bool `json:"enabled"`
		}
		if e := decode(w, r, &input); e != nil {
			return e
		}
		e := s.Store.Update(func(st *State) error {
			for i := range st.Sources {
				if st.Sources[i].ID == r.PathValue("id") {
					// long: 手动调整开关代表用户选择，后续检测不再按上次自动禁用状态恢复站源。
					st.Sources[i].DisabledByCheck = false
					st.Sources[i].Enabled = input.Enabled
					chooseDefaultSource(st.Sources)
					return nil
				}
			}
			return errors.New("站源不存在")
		})
		if e != nil {
			return e
		}
		writeJSON(w, 200, map[string]bool{"saved": true})
		return nil
	})
	api("POST /api/sources/{id}/default", func(w http.ResponseWriter, r *http.Request) error {
		var input struct{}
		if err := decode(w, r, &input); err != nil {
			return err
		}
		if err := s.Store.Update(func(st *State) error {
			selected := -1
			for i, source := range st.Sources {
				if source.ID == r.PathValue("id") {
					selected = i
					break
				}
			}
			if selected < 0 {
				return errors.New("站源不存在")
			}
			if !st.Sources[selected].Enabled {
				return errors.New("请先启用站源，再设为默认")
			}
			// long: 手动默认只改变首页来源，保留原延迟排序；下次一键检测仍会重新推荐最快站源。
			for i := range st.Sources {
				st.Sources[i].Default = i == selected
			}
			return nil
		}); err != nil {
			return err
		}
		writeJSON(w, 200, map[string]bool{"saved": true})
		return nil
	})
	api("GET /api/home", func(w http.ResponseWriter, r *http.Request) error {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 1 {
			page = 1
		}
		if page > 10000 {
			return errors.New("页码过大")
		}
		typeID := strings.TrimSpace(r.URL.Query().Get("typeId"))
		if len(typeID) > 100 {
			return errors.New("分类标识过长")
		}
		result, err := s.Home(r.Context(), page, typeID)
		if err != nil {
			return err
		}
		writeJSON(w, 200, result)
		return nil
	})
	api("GET /api/search", func(w http.ResponseWriter, r *http.Request) error {
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if q == "" || len(q) > 200 {
			return errors.New("请输入 1–200 字节的搜索词")
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 1 {
			page = 1
		}
		if page > 10000 {
			return errors.New("页码过大")
		}
		sources := s.Store.Snapshot().Sources
		if id := r.URL.Query().Get("sourceId"); id != "" {
			src, e := s.source(id)
			if e != nil {
				return e
			}
			sources = []Source{src}
		}
		writeJSON(w, 200, s.CMS.Search(r.Context(), sources, q, page))
		return nil
	})
	api("GET /api/detail", func(w http.ResponseWriter, r *http.Request) error {
		v, e := s.Detail(r.Context(), r.URL.Query().Get("sourceId"), r.URL.Query().Get("id"))
		if e != nil {
			return e
		}
		// long: 剧集详情需要返回资源站给出的分集播放地址，便于用户核对线路；请求详情时仍重新从站源获取最新地址。
		writeJSON(w, 200, v)
		return nil
	})
	api("POST /api/jobs", func(w http.ResponseWriter, r *http.Request) error {
		var sel Selection
		if e := decode(w, r, &sel); e != nil {
			return e
		}
		n, e := s.Queue(r.Context(), sel)
		if e != nil {
			return e
		}
		writeJSON(w, 200, map[string]int{"queued": n})
		return nil
	})
	api("POST /api/jobs/cleanup", func(w http.ResponseWriter, r *http.Request) error {
		select {
		case s.cleanupWake <- struct{}{}:
		default:
		}
		writeJSON(w, http.StatusAccepted, map[string]bool{"pending": true})
		return nil
	})
	api("POST /api/jobs/batch", func(w http.ResponseWriter, r *http.Request) error {
		var input struct {
			IDs    []string `json:"ids"`
			Action string   `json:"action"`
		}
		if err := decode(w, r, &input); err != nil {
			return err
		}
		results, err := s.BatchJobAction(r.Context(), input.IDs, input.Action)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]any{"results": results})
		return nil
	})
	api("POST /api/jobs/{id}/{action}", func(w http.ResponseWriter, r *http.Request) error {
		if r.PathValue("action") == "reconfigure" {
			var input JobReconfigure
			if err := decode(w, r, &input); err != nil {
				return err
			}
			if err := s.ReconfigureJob(r.Context(), r.PathValue("id"), input); err != nil {
				return err
			}
			writeJSON(w, 200, map[string]bool{"saved": true})
			return nil
		}
		if action := r.PathValue("action"); action == "remove" || action == "remove-files" {
			if err := s.DeleteJob(r.Context(), r.PathValue("id"), action == "remove-files"); err != nil {
				return err
			}
			writeJSON(w, 200, map[string]bool{"deleted": true})
			return nil
		}
		if e := s.JobAction(r.PathValue("id"), r.PathValue("action")); e != nil {
			return e
		}
		writeJSON(w, 200, map[string]bool{"saved": true})
		return nil
	})
	mux.HandleFunc("GET /api/jobs/{id}/media", s.serveJobMedia)
	api("POST /api/follows", func(w http.ResponseWriter, r *http.Request) error {
		var sel Selection
		if e := decode(w, r, &sel); e != nil {
			return e
		}
		if e := s.AddFollow(r.Context(), sel); e != nil {
			return e
		}
		writeJSON(w, 200, map[string]bool{"saved": true})
		return nil
	})
	api("POST /api/follows/check", func(w http.ResponseWriter, r *http.Request) error {
		if e := s.CheckFollows(r.Context()); e != nil {
			return e
		}
		writeJSON(w, 200, map[string]bool{"checked": true})
		return nil
	})
	api("POST /api/follows/{id}/{action}", func(w http.ResponseWriter, r *http.Request) error {
		if e := s.FollowAction(r.PathValue("id"), r.PathValue("action")); e != nil {
			return e
		}
		writeJSON(w, 200, map[string]bool{"saved": true})
		return nil
	})
	api("POST /api/emby/scan", func(w http.ResponseWriter, r *http.Request) error {
		if e := s.RequestScan(); e != nil {
			return e
		}
		writeJSON(w, 202, map[string]bool{"pending": true})
		return nil
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		// long: 浏览器直接请求站源媒体，允许 HTTP/HTTPS 片单和分片连接；脚本仍只加载服务内置资源。
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' https: http: data:; connect-src 'self' https: http:; media-src 'self' https: http: blob:; worker-src 'self' blob:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		// long: 局域网免令牌模式只接受 IP 或 localhost，避免恶意域名通过 DNS 重绑定读取片库设置。
		host := r.Host
		if value, _, err := net.SplitHostPort(host); err == nil {
			host = value
		}
		if host != "localhost" && net.ParseIP(host) == nil {
			writeJSON(w, 403, map[string]string{"error": "请使用服务器 IP 访问"})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			// long: 局域网免令牌访问仍校验 Origin，阻止其他网页借浏览器发起跨站管理请求。
			if origin := r.Header.Get("Origin"); origin != "" {
				u, e := url.Parse(origin)
				if e != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
					writeJSON(w, 403, map[string]string{"error": "拒绝跨站请求"})
					return
				}
			}
		}
		mux.ServeHTTP(w, r)
	})
}
