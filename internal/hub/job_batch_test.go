package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBatchPauseMixedStatesAndDuplicateIDs(t *testing.T) {
	s, completed := jobFilesFixture(t)
	queued := completed
	queued.ID, queued.Status, queued.Speed = "queued", "queued", "1 MB/s"
	if err := s.Store.Update(func(st *State) error { st.Jobs = append(st.Jobs, queued); return nil }); err != nil {
		t.Fatal(err)
	}
	results, err := s.BatchJobAction(context.Background(), []string{queued.ID, completed.ID, "missing", queued.ID}, "pause")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 || results[0].ID != queued.ID || !results[0].Success || results[1].Success || results[2].Error != "任务不存在" {
		t.Fatalf("批量结果未保留顺序、去重或区分失败：%+v", results)
	}
	jobs := s.Store.Snapshot().Jobs
	if jobs[0].Status != "completed" || jobs[1].Status != "paused" || jobs[1].Speed != "" {
		t.Fatalf("混合状态暂停错误：%+v", jobs)
	}
}

func TestBatchResumeResetsRetryState(t *testing.T) {
	s, completed := jobFilesFixture(t)
	ids := []string{}
	if err := s.Store.Update(func(st *State) error {
		for _, status := range []string{"paused", "failed", "canceled"} {
			job := completed
			job.ID, job.Status, job.Attempts, job.Error = status, status, 3, "上次下载失败"
			job.NextAttempt, job.WaitingForSpace = time.Now().Add(time.Hour), true
			st.Jobs = append(st.Jobs, job)
			ids = append(ids, job.ID)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	results, err := s.BatchJobAction(context.Background(), ids, "resume")
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		if !result.Success {
			t.Fatalf("可重试任务被拒绝：%+v", result)
		}
	}
	for _, job := range s.Store.Snapshot().Jobs[1:] {
		if job.Status != "queued" || job.Attempts != 0 || job.Error != "" || !job.NextAttempt.IsZero() || job.WaitingForSpace {
			t.Fatalf("重试遗留了失败或等待状态：%+v", job)
		}
	}
}

func TestBatchDeleteKeepsFilesAndPersistsSuccess(t *testing.T) {
	s, job := jobFilesFixture(t)
	results, err := s.BatchJobAction(context.Background(), []string{job.ID, "missing", job.ID}, "remove")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || !results[0].Success || results[1].Success || results[1].Error != "任务不存在" {
		t.Fatalf("删除部分成功结果错误：%+v", results)
	}
	for _, path := range []string{job.Output, job.Output + ".moviehub.json", strings.TrimSuffix(job.Output, ".mp4") + ".nfo", filepath.Join(job.MediaDir, ".moviehub", job.ID, "attempt", "part.ts")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("默认批量删除误操作文件：%s %v", path, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(s.Store.dir, "jobs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var jobs []Job
	if err := json.Unmarshal(data, &jobs); err != nil || len(jobs) != 0 || len(s.Store.Snapshot().Jobs) != 0 || s.Store.Snapshot().Emby.Pending {
		t.Fatalf("删除结果未持久化或错误触发扫描：%s %v", data, err)
	}
}

func TestBatchDeleteFilesContinuesAfterForeignReceipt(t *testing.T) {
	s, job := jobFilesFixture(t)
	foreign := job
	foreign.ID, foreign.Episode.Key, foreign.Episode.Number = "foreign", "foreign-episode", 2
	foreign.Output = jobPath(foreign)
	if err := os.WriteFile(foreign.Output, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeReceiptNoClobber(foreign.Output+".moviehub.json", receipt{JobID: "another-job"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Update(func(st *State) error { st.Jobs = append(st.Jobs, foreign); return nil }); err != nil {
		t.Fatal(err)
	}
	results, err := s.BatchJobAction(context.Background(), []string{foreign.ID, job.ID}, "remove-files")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Success || !strings.Contains(results[0].Error, "入库凭据不属于本任务") || !results[1].Success {
		t.Fatalf("归属失败阻止了其他任务：%+v", results)
	}
	for _, path := range []string{job.Output, job.Output + ".moviehub.json", strings.TrimSuffix(job.Output, ".mp4") + ".nfo", filepath.Join(job.MediaDir, ".moviehub", job.ID)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("成功任务未清理文件：%s %v", path, err)
		}
	}
	for _, path := range []string{foreign.Output, foreign.Output + ".moviehub.json", filepath.Join(filepath.Dir(filepath.Dir(job.Output)), "tvshow.nfo")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("误删归属不明或共享文件：%s %v", path, err)
		}
	}
	state := s.Store.Snapshot()
	if len(state.Jobs) != 1 || state.Jobs[0].ID != foreign.ID || state.Jobs[0].Error != results[0].Error || !state.Emby.Pending {
		t.Fatalf("失败记录或扫描状态错误：%+v", state)
	}
}

func TestBatchDeleteWaitTimeoutRetainsJobForRetry(t *testing.T) {
	s, job := jobFilesFixture(t)
	other := job
	other.ID, other.Status = "other", "paused"
	other.Episode.Key, other.Episode.Number = "other-episode", 2
	other.Output = jobPath(other)
	if err := s.Store.Update(func(st *State) error {
		st.Jobs[0].Status = "downloading"
		st.Jobs = append(st.Jobs, other)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	s.workers[job.ID] = done
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	results, err := s.BatchJobAction(ctx, []string{job.ID, other.ID}, "remove-files")
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Success || !strings.Contains(results[0].Error, "下载仍在停止") || !results[1].Success {
		t.Fatalf("下载停止超时未隔离到单项：%+v", results)
	}
	state := s.Store.Snapshot()
	if len(state.Jobs) != 1 || state.Jobs[0].ID != job.ID || state.Jobs[0].Status != "canceled" || s.deleting[job.ID] {
		t.Fatalf("停止超时移除任务或遗留删除锁：%+v", state.Jobs)
	}
	if _, err := os.Stat(job.Output); err != nil {
		t.Fatalf("worker 退出前删除了媒体：%v", err)
	}
	close(done)
	results, err = s.BatchJobAction(context.Background(), []string{job.ID}, "remove-files")
	if err != nil || !results[0].Success || len(s.Store.Snapshot().Jobs) != 0 {
		t.Fatalf("worker 退出后无法重试：%+v %v", results, err)
	}
}

func TestBatchCanceledRequestDoesNotStartActions(t *testing.T) {
	s, job := jobFilesFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results, err := s.BatchJobAction(ctx, []string{job.ID, "missing"}, "remove-files")
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		if result.Success || !strings.Contains(result.Error, "任务未处理") {
			t.Fatalf("取消请求仍开始操作：%+v", result)
		}
	}
	if len(s.Store.Snapshot().Jobs) != 1 {
		t.Fatal("取消请求删除了任务记录")
	}
	if _, err := os.Stat(job.Output); err != nil {
		t.Fatalf("取消请求删除了媒体：%v", err)
	}
}

func TestBatchJobHTTPValidationAndPartialResults(t *testing.T) {
	s, job := jobFilesFixture(t)
	handler := s.Handler()
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"空选择", `{"ids":[],"action":"remove"}`, http.StatusBadRequest},
		{"先验证所有ID", `{"ids":["job-one"," "],"action":"remove"}`, http.StatusBadRequest},
		{"未知动作", `{"ids":["job-one"],"action":"cancel"}`, http.StatusBadRequest},
		{"未知字段", `{"ids":["job-one"],"action":"remove","all":true}`, http.StatusBadRequest},
		{"畸形JSON", `{"ids":`, http.StatusBadRequest},
		{"部分成功", `{"ids":["job-one","missing","job-one"],"action":"remove"}`, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/jobs/batch", strings.NewReader(tc.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.want {
				t.Fatalf("HTTP %d，期望 %d：%s", response.Code, tc.want, response.Body.String())
			}
			if tc.want == http.StatusBadRequest {
				if len(s.Store.Snapshot().Jobs) != 1 {
					t.Fatal("无效批量请求部分执行了删除")
				}
				return
			}
			var body struct {
				Results []JobActionResult `json:"results"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || len(body.Results) != 2 || body.Results[0].ID != job.ID || !body.Results[0].Success || body.Results[1].Error != "任务不存在" {
				t.Fatalf("接口未逐项返回结果：%s %v", response.Body.String(), err)
			}
		})
	}
}
