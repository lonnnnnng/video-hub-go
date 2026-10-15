package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func embyServerURL(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	parsed, err := url.Parse(value)
	if err != nil || parsed == nil || len(value) > 2048 || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("Emby 地址必须是不含账号、查询参数的 HTTP/HTTPS 服务器地址")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func (s *Service) RequestScan() error {
	err := s.Store.Update(func(st *State) error {
		if !st.Settings.EmbyEnabled {
			return errors.New("请先配置并启用 Emby")
		}
		st.Emby.Pending = true
		st.Emby.Revision++
		st.Emby.NextAttempt = time.Now()
		st.Emby.Error = ""
		return nil
	})
	if err == nil {
		slog.Info("Emby 手动扫描已排队")
	}
	return err
}
func (s *Service) syncEmby(ctx context.Context) {
	if !s.embyMu.TryLock() {
		return
	}
	defer s.embyMu.Unlock()
	st := s.Store.Snapshot()
	if !st.Emby.Pending || !st.Settings.EmbyEnabled || st.Emby.NextAttempt.After(time.Now()) {
		return
	}
	cfg := st.Settings
	started := time.Now()
	status := 0
	// long: MP4 与 NFO 已落盘且任务状态已保存后才会进入待办；扫描只通知 Emby 读取共享媒体目录。
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(cfg.EmbyURL, "/")+"/Library/Refresh", nil)
	if e == nil {
		req.Header.Set("X-Emby-Token", cfg.EmbyKey)
		client := http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		res, err := client.Do(req)
		if err != nil {
			e = errors.New("Emby 连接失败，请检查地址和网络")
		} else {
			status = res.StatusCode
			_, readErr := io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
			res.Body.Close()
			if res.StatusCode < 200 || res.StatusCode >= 300 {
				e = fmt.Errorf("Emby 返回 HTTP %d", res.StatusCode)
				if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
					e = fmt.Errorf("Emby 返回 HTTP %d，请检查管理员 API Key", res.StatusCode)
				}
			} else if readErr != nil {
				e = errors.New("Emby 扫描响应读取失败")
			}
		}
	}
	stale := false
	updateErr := s.Store.Update(func(current *State) error {
		// long: 下载或 Emby 配置在请求期间变化时，旧服务器的结果不能清掉新一轮扫描待办。
		if current.Emby.Revision != st.Emby.Revision {
			stale = true
			return nil
		}
		if e != nil {
			current.Emby.Error = e.Error()
			current.Emby.NextAttempt = time.Now().Add(time.Minute)
			return nil
		}
		current.Emby.LastRequest = time.Now()
		current.Emby.Error = ""
		current.Emby.Pending = false
		return nil
	})
	if updateErr != nil {
		slog.Error("Emby 扫描状态保存失败", "revision", st.Emby.Revision)
		return
	}
	if stale {
		slog.Info("Emby 旧扫描结果已忽略", "revision", st.Emby.Revision, "duration_ms", time.Since(started).Milliseconds())
	} else if e != nil {
		slog.Warn("Emby 扫描通知失败，稍后重试", "revision", st.Emby.Revision, "http_status", status, "duration_ms", time.Since(started).Milliseconds())
	} else {
		slog.Info("Emby 扫描通知成功", "revision", st.Emby.Revision, "http_status", status, "duration_ms", time.Since(started).Milliseconds())
	}
}
