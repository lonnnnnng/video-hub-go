package hub

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestAPILogOmitsSearch(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "data"), filepath.Join(dir, "media"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	defer slog.SetDefault(previous)
	request := httptest.NewRequest(http.MethodGet, "http://localhost/api/search?q=PRIVATE_SEARCH_MARKER", nil)
	response := httptest.NewRecorder()
	NewService(store).Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("search status = %d", response.Code)
	}
	logs := output.String()
	if !strings.Contains(logs, "GET /api/search") || strings.Contains(logs, "PRIVATE_SEARCH_MARKER") {
		t.Fatalf("API 日志包含敏感信息或缺少路由：%q", logs)
	}
}
