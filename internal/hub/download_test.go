package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

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
