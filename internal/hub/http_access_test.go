package hub

import (
	"encoding/json"
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
		{"局域网地址免令牌", "192.168.1.2:4444", "", http.StatusOK},
		{"跨站请求被拒绝", "192.168.1.2:4444", "http://example.com", http.StatusForbidden},
		{"域名重绑定被拒绝", "example.com:4444", "", http.StatusForbidden},
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

func TestDetailReturnsEpisodeURL(t *testing.T) {
	const episodeURL = "https://cdn.example/episode.m3u8"
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "list": []map[string]string{{"vod_id": "77096", "vod_name": "你也配剑来", "vod_play_from": "360zy", "vod_play_url": "合全集$" + episodeURL}}})
	}))
	defer source.Close()
	store, err := OpenStore(filepath.Join(t.TempDir(), "data"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Update(func(state *State) error {
		state.Sources = []Source{{ID: "360zy.com", Name: "360zy", API: source.URL, Enabled: true}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	service := NewService(store)
	service.CMS = localTestCMS()
	request := httptest.NewRequest(http.MethodGet, "http://192.168.1.2:4444/api/detail?sourceId=360zy.com&id=77096", nil)
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("详情接口状态 = %d：%s", response.Code, response.Body.String())
	}
	var video Video
	if err := json.Unmarshal(response.Body.Bytes(), &video); err != nil {
		t.Fatal(err)
	}
	if len(video.Lines) != 1 || len(video.Lines[0].Episodes) != 1 || video.Lines[0].Episodes[0].URL != episodeURL {
		t.Fatalf("详情接口未返回站源播放地址：%+v", video.Lines)
	}
}
