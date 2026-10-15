package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestEmbyServerURL(t *testing.T) {
	for _, raw := range []string{"http://localhost:8096?key=secret", "http://localhost:8096/#part", "http://user:pass@localhost:8096", "file:///tmp/emby", "http://localhost:8096?"} {
		if _, err := embyServerURL(raw); err == nil {
			t.Fatalf("accepted unsafe Emby URL: %q", raw)
		}
	}
	got, err := embyServerURL("  http://localhost:8096/emby/  ")
	if err != nil || got != "http://localhost:8096/emby" {
		t.Fatalf("normalized URL = %q, %v", got, err)
	}
}

func TestEmbyScanAfterPublishedMediaAndRetry(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "data"), filepath.Join(dir, "media"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job := Job{ID: "job-one", Title: "测试剧", SourceID: "source", VideoID: "video", Season: 1, Episode: Episode{Key: "episode-one", Name: "第一集", Number: 1}, Status: "downloading", MediaDir: filepath.Join(dir, "media")}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/emby/Library/Refresh" || r.URL.RawQuery != "" || r.Header.Get("X-Emby-Token") != "test-key" {
			t.Errorf("Emby 扫描请求不符合约定: %s %s", r.Method, r.URL.String())
		}
		// long: Emby 收到通知前，MP4 和两级 NFO 必须已完整落在共享媒体目录。
		for _, path := range []string{jobPath(job), strings.TrimSuffix(jobPath(job), ".mp4") + ".nfo", filepath.Join(filepath.Dir(filepath.Dir(jobPath(job))), "tvshow.nfo")} {
			if info, err := os.Stat(path); err != nil || info.Size() == 0 {
				t.Errorf("扫描时媒体尚未就绪: %s: %v", path, err)
			}
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	if err := store.Update(func(st *State) error {
		st.Settings.EmbyEnabled = true
		st.Settings.EmbyURL = server.URL + "/emby"
		st.Settings.EmbyKey = "test-key"
		st.Jobs = append(st.Jobs, job)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	from := filepath.Join(dir, "ready.mp4")
	if err := os.WriteFile(from, []byte("test media"), 0600); err != nil {
		t.Fatal(err)
	}
	service := NewService(store)
	if err := service.publish(context.Background(), job, from, store.Snapshot().Settings); err != nil {
		t.Fatal(err)
	}
	if state := store.Snapshot(); state.Jobs[0].Status != "completed" || !state.Emby.Pending {
		t.Fatalf("发布后任务或待办状态错误: %+v", state.Emby)
	}
	if err := store.Update(func(st *State) error { st.Emby.NextAttempt = time.Now(); return nil }); err != nil {
		t.Fatal(err)
	}
	service.syncEmby(context.Background())
	failed := store.Snapshot().Emby
	if !failed.Pending || !strings.Contains(failed.Error, "HTTP 503") || !failed.NextAttempt.After(time.Now()) {
		t.Fatalf("失败后没有保留一分钟重试待办: %+v", failed)
	}
	if err := store.Update(func(st *State) error { st.Emby.NextAttempt = time.Now(); return nil }); err != nil {
		t.Fatal(err)
	}
	service.syncEmby(context.Background())
	succeeded := store.Snapshot().Emby
	if succeeded.Pending || succeeded.Error != "" || succeeded.LastRequest.IsZero() || calls.Load() != 2 {
		t.Fatalf("重试成功后状态错误: %+v, calls=%d", succeeded, calls.Load())
	}
}

func TestSettingsChangeSchedulesScan(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "data"), filepath.Join(dir, "media"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := NewService(store)
	cfg := store.Snapshot().Settings
	cfg.EmbyEnabled, cfg.EmbyURL, cfg.EmbyKey = true, "http://localhost:8096/emby/", "test-key"
	body, err := json.Marshal(map[string]any{"settings": cfg})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://localhost/api/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	service.Handler("").ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("保存 Emby 设置失败: %d %s", response.Code, response.Body.String())
	}
	state := store.Snapshot()
	if !state.Emby.Pending || state.Emby.Revision != 1 || state.Emby.NextAttempt.After(time.Now()) || state.Settings.EmbyURL != "http://localhost:8096/emby" {
		t.Fatalf("启用 Emby 后没有安排扫描: %+v", state.Emby)
	}
}

func TestEmbyScanKeepsNewConfigurationPending(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "data"), filepath.Join(dir, "media"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	if err := store.Update(func(st *State) error {
		st.Settings.EmbyEnabled, st.Settings.EmbyURL, st.Settings.EmbyKey = true, server.URL, "test-key"
		st.Emby.Pending, st.Emby.Revision, st.Emby.NextAttempt = true, 1, time.Now()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	service := NewService(store)
	done := make(chan struct{})
	go func() { service.syncEmby(context.Background()); close(done) }()
	<-entered
	if err := store.Update(func(st *State) error {
		// long: 模拟管理员在旧 Emby 请求等待期间换服务器，新待办应立即发往新地址。
		st.Settings.EmbyURL = "http://localhost:8096/new"
		st.Emby.Revision++
		st.Emby.NextAttempt = time.Now()
		return nil
	}); err != nil {
		close(release)
		<-done
		t.Fatal(err)
	}
	close(release)
	<-done
	state := store.Snapshot().Emby
	if !state.Pending || state.Revision != 2 || state.NextAttempt.After(time.Now().Add(time.Second)) || state.Error != "" {
		t.Fatalf("旧请求覆盖了新服务器待办: %+v", state)
	}
}
