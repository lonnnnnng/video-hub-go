package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func testPoster(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 3))
	img.Set(1, 1, color.RGBA{R: 180, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestQueueSnapshotsPoster(t *testing.T) {
	st := defaultState(t.TempDir())
	v := Video{ID: "video", SourceID: "source", Title: "测试剧", Poster: "https://image.example/poster.jpg"}
	ep := Episode{Key: "ep", Number: 1}
	if enqueue(&st, v, Line{ID: "line"}, []Episode{ep}, 1) != 1 || st.Jobs[0].Poster != v.Poster {
		t.Fatal("入队未保留站源海报")
	}
}

func TestPosterBackfillsOldJobsOncePerSeriesAndScans(t *testing.T) {
	s, j := jobFilesFixture(t)
	data := testPoster(t)
	var details, images, scans atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/poster.jpg":
			images.Add(1)
			// long: 上游扩展名和 Content-Type 都可能不准确，落盘格式必须以实际图片内容为准。
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write(data)
		case "/emby/Library/Refresh":
			scans.Add(1)
			for _, dir := range []string{posterSeries(j), filepath.Dir(j.Output), filepath.Join(posterSeries(j), "Season 02")} {
				if b, err := os.ReadFile(filepath.Join(dir, "poster.png")); err != nil || !bytes.Equal(b, data) {
					t.Errorf("扫描时海报尚未完整落盘: %v", err)
				}
			}
			if r.Header.Get("X-Emby-Token") != "test-key" {
				t.Error("扫描缺少认证头")
			}
			w.WriteHeader(204)
		default:
			details.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"list": []map[string]any{{"vod_id": j.VideoID, "vod_name": j.Title, "vod_pic": "http://" + r.Host + "/poster.jpg"}}})
		}
	}))
	defer server.Close()
	s.CMS = localTestCMS()
	sibling := j
	sibling.ID, sibling.Season, sibling.Episode.Key = "job-two", 2, "ep-two"
	sibling.Output = jobPath(sibling)
	if err := os.MkdirAll(filepath.Dir(sibling.Output), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sibling.Output, []byte("media"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Update(func(st *State) error {
		st.Jobs = append(st.Jobs, sibling)
		st.Sources = []Source{{ID: j.SourceID, API: server.URL}}
		st.Settings.EmbyEnabled, st.Settings.EmbyURL, st.Settings.EmbyKey = true, server.URL+"/emby", "test-key"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s.syncPosters(context.Background(), map[string]time.Time{})
	state := s.Store.Snapshot()
	if !state.Emby.Pending || state.Emby.Revision != 1 || images.Load() != 1 || details.Load() != 1 {
		t.Fatalf("历史补齐未按剧去重或未安排扫描: %+v images=%d details=%d", state.Emby, images.Load(), details.Load())
	}
	var saved []Job
	b, err := os.ReadFile(filepath.Join(s.Store.dir, "jobs.json"))
	if err != nil || json.Unmarshal(b, &saved) != nil || len(saved) != 2 || saved[0].Poster == "" || saved[1].Poster != saved[0].Poster {
		t.Fatal("历史任务的海报快照未原子保存到 jobs.json")
	}
	s.syncPosters(context.Background(), map[string]time.Time{})
	if images.Load() != 1 || details.Load() != 1 || s.Store.Snapshot().Emby.Revision != 1 {
		t.Fatal("重复补图请求了上游或重复触发扫描")
	}
	s.Store.Update(func(st *State) error { st.Emby.NextAttempt = time.Now(); return nil })
	s.syncEmby(context.Background())
	if scans.Load() != 1 || s.Store.Snapshot().Emby.Pending {
		t.Fatal("本地海报发布后未成功通知 Emby")
	}
}

func TestPosterFailureRetriesWithoutChangingCompletedMedia(t *testing.T) {
	s, j := jobFilesFixture(t)
	data := testPoster(t)
	var calls atomic.Int32
	var good atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if good.Load() {
			w.Write(data)
		} else {
			w.Write([]byte("<html>upstream failed</html>"))
		}
	}))
	defer server.Close()
	s.CMS = localTestCMS()
	s.Store.Update(func(st *State) error { st.Jobs[0].Poster = server.URL; return nil })
	retry := map[string]time.Time{}
	s.syncPosters(context.Background(), retry)
	if got := s.Store.Snapshot().Jobs[0]; got.Status != "completed" || got.Error != "" || got.Output != j.Output {
		t.Fatal("图片失败污染了已完成下载状态")
	}
	if _, err := os.Stat(filepath.Join(posterSeries(j), "poster.png")); !os.IsNotExist(err) {
		t.Fatal("无效图片被写入媒体目录")
	}
	s.syncPosters(context.Background(), retry)
	if calls.Load() != 1 {
		t.Fatal("海报失败没有退避")
	}
	good.Store(true)
	retry[posterSeries(j)] = time.Time{}
	s.syncPosters(context.Background(), retry)
	if calls.Load() != 2 || !s.Store.Snapshot().Emby.Pending {
		t.Fatal("恢复后未重试补齐图片")
	}
}

func TestPosterPreservesCustomImagesAndRejectsSymlinks(t *testing.T) {
	for _, scenario := range []string{"custom", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			s, j := jobFilesFixture(t)
			series := posterSeries(j)
			custom := filepath.Join(series, "folder.jpg")
			if scenario == "custom" {
				os.WriteFile(custom, []byte("user poster"), 0644)
				os.WriteFile(filepath.Join(series, "season01-poster.jpg"), []byte("user season"), 0644)
			} else if err := os.Symlink(filepath.Join(t.TempDir(), "outside.jpg"), custom); err != nil {
				t.Fatal(err)
			}
			n, err := s.syncPoster(context.Background(), []Job{j})
			if n != 0 || (scenario == "symlink") != (err != nil) {
				t.Fatalf("已有海报保护失败: n=%d err=%v", n, err)
			}
			if scenario == "custom" {
				b, _ := os.ReadFile(custom)
				if string(b) != "user poster" {
					t.Fatal("覆盖了手工海报")
				}
			}
		})
	}
}

func TestDeleteWhilePosterDownloadsDoesNotPublish(t *testing.T) {
	s, j := jobFilesFixture(t)
	data := testPoster(t)
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.Write(data)
	}))
	defer server.Close()
	s.CMS = localTestCMS()
	j.Poster = server.URL
	s.Store.Update(func(st *State) error { st.Jobs[0].Poster = j.Poster; return nil })
	done := make(chan struct{})
	go func() { defer close(done); s.syncPoster(context.Background(), []Job{j}) }()
	<-entered
	err := s.DeleteJob(context.Background(), j.ID, true)
	close(release)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(posterSeries(j), "poster.png")); !os.IsNotExist(err) {
		t.Fatal("删除任务后后台又写回海报")
	}
}

func TestPosterWorkerStopsWithService(t *testing.T) {
	s, _ := jobFilesFixture(t)
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
	}))
	defer server.Close()
	s.CMS = localTestCMS()
	if err := s.Store.Update(func(st *State) error {
		st.Jobs[0].Poster = server.URL
		st.Sources = []Source{{ID: "source"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("启动后没有自动补图")
	}
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("关闭服务没有取消补图请求")
	}
}
