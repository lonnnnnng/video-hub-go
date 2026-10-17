package hub

import (
	"bytes"
	"context"
	"io"
	"movie-hub-go/internal/diskspace"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestDownloadSpaceWaitDoesNotConsumeAttempt(t *testing.T) {
	s, job := jobFilesFixture(t)
	if err := os.Remove(job.Output); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Update(func(st *State) error { st.Jobs[0].Status = "queued"; return nil }); err != nil {
		t.Fatal(err)
	}
	s.spaceCheck = func(string, uint64) error {
		return &diskspace.WaitingError{Available: 1, Required: diskspace.MinFreeBytes}
	}
	s.dispatch()
	got := s.Store.Snapshot().Jobs[0]
	if got.Status != "queued" || !got.WaitingForSpace || got.Attempts != 0 || len(s.workers) != 0 || !got.NextAttempt.After(time.Now()) {
		t.Fatal(got)
	}
	s.spaceCheck = func(string, uint64) error { return nil }
	if err := s.Store.Update(func(st *State) error { st.Jobs[0].NextAttempt = time.Time{}; return nil }); err != nil {
		t.Fatal(err)
	}
	s.dispatch()
	s.wg.Wait()
	got = s.Store.Snapshot().Jobs[0]
	if got.WaitingForSpace || got.Attempts != 1 {
		t.Fatal("space recovery did not release the job", got)
	}
}

func TestSpaceWaitRecoversWhenStateCannotBeSaved(t *testing.T) {
	s, job := jobFilesFixture(t)
	if err := os.Remove(job.Output); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Update(func(st *State) error { st.Jobs[0].Status = "downloading"; st.Jobs[0].Attempts = 1; return nil }); err != nil {
		t.Fatal(err)
	}
	previous := s.Store.dir
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	s.Store.dir = blocked
	s.finishError(job, &diskspace.WaitingError{Available: 1, Required: diskspace.MinFreeBytes})
	if len(s.spaceWaiting) != 1 {
		t.Fatal("failed state save dropped space-wait recovery")
	}
	s.Store.dir = previous
	s.spaceCheck = func(string, uint64) error {
		return &diskspace.WaitingError{Available: 1, Required: diskspace.MinFreeBytes}
	}
	s.dispatch()
	got := s.Store.Snapshot().Jobs[0]
	if got.Status != "queued" || !got.WaitingForSpace || got.Attempts != 0 || len(s.spaceWaiting) != 0 {
		t.Fatal("space-wait recovery left download stuck", got)
	}
}

func TestSpaceWaitAtShutdownDoesNotConsumeAttempt(t *testing.T) {
	s, job := jobFilesFixture(t)
	if err := s.Store.Update(func(st *State) error { st.Jobs[0].Status = "downloading"; st.Jobs[0].Attempts = 1; return nil }); err != nil {
		t.Fatal(err)
	}
	s.cancel()
	s.finishError(job, &diskspace.WaitingError{Available: 1, Required: diskspace.MinFreeBytes})
	got := s.Store.Snapshot().Jobs[0]
	if got.Status != "queued" || !got.WaitingForSpace || got.Attempts != 0 {
		t.Fatal("shutdown consumed a space-wait attempt", got)
	}
}

func TestMuxSpaceWaitDoesNotAccumulateAttempts(t *testing.T) {
	_, job := jobFilesFixture(t)
	job.Status = "downloading"
	f := &hlsFetcher{client: &http.Client{Transport: hlsTransport(func(r *http.Request) (*http.Response, error) {
		body := []byte("abc")
		if filepath.Ext(r.URL.Path) == ".m3u8" {
			body = []byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXTINF:1,\na.ts\n#EXTINF:1,\nb.ts\n#EXT-X-ENDLIST\n")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header), Request: r}, nil
	})}, spaceCheck: func(_ string, reserve uint64) error {
		if reserve > 3 {
			return &diskspace.WaitingError{Available: diskspace.MinFreeBytes + 3, Required: diskspace.MinFreeBytes + reserve}
		}
		return nil
	}}
	for i := 0; i < 2; i++ {
		stage := filepath.Join(job.MediaDir, ".moviehub", job.ID, uid())
		if err := os.MkdirAll(stage, 0700); err != nil {
			t.Fatal(err)
		}
		err := downloadHLSWithFetcher(context.Background(), f, "https://example.com/index.m3u8", stage, filepath.Join(stage, "media.mp4"), "ffmpeg-must-not-run", 1, nil)
		if !diskspace.IsWaiting(err) {
			t.Fatal("mux did not reserve output space", err)
		}
		if err := discardSpaceAttempt(job, stage); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(stage); !os.IsNotExist(err) {
			t.Fatal("retry retained a full duplicate of the segments", err)
		}
	}
	if _, err := os.Stat(job.Output); err != nil {
		t.Fatal("retry cleanup touched published media", err)
	}
	if _, err := os.Stat(filepath.Join(job.MediaDir, ".moviehub", job.ID, "attempt", "part.ts")); err != nil {
		t.Fatal("retry cleanup touched a different attempt", err)
	}
}

func TestFFmpegDiskFullIsSpaceWait(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable fixture")
	}
	program := filepath.Join(t.TempDir(), "fake-ffmpeg")
	if err := os.WriteFile(program, []byte("#!/bin/sh\nprintf 'No space left on device' >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	stage := t.TempDir()
	f := &hlsFetcher{client: &http.Client{Transport: hlsTransport(func(r *http.Request) (*http.Response, error) {
		body := "bytes"
		if filepath.Ext(r.URL.Path) == ".m3u8" {
			body = "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXTINF:1,\na.ts\n#EXT-X-ENDLIST\n"
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header), Request: r}, nil
	})}}
	if err := downloadHLSWithFetcher(context.Background(), f, "https://example.com/index.m3u8", stage, filepath.Join(stage, "media.mp4"), program, 1, nil); !diskspace.IsWaiting(err) {
		t.Fatal("FFmpeg full disk consumed normal retries", err)
	}
}
