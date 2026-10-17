package hub

import (
	"context"
	"encoding/json"
	"movie-hub-go/internal/embycheck"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestEmbyConfirmationSeparateFromDownloadAndRevision(t *testing.T) {
	for _, oldRequest := range []bool{false, true} {
		t.Run(map[bool]string{false: "delayed-scan", true: "changed-revision"}[oldRequest], func(t *testing.T) {
			s, job := jobFilesFixture(t)
			var recognized atomic.Bool
			started, release := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if oldRequest {
					close(started)
					<-release
				}
				items := []embycheck.Item{}
				if recognized.Load() {
					items = append(items, embycheck.Item{ID: "item-id", Type: "Episode", Path: job.Output, MediaSources: []embycheck.Source{{Path: job.Output, Protocol: "File"}}})
				}
				json.NewEncoder(w).Encode(map[string]any{"Items": items, "TotalRecordCount": len(items)})
			}))
			defer server.Close()
			if err := s.Store.Update(func(st *State) error {
				st.Settings.EmbyEnabled, st.Settings.EmbyURL, st.Settings.EmbyKey = true, server.URL, "test-key"
				st.Emby.Pending = false
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if oldRequest {
				recognized.Store(true)
				done := make(chan struct{})
				go func() { s.confirmEmbyMedia(context.Background()); close(done) }()
				<-started
				if err := s.RequestScan(); err != nil {
					close(release)
					t.Fatal(err)
				}
				close(release)
				<-done
				state := s.Store.Snapshot()
				if state.Jobs[0].EmbyMedia.State == "confirmed" || !state.Emby.Pending {
					t.Fatal("old query overwrote the new scan")
				}
				return
			}
			s.confirmEmbyMedia(context.Background())
			got := s.Store.Snapshot().Jobs[0]
			if got.Status != "completed" || got.EmbyMedia.State != "waiting" {
				t.Fatal("scan delay changed download result", got)
			}
			recognized.Store(true)
			if err := s.Store.Update(func(st *State) error { st.Jobs[0].EmbyMedia.NextCheckAt = time.Time{}; return nil }); err != nil {
				t.Fatal(err)
			}
			s.confirmEmbyMedia(context.Background())
			got = s.Store.Snapshot().Jobs[0]
			if got.Status != "completed" || got.EmbyMedia.State != "confirmed" || got.EmbyMedia.ItemID != "item-id" {
				t.Fatal(got)
			}
		})
	}
}
