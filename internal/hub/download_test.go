package hub

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPublishKeepsMP4CompletionTime(t *testing.T) {
	for _, recoverExisting := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "recovered"}[recoverExisting], func(t *testing.T) {
			s, job := jobFilesFixture(t)
			completed := time.Date(2026, 10, 14, 12, 34, 56, 0, time.UTC)
			from := filepath.Join(t.TempDir(), "merged.mp4")
			if recoverExisting {
				from = job.Output
			} else {
				if err := os.Remove(job.Output); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(job.Output + ".moviehub.json"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(from, []byte("merged video"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Chtimes(from, completed, completed); err != nil {
				t.Fatal(err)
			}
			if err := s.Store.Update(func(st *State) error { st.Jobs[0].Status = "downloading"; return nil }); err != nil {
				t.Fatal(err)
			}
			if recoverExisting {
				from = ""
			}
			if err := s.publish(context.Background(), job, from, s.Store.Snapshot().Settings); err != nil {
				t.Fatal(err)
			}
			got := s.Store.Snapshot().Jobs[0]
			if got.Status != "completed" || !got.CompletedAt.Equal(completed) {
				t.Fatalf("completion time changed during publication: %+v", got)
			}
		})
	}
}

func TestReceiptIsNeverOverwritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "episode.moviehub.json")
	first := receipt{JobID: "first", SHA256: "a"}
	if err := writeReceiptNoClobber(path, first); err != nil {
		t.Fatal(err)
	}
	if err := writeReceiptNoClobber(path, receipt{JobID: "second", SHA256: "b"}); err == nil {
		t.Fatal("existing receipt was overwritten")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got receipt
	if err = json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got != first {
		t.Fatalf("receipt changed: %+v", got)
	}
}

func TestRecoverOnlyRemovesReceiptForSameJobWithoutMedia(t *testing.T) {
	dir := t.TempDir()
	j := Job{ID: "job-one", Title: "测试剧", SourceID: "source", VideoID: "video", Season: 1, Episode: Episode{Key: "ep", Number: 1}, MediaDir: dir}
	receiptPath := jobPath(j) + ".moviehub.json"
	if err := os.MkdirAll(filepath.Dir(receiptPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(receiptPath, receipt{JobID: j.ID, SHA256: "partial"}); err != nil {
		t.Fatal(err)
	}
	if err := recoverStaleReceipt(j); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(receiptPath); !os.IsNotExist(err) {
		t.Fatalf("same-job stale receipt remains: %v", err)
	}
	if err := atomicJSON(receiptPath, receipt{JobID: "other-job", SHA256: "keep"}); err != nil {
		t.Fatal(err)
	}
	if err := recoverStaleReceipt(j); err == nil {
		t.Fatal("receipt belonging to another job was removed")
	}
	if _, err := os.Stat(receiptPath); err != nil {
		t.Fatalf("foreign receipt was removed: %v", err)
	}
}
