package hub

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func cleanupFixture(t *testing.T) (*Service, Job, string) {
	t.Helper()
	s, job := jobFilesFixture(t)
	hash, err := hashFile(job.Output)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(job.Output+".moviehub.json", receipt{JobID: job.ID, SHA256: hash}); err != nil {
		t.Fatal(err)
	}
	return s, job, filepath.Join(job.MediaDir, ".moviehub", job.ID)
}

func TestTemporaryCleanupPreservesPublishedHardLinkAndTask(t *testing.T) {
	s, job, temporary := cleanupFixture(t)
	if err := os.Link(job.Output, filepath.Join(temporary, "attempt", "media.mp4")); err != nil {
		t.Fatal(err)
	}
	s.cleanupCompletedTemporary(context.Background())
	if _, err := os.Stat(temporary); !os.IsNotExist(err) {
		t.Fatal("已完成任务的临时分片未清理", err)
	}
	for _, p := range []string{job.Output, job.Output + ".moviehub.json", strings.TrimSuffix(job.Output, ".mp4") + ".nfo", filepath.Join(filepath.Dir(filepath.Dir(job.Output)), "tvshow.nfo")} {
		if info, err := os.Stat(p); err != nil || info.Size() == 0 {
			t.Fatal("误删正式媒体或元数据", p, err)
		}
	}
	if st := s.Store.Snapshot(); len(st.Jobs) != 1 || st.Jobs[0].Status != "completed" || st.Jobs[0].CleanupError != "" {
		t.Fatal("清理影响任务完成状态", st.Jobs)
	}
}

func TestTemporaryCleanupProtectsRecoveryAndForeignPaths(t *testing.T) {
	for _, scenario := range []string{"changed", "foreign", "symlink", "missing-nfo", "canceled", "active"} {
		t.Run(scenario, func(t *testing.T) {
			s, job, tmp := cleanupFixture(t)
			ctx := context.Background()
			switch scenario {
			case "changed":
				if err := os.WriteFile(job.Output, []byte("edited media"), 0644); err != nil {
					t.Fatal(err)
				}
			case "foreign":
				if err := atomicJSON(job.Output+".moviehub.json", receipt{JobID: "foreign", SHA256: strings.Repeat("a", 64)}); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				outside := filepath.Join(t.TempDir(), "keep")
				if err := os.Rename(tmp, outside); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, tmp); err != nil {
					t.Fatal(err)
				}
			case "missing-nfo":
				if err := os.Remove(strings.TrimSuffix(job.Output, ".mp4") + ".nfo"); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "active":
				s.workers[job.ID] = make(chan struct{})
			}
			s.cleanupCompletedTemporary(ctx)
			if _, err := os.Stat(tmp); err != nil {
				t.Fatal("保护条件下仍清理了分片", err)
			}
			if len(s.Store.Snapshot().Jobs) != 1 {
				t.Fatal("清理移除了任务")
			}
			delete(s.workers, job.ID)
		})
	}
}
