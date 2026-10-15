package hub

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSplitStoreAndExclusiveLock(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, filepath.Join(dir, "media"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = OpenStore(dir, filepath.Join(dir, "media")); err == nil {
		t.Fatal("second process acquired lock")
	}
	for _, name := range stateFiles {
		if _, err = os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 8; n++ {
				if e := s.Update(func(st *State) error { st.Emby.Revision++; return nil }); e != nil {
					t.Error(e)
					return
				}
			}
		}()
	}
	wg.Wait()
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := reopened.Snapshot().Emby.Revision; got != 96 {
		t.Fatalf("lost concurrent updates: %d", got)
	}
	if _, err = os.Stat(filepath.Join(dir, "emby.json.bak")); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dir, "jobs.json.bak")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unchanged jobs should not have backup: %v", err)
	}
}

func TestStoreMigrationPreservesLegacyAndSections(t *testing.T) {
	dir := t.TempDir()
	legacy := defaultState("/old/media")
	legacy.Settings.Workers = 4
	legacy.Sources = []Source{{ID: "first", Name: "First", API: "https://example.org/api", Enabled: true}}
	legacy.SourcesUpdatedAt = time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	legacy.Follows = []Follow{{ID: "follow-1", Enabled: true}}
	legacy.Jobs = []Job{{ID: "job-1", Status: "queued"}}
	legacy.Emby = EmbyState{Pending: true, Revision: 7}
	legacyPath := filepath.Join(dir, "state.json")
	if err := atomicJSON(legacyPath, legacy); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir, "/new/media")
	if err != nil {
		t.Fatal(err)
	}
	got := s.Snapshot()
	if got.Settings.Workers != 4 || got.Settings.MediaDir != "/old/media" || len(got.Sources) != 1 || len(got.Follows) != 1 || len(got.Jobs) != 1 || got.Emby.Revision != 7 || !got.SourcesUpdatedAt.Equal(legacy.SourcesUpdatedAt) {
		t.Fatalf("legacy migration lost data: %+v", got)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(legacyPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("legacy file changed: %v", err)
	}
	for _, name := range stateFiles {
		if _, err = os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	var src sourcesStateFile
	b, err := os.ReadFile(filepath.Join(dir, "sources.json"))
	if err != nil || json.Unmarshal(b, &src) != nil || !src.SourcesUpdatedAt.Equal(legacy.SourcesUpdatedAt) {
		t.Fatalf("source timestamp missing: %v", err)
	}
}

func TestUpdateRollbackAndCorruptSectionPreserved(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Update(func(st *State) error { st.Emby.Revision = 9; return errors.New("stop") })
	if err == nil || s.Snapshot().Emby.Revision != 0 {
		t.Fatal("callback rollback failed")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "jobs.json")
	if err = os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenStore(dir, dir); err == nil {
		t.Fatal("corrupt section must fail closed")
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "{" {
		t.Fatalf("corrupt original was modified: %v", err)
	}
}

func TestTransactionRecoveryAcrossSections(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	state := defaultState(dir)
	state.Follows = []Follow{{ID: "follow-2", Enabled: true}}
	state.Jobs = []Job{{ID: "job-2", Status: "queued"}}
	sections, err := sectionJSON(state)
	if err != nil {
		t.Fatal(err)
	}
	// long: 模拟只写完追更文件就崩溃；重启必须补齐队列，不能留下半次自动入队结果。
	tx := stateTransaction{Version: 1, Files: map[string]json.RawMessage{
		"follows.json": sections["follows.json"], "jobs.json": sections["jobs.json"],
	}}
	if err = atomicJSON(filepath.Join(dir, transactionFile), tx); err != nil {
		t.Fatal(err)
	}
	if err = atomicWrite(filepath.Join(dir, "follows.json"), sections["follows.json"], 0600); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got := s.Snapshot()
	if len(got.Follows) != 1 || len(got.Jobs) != 1 {
		t.Fatalf("transaction not fully replayed: %+v", got)
	}
	if _, err = os.Stat(filepath.Join(dir, transactionFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("transaction journal should be removed: %v", err)
	}
}

func TestMultiSectionUpdateAndInvalidJournal(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Update(func(st *State) error {
		st.Follows = append(st.Follows, Follow{ID: "f", Enabled: true})
		st.Jobs = append(st.Jobs, Job{ID: "j", Status: "queued"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"follows.json", "jobs.json"} {
		if _, err = os.Stat(filepath.Join(dir, name+".bak")); err != nil {
			t.Fatalf("missing %s backup: %v", name, err)
		}
	}
	jobsPath := filepath.Join(dir, "jobs.json")
	before, err := os.ReadFile(jobsPath)
	if err != nil {
		t.Fatal(err)
	}
	bad := stateTransaction{Version: 1, Files: map[string]json.RawMessage{
		"follows.json": json.RawMessage(`[]`),
		"jobs.json":    json.RawMessage(`{}`),
	}}
	if err = atomicJSON(filepath.Join(dir, transactionFile), bad); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenStore(dir, dir); err == nil {
		t.Fatal("invalid journal must fail before changing files")
	}
	after, err := os.ReadFile(jobsPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("invalid journal changed jobs: %v", err)
	}
}

func TestIncompleteSectionsAndBrokenLegacyFailClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(dir, dir); err == nil {
		t.Fatal("corrupt legacy state must fail")
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(dir, dir); err == nil {
		t.Fatal("partial split state must fail")
	}
	if _, err := os.Stat(filepath.Join(dir, "sources.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial state was overwritten: %v", err)
	}
}
