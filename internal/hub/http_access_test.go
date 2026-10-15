package hub

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestLANAccessWithoutToken(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "data"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := NewService(store).Handler()
	for _, tc := range []struct {
		name, host, origin string
		want               int
	}{
		{"局域网地址免令牌", "192.168.1.2:4488", "", http.StatusOK},
		{"跨站请求被拒绝", "192.168.1.2:4488", "http://example.com", http.StatusForbidden},
		{"域名重绑定被拒绝", "example.com:4488", "", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://"+tc.host+"/api/state", nil)
			request.Header.Set("Origin", tc.origin)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.want {
				t.Fatalf("HTTP 状态 = %d，期望 %d", response.Code, tc.want)
			}
		})
	}
}
