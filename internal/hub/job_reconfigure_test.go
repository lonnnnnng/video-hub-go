package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func reconfigureFixture(t *testing.T) (*Service, Job, Video) {
	t.Helper()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "list": []map[string]string{{"vod_id": "1", "vod_name": "测试剧", "vod_play_from": "A$$$B", "vod_play_url": "第1集$https://cdn.example/a.m3u8$$$01$https://cdn.example/b.m3u8"}}})
	}))
	t.Cleanup(source.Close)
	store, err := OpenStore(filepath.Join(t.TempDir(), "data"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Update(func(st *State) error {
		st.Sources = []Source{{ID: "source", API: source.URL, Enabled: true}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s := NewService(store)
	s.CMS = localTestCMS()
	video, err := s.Detail(context.Background(), "source", "1")
	if err != nil || len(video.Lines) != 2 {
		t.Fatal("两线路夹具无效", err)
	}
	job := Job{ID: "job-1", SourceID: "source", VideoID: "1", Title: video.Title, LineID: video.Lines[0].ID, Episode: video.Lines[0].Episodes[0], Season: 1, Status: "failed", Attempts: 3, Error: "旧线路失败", NextAttempt: time.Now().Add(time.Hour), Progress: .5, MediaDir: store.Snapshot().Settings.MediaDir, UpdatedAt: time.Now()}
	if err := store.Update(func(st *State) error { st.Jobs = []Job{job}; return nil }); err != nil {
		t.Fatal(err)
	}
	return s, job, video
}

func TestJobReconfigureRetryRefreshesLineAndSeason(t *testing.T) {
	s, old, video := reconfigureFixture(t)
	input := JobReconfigure{LineID: video.Lines[1].ID, EpisodeKey: video.Lines[1].Episodes[0].Key, Season: 3, Retry: true, ExpectedUpdatedAt: old.UpdatedAt}
	body, _ := json.Marshal(input)
	r := httptest.NewRequest(http.MethodPost, "http://192.168.1.2:4444/api/jobs/"+old.ID+"/reconfigure", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	got := s.Store.Snapshot().Jobs[0]
	if got.ID != old.ID || got.LineID != input.LineID || got.Episode.URL != "https://cdn.example/b.m3u8" || got.Season != 3 || got.Status != "queued" || got.Attempts != 0 || got.Progress != 0 || got.Error != "" || !got.NextAttempt.IsZero() {
		t.Fatal("任务未按新线路恢复", got)
	}
	dir := s.Store.dir
	if err := s.Store.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenStore(dir, old.MediaDir)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if restored.Snapshot().Jobs[0].LineID != input.LineID || restored.Snapshot().Jobs[0].Season != 3 {
		t.Fatal("线路调整未持久化")
	}
}

func TestJobReconfigureProtectsPublishedBusyAndDuplicateTasks(t *testing.T) {
	for _, kind := range []string{"completed", "active", "worker-exiting", "published-failed", "receipt", "target-exists", "duplicate", "invalid-episode", "invalid-season"} {
		t.Run(kind, func(t *testing.T) {
			s, old, video := reconfigureFixture(t)
			input := JobReconfigure{LineID: video.Lines[1].ID, EpisodeKey: video.Lines[1].Episodes[0].Key, Season: 2, Retry: true, ExpectedUpdatedAt: old.UpdatedAt}
			if kind == "active" {
				s.active[old.ID] = func() {}
			}
			if kind == "worker-exiting" {
				s.workers[old.ID] = make(chan struct{})
			}
			if kind == "invalid-episode" {
				input.EpisodeKey = "gone"
			}
			if kind == "invalid-season" {
				input.Season = 0
			}
			if err := s.Store.Update(func(st *State) error {
				if kind == "completed" {
					st.Jobs[0].Status = "completed"
				}
				if kind == "duplicate" {
					other := old
					other.ID = "other"
					other.Season = input.Season
					other.Episode = video.Lines[1].Episodes[0]
					st.Jobs = append(st.Jobs, other)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			path := ""
			if kind == "published-failed" {
				path = jobPath(old)
			}
			if kind == "receipt" {
				path = jobPath(old) + ".moviehub.json"
			}
			if kind == "target-exists" {
				target := old
				target.Season = input.Season
				target.Episode = video.Lines[1].Episodes[0]
				path = jobPath(target)
			}
			if path != "" {
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("user file"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := s.Store.Snapshot().Jobs
			if err := s.ReconfigureJob(context.Background(), old.ID, input); err == nil {
				t.Fatal("不安全的调整被接受")
			}
			if !reflect.DeepEqual(before, s.Store.Snapshot().Jobs) {
				t.Fatal("拒绝调整却修改任务")
			}
			if path != "" {
				body, err := os.ReadFile(path)
				if err != nil || string(body) != "user file" {
					t.Fatal("原文件被改变", err)
				}
			}
		})
	}
}

type reconfigureTransport func(*http.Request) (*http.Response, error)

func (transport reconfigureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return transport(r)
}

func TestJobReconfigureDoesNotOverwriteConcurrentCancellation(t *testing.T) {
	s, old, video := reconfigureFixture(t)
	requested, resume := make(chan struct{}, 1), make(chan struct{})
	s.CMS.Client.Transport = reconfigureTransport(func(r *http.Request) (*http.Response, error) {
		select {
		case requested <- struct{}{}:
		default:
		}
		<-resume
		return http.DefaultTransport.RoundTrip(r)
	})
	done := make(chan error, 1)
	go func() {
		done <- s.ReconfigureJob(context.Background(), old.ID, JobReconfigure{LineID: video.Lines[1].ID, EpisodeKey: video.Lines[1].Episodes[0].Key, Season: 2, Retry: true, ExpectedUpdatedAt: old.UpdatedAt})
	}()
	<-requested
	if err := s.JobAction(old.ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	close(resume)
	if err := <-done; err == nil {
		t.Fatal("旧窗口覆盖了用户取消")
	}
	got := s.Store.Snapshot().Jobs[0]
	if got.Status != "canceled" || got.Season != 1 || got.LineID != old.LineID {
		t.Fatal("并发取消未保留", got)
	}
}

func TestJobReconfigureSaveFailureKeepsOriginal(t *testing.T) {
	s, old, video := reconfigureFixture(t)
	before := s.Store.Snapshot().Jobs
	dir := s.Store.dir
	path := filepath.Join(t.TempDir(), "not-directory")
	if err := os.WriteFile(path, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	s.Store.dir = path
	defer func() { s.Store.dir = dir }()
	if err := s.ReconfigureJob(context.Background(), old.ID, JobReconfigure{LineID: video.Lines[1].ID, EpisodeKey: video.Lines[1].Episodes[0].Key, Season: 2, Retry: true, ExpectedUpdatedAt: old.UpdatedAt}); err == nil {
		t.Fatal("状态保存失败仍接受调整")
	}
	if !reflect.DeepEqual(before, s.Store.Snapshot().Jobs) {
		t.Fatal("保存失败改写内存任务")
	}
}

func TestJobReconfigureRejectsStaleWindowAfterCancellation(t *testing.T) {
	s, old, video := reconfigureFixture(t)
	input := JobReconfigure{LineID: video.Lines[1].ID, EpisodeKey: video.Lines[1].Episodes[0].Key, Season: 2, Retry: true, ExpectedUpdatedAt: old.UpdatedAt}
	if err := s.JobAction(old.ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconfigureJob(context.Background(), old.ID, input); err == nil {
		t.Fatal("旧调整窗口重新启动已取消任务")
	}
	input.ExpectedUpdatedAt = s.Store.Snapshot().Jobs[0].UpdatedAt
	if err := s.ReconfigureJob(context.Background(), old.ID, input); err != nil {
		t.Fatal("新打开的取消任务窗口不能明确重试", err)
	}
}
