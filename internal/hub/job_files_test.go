package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func jobFilesFixture(t *testing.T) (*Service, Job) {
	t.Helper()
	store, err := OpenStore(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(store)
	t.Cleanup(func() { s.Close(); store.Close() })
	job := Job{ID: "job-one", Title: "测试剧", SourceID: "source", VideoID: "video", Season: 1, Status: "completed", Episode: Episode{Key: "ep-one", Number: 1}, MediaDir: store.Snapshot().Settings.MediaDir}
	job.Output = jobPath(job)
	if err := os.MkdirAll(filepath.Dir(job.Output), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(job.Output, []byte("0123456789"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeReceiptNoClobber(job.Output+".moviehub.json", receipt{JobID: job.ID}); err != nil {
		t.Fatal(err)
	}
	if err := writeNFO(job); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(job.MediaDir, ".moviehub", job.ID, "attempt"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(job.MediaDir, ".moviehub", job.ID, "attempt", "part.ts"), []byte("part"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(st *State) error { st.Jobs = []Job{job}; return nil }); err != nil {
		t.Fatal(err)
	}
	return s, job
}

func TestDeleteJobKeepFiles(t *testing.T) {
	s, job := jobFilesFixture(t)
	if err := s.DeleteJob(context.Background(), job.ID, false); err != nil {
		t.Fatal(err)
	}
	if len(s.Store.Snapshot().Jobs) != 0 {
		t.Fatal("任务未删除")
	}
	for _, path := range []string{job.Output, job.Output + ".moviehub.json", strings.TrimSuffix(job.Output, ".mp4") + ".nfo", filepath.Join(job.MediaDir, ".moviehub", job.ID, "attempt", "part.ts")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("只删记录却操作了文件：%s %v", path, err)
		}
	}
	if s.Store.Snapshot().Emby.Pending {
		t.Fatal("保留媒体时不应触发删除扫描")
	}
}

func TestDeleteJobFilesKeepsOtherEpisodes(t *testing.T) {
	s, job := jobFilesFixture(t)
	sibling := filepath.Join(filepath.Dir(job.Output), "other.mp4")
	if err := os.WriteFile(sibling, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteJob(context.Background(), job.ID, true); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{job.Output, job.Output + ".moviehub.json", strings.TrimSuffix(job.Output, ".mp4") + ".nfo", filepath.Join(job.MediaDir, ".moviehub", job.ID)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("任务文件未删除：%s %v", path, err)
		}
	}
	for _, path := range []string{sibling, filepath.Join(filepath.Dir(filepath.Dir(job.Output)), "tvshow.nfo")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("误删共享或其他媒体：%s %v", path, err)
		}
	}
	st := s.Store.Snapshot()
	if len(st.Jobs) != 0 || !st.Emby.Pending || st.Emby.Revision != 1 {
		t.Fatalf("删除或扫描状态错误：%+v", st.Emby)
	}
}

func TestDeleteJobWaitsForDownloadExit(t *testing.T) {
	s, job := jobFilesFixture(t)
	started := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	defer upstream.Close()
	if err := s.Store.Update(func(st *State) error {
		st.Jobs[0].Status = "queued"
		st.Jobs[0].Output = ""
		st.Jobs[0].Episode.Key = "ep-two"
		st.Sources = []Source{{ID: job.SourceID, API: upstream.URL, Enabled: true}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s.CMS = localTestCMS()
	s.dispatch()
	<-started
	if err := s.DeleteJob(context.Background(), job.ID, false); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	workers, active := len(s.workers), len(s.active)
	s.mu.Unlock()
	if workers != 0 || active != 0 || len(s.Store.Snapshot().Jobs) != 0 {
		t.Fatal("未等下载 worker 退出就删除记录")
	}
}

func TestDeleteJobRejectsForeignReceiptAndSymlink(t *testing.T) {
	for _, scenario := range []string{"foreign", "symlink", "path"} {
		t.Run(scenario, func(t *testing.T) {
			s, job := jobFilesFixture(t)
			switch scenario {
			case "foreign":
				if err := atomicJSON(job.Output+".moviehub.json", receipt{JobID: "other-job"}); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				outside := t.TempDir()
				if err := os.Rename(filepath.Join(job.MediaDir, ".moviehub", job.ID), filepath.Join(outside, "keep")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(outside, "keep"), filepath.Join(job.MediaDir, ".moviehub", job.ID)); err != nil {
					t.Fatal(err)
				}
			case "path":
				if err := s.Store.Update(func(st *State) error { st.Jobs[0].Episode.Key = "../../../escape"; return nil }); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.DeleteJob(context.Background(), job.ID, true); err == nil {
				t.Fatal("允许删除归属不明或越界文件")
			}
			if len(s.Store.Snapshot().Jobs) != 1 {
				t.Fatal("删除失败却移除了记录")
			}
			if _, err := os.Stat(job.Output); err != nil {
				t.Fatal("误删媒体")
			}
		})
	}
}

func TestJobMediaRangeHeadAndAccess(t *testing.T) {
	s, job := jobFilesFixture(t)
	handler := s.Handler()
	for _, tc := range []struct {
		method, rangeValue string
		status             int
		body               string
	}{
		{"GET", "bytes=2-5", 206, "2345"}, {"HEAD", "", 200, ""}, {"GET", "bytes=30-40", 416, "invalid range: failed to overlap\n"},
	} {
		request := httptest.NewRequest(tc.method, "http://127.0.0.1/api/jobs/"+job.ID+"/media", nil)
		request.Header.Set("Range", tc.rangeValue)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != tc.status || response.Body.String() != tc.body {
			t.Fatalf("MP4 Range/HEAD错误：%d %q", response.Code, response.Body.String())
		}
		if tc.status == 206 && (response.Header().Get("Content-Range") != "bytes 2-5/10" || response.Header().Get("Content-Type") != "video/mp4") {
			t.Fatal("缺少MP4范围响应头")
		}
	}
	if err := s.Store.Update(func(st *State) error { st.Jobs[0].Status = "failed"; return nil }); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "http://127.0.0.1/api/jobs/"+job.ID+"/media", nil))
	if response.Code != 404 {
		t.Fatal("未完成任务也能播放文件")
	}
	if err := s.Store.Update(func(st *State) error { st.Jobs[0].Status = "completed"; st.Jobs[0].Output = "/etc/passwd"; return nil }); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "http://127.0.0.1/api/jobs/"+job.ID+"/media", nil))
	if response.Code != 404 {
		t.Fatal("读取了非归档路径")
	}
}
