package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestManualDefaultSourcePersists(t *testing.T) {
	s, _ := jobFilesFixture(t)
	if err := s.Store.Update(func(st *State) error {
		st.Sources = []Source{{ID: "fast", Enabled: true, Default: true, LatencyMS: 10}, {ID: "chosen", Enabled: true, LatencyMS: 50}, {ID: "disabled"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	handler := s.Handler()
	for _, tc := range []struct {
		id     string
		status int
	}{{"chosen", 200}, {"disabled", 400}, {"missing", 400}} {
		req := httptest.NewRequest("POST", "http://127.0.0.1:4444/api/sources/"+tc.id+"/default", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.id, res.Code, res.Body.String())
		}
	}
	state := s.Store.Snapshot()
	sortSources(state.Sources)
	chooseDefaultSource(state.Sources)
	for _, source := range state.Sources {
		if source.Default != (source.ID == "chosen") {
			t.Fatal("manual default overwritten")
		}
	}
	var saved sourcesStateFile
	data, err := os.ReadFile(s.Store.dir + "/sources.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.Sources[1].Default {
		t.Fatal("default not persisted")
	}
}
func TestOldCompletedSizeBackfill(t *testing.T) {
	s, _ := jobFilesFixture(t)
	if err := s.Store.Update(func(st *State) error { st.Sources = []Source{{ID: "source", Enabled: true, Default: true}}; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	if size := s.Store.Snapshot().Jobs[0].SizeBytes; size != 10 {
		t.Fatalf("size=%d", size)
	}
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4444/api/state", nil)
	res := httptest.NewRecorder()
	s.Handler().ServeHTTP(res, req)
	if !strings.Contains(res.Body.String(), `"sizeBytes":10`) {
		t.Fatal("size absent from state API")
	}
}
func TestWebUpdateRejectsUnauthorisedBodies(t *testing.T) {
	s, _ := jobFilesFixture(t)
	for _, tc := range []struct {
		origin, body string
		status       int
	}{{"", `{"version":"v99.0.0"}`, 400}, {"https://example.com", `{"version":"v99.0.0"}`, 403}, {"http://127.0.0.1:4444", `{"version":"v99.0.0","url":"https://example.com"}`, 400}, {"http://127.0.0.1:4444", `{"version":"v99.0.0"}`, 400}} {
		req := httptest.NewRequest("POST", "http://127.0.0.1:4444/api/update/apply", strings.NewReader(tc.body))
		req.Header.Set("Origin", tc.origin)
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		s.Handler().ServeHTTP(res, req)
		if res.Code != tc.status {
			t.Fatalf("%+v: %d %s", tc, res.Code, res.Body.String())
		}
	}
}
