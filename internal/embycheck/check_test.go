package embycheck

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

func targetFixture(t *testing.T) Target {
	t.Helper()
	name := filepath.Join(t.TempDir(), "series-id", "Season 01", "S01E001.mp4")
	if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte("media"), 0600); err != nil {
		t.Fatal(err)
	}
	return Target{ID: "target", LocalPath: name, Suffix: "series-id/Season 01/S01E001.mp4"}
}

func fileItem(target Target, id string) Item {
	return Item{ID: id, Type: "Episode", Path: "/container/" + target.Suffix,
		MediaSources: []Source{{Protocol: "File", Path: "/container/" + target.Suffix}}}
}

func TestDelayedRecognitionAndRecovery(t *testing.T) {
	target := targetFixture(t)
	now := time.Now()
	target.Previous = Pending(now)
	waiting := Evaluate(target, nil, now.Add(time.Minute))
	if waiting.State != "waiting" || waiting.ItemID != "" {
		t.Fatal(waiting)
	}
	target.Previous = waiting
	late := Evaluate(target, nil, now.Add(11*time.Minute))
	if late.State != "error" || late.Error == "" {
		t.Fatal(late)
	}
	target.Previous = late
	confirmed := Evaluate(target, []Item{fileItem(target, "emby-id")}, now.Add(12*time.Minute))
	if confirmed.State != "confirmed" || confirmed.ItemID != "emby-id" || confirmed.Error != "" || !confirmed.WaitingSince.IsZero() {
		t.Fatal("scan completion did not recover the waiting target", confirmed)
	}
	if !confirmed.NextCheckAt.Equal(now.Add(17 * time.Minute)) {
		t.Fatal("confirmed item was not scheduled for recheck")
	}
}

func TestProtocolIdentityAndDuplicates(t *testing.T) {
	for _, scenario := range []string{"http", "missing-source", "wrong-source", "other-series", "duplicate", "mixed-duplicate", "windows-mount", "missing-local", "symlink-local"} {
		t.Run(scenario, func(t *testing.T) {
			target := targetFixture(t)
			item := fileItem(target, "one")
			items := []Item{item}
			want := "error"
			switch scenario {
			case "http":
				items[0].MediaSources[0].Protocol = "Http"
			case "missing-source":
				items[0].MediaSources = nil
			case "wrong-source":
				items[0].MediaSources[0].Path = "/other/video.mp4"
			case "other-series":
				items[0].Path = "/another/Season 01/S01E001.mp4"
				want = "waiting"
			case "duplicate":
				items = append(items, fileItem(target, "two"))
			case "mixed-duplicate":
				other := fileItem(target, "two")
				other.MediaSources = nil
				items = append(items, other)
			case "windows-mount":
				items[0].Path = `D:\media\` + strings.ReplaceAll(target.Suffix, "/", `\`)
				items[0].MediaSources[0].Path = items[0].Path
				want = "confirmed"
			case "missing-local":
				if err := os.Remove(target.LocalPath); err != nil {
					t.Fatal(err)
				}
			case "symlink-local":
				other := target.LocalPath + ".real"
				if err := os.Rename(target.LocalPath, other); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, target.LocalPath); err != nil {
					t.Fatal(err)
				}
			}
			if got := Evaluate(target, items, time.Now()); got.State != want {
				t.Fatal(scenario, got)
			}
		})
	}
}

func TestQueryPagesAndSafeErrors(t *testing.T) {
	for _, scenario := range []string{"complete", "empty-page", "repeated-page", "http-error", "invalid", "redirect"} {
		t.Run(scenario, func(t *testing.T) {
			target := targetFixture(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/Items" || r.Header.Get("X-Emby-Token") != "private-key" || strings.Contains(r.URL.RawQuery, "private-key") {
					t.Error("query leaked credentials or used wrong route", r.URL.Path)
				}
				switch scenario {
				case "http-error":
					w.WriteHeader(503)
					return
				case "invalid":
					w.Write([]byte(`{"unexpected":true}`))
					return
				case "redirect":
					w.Header().Set("Location", "/redirected")
					w.WriteHeader(302)
					return
				}
				items := []Item{fileItem(target, "one")}
				if r.URL.Query().Get("StartIndex") != "0" {
					switch scenario {
					case "complete":
						items = []Item{{ID: "other", Type: "Episode", Path: "/other/media.mp4"}}
					case "empty-page":
						items = []Item{}
					}
				}
				json.NewEncoder(w).Encode(map[string]any{"Items": items, "TotalRecordCount": 2})
			}))
			defer server.Close()
			results, err := Check(context.Background(), server.URL, "private-key", []Target{target})
			got := results[target.ID]
			if scenario == "complete" {
				if err != nil || got.State != "confirmed" {
					t.Fatal(got, err)
				}
			} else if err == nil || got.State != "error" || strings.Contains(got.Error, "private-key") || strings.Contains(got.Error, server.URL) {
				t.Fatal("partial or failed query falsely confirmed or exposed secrets", got, err)
			}
		})
	}
}
