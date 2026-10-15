package hub

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func localTestCMS() *CMS {
	return &CMS{Client: &http.Client{}}
}

func TestManifestPreservesSourceConfiguration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]map[string]string{"site": {"name": "站源", "api": "https://source.example/api.php/provide/vod", "detail": "https://source.example"}})
	}))
	defer server.Close()
	cms := localTestCMS()
	old := []Source{{ID: "site", Name: "旧名称", Enabled: false, Headers: map[string]string{"Referer": "https://example.test"}}}
	got, e := cms.Manifest(context.Background(), server.URL, old)
	if e != nil {
		t.Fatal(e)
	}
	if got[0].Enabled || got[0].Headers["Referer"] == "" {
		t.Fatal("manifest refresh lost user config")
	}
}

func TestPublicDialRejectsPrivateAddress(t *testing.T) {
	_, err := safePublicDial(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", "80"))
	if err == nil {
		t.Fatal("private destination was allowed")
	}
}

func TestSourceRedirectRejectsNonHTTPDestination(t *testing.T) {
	client := NewCMS().Client
	request, _ := http.NewRequest(http.MethodGet, "https://public.example/", nil)
	redirect, _ := http.NewRequest(http.MethodGet, "file:///etc/passwd", nil)
	err := client.CheckRedirect(redirect, []*http.Request{request})
	if err == nil {
		t.Fatal("redirect to loopback was allowed")
	}
}

func TestParseDuplicateEpisodeNamesRemainStableAndDistinct(t *testing.T) {
	source := Source{ID: "s", Name: "资源站"}
	first := parseVideo(source, vodRecord{ID: "1", From: "线路", URLs: "第1集$https://a.example/1.m3u8#第1集$https://a.example/2.m3u8"})
	updated := parseVideo(source, vodRecord{ID: "1", From: "线路", URLs: "第1集$https://b.example/1.m3u8?token=x#第1集$https://b.example/2.m3u8?token=y"})
	a, b := first.Lines[0].Episodes, updated.Lines[0].Episodes
	if len(a) != 2 || a[0].Key == a[1].Key || a[0].Key != b[0].Key || a[1].Key != b[1].Key {
		t.Fatalf("duplicate episode identities are not stable: %#v %#v", a, b)
	}
}

func TestSourceURLRejectsCredentialsAndNonHTTP(t *testing.T) {
	for _, raw := range []string{"ftp://source.example/list", "https://user:pass@source.example/list"} {
		if _, err := webURL(raw); err == nil {
			t.Fatalf("accepted invalid source URL %q", raw)
		}
	}
}
func TestParseVideoStableEpisodeIDsAndFiltersNonHLS(t *testing.T) {
	source := Source{ID: "s", Name: "资源"}
	raw := vodRecord{ID: "1", Title: "剧", From: "线路一$$$备用", URLs: "第一集$https://cdn.test/1.m3u8?sig=a#第二集$//cdn.test/2.m3u8#花絮$https://cdn.test/trailer.mp4$$$备用集$https://cdn.test/b.m3u8"}
	v := parseVideo(source, raw)
	if len(v.Lines) != 2 || len(v.Lines[0].Episodes) != 2 {
		t.Fatalf("unexpected lines: %+v", v.Lines)
	}
	changed := raw
	changed.URLs = "第一集$https://other.test/new.m3u8#第二集$https://other.test/2.m3u8$$$备用集$https://cdn.test/b.m3u8"
	v2 := parseVideo(source, changed)
	for i := range v.Lines {
		for j := range v.Lines[i].Episodes {
			if v.Lines[i].Episodes[j].Key != v2.Lines[i].Episodes[j].Key {
				t.Fatal("signed URL changed episode identity")
			}
		}
	}
	if v.Lines[0].Episodes[1].Number != 2 {
		t.Fatal("episode numbering failed")
	}
}

func TestCheckSourcesDisablesFailuresAndSelectsLowestLatency(t *testing.T) {
	makeSite := func(delay time.Duration, searchable bool, status int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if status != 200 {
				w.WriteHeader(status)
				return
			}
			if r.URL.Query().Get("wd") != "" {
				time.Sleep(delay)
				if !searchable {
					_, _ = w.Write([]byte(`{"code":1,"msg":"no search"}`))
					return
				}
			}
			_, _ = w.Write([]byte(`{"code":1,"pagecount":2,"list":[{"vod_id":"7","vod_name":"测试影片","vod_pic":"https://example.com/p.jpg"}]}`))
		}))
	}
	fast, slow, noSearch, offline := makeSite(2*time.Millisecond, true, 200), makeSite(35*time.Millisecond, true, 200), makeSite(0, false, 200), makeSite(0, false, 503)
	defer fast.Close()
	defer slow.Close()
	defer noSearch.Close()
	defer offline.Close()
	store, err := OpenStore(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Update(func(st *State) error {
		st.Sources = []Source{
			{ID: "slow", Name: "慢源", API: slow.URL, Enabled: true, Default: true},
			{ID: "bad-search", Name: "不可搜索", API: noSearch.URL, Enabled: true},
			{ID: "offline", Name: "连接失败", API: offline.URL, Enabled: true},
			{ID: "fast", Name: "快源", API: fast.URL, Enabled: true},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	service := NewService(store)
	service.CMS = localTestCMS()
	summary, err := service.CheckSources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if summary.Total != 4 || summary.Available != 2 || summary.Disabled != 2 || summary.DefaultSource != "快源" {
		t.Fatalf("检测汇总错误: %+v", summary)
	}
	got := store.Snapshot().Sources
	if got[0].ID != "fast" || !got[0].Default || got[0].LatencyMS <= 0 || got[1].ID != "slow" {
		t.Fatalf("排序或默认源错误: %+v", got)
	}
	for _, source := range got {
		if source.ID == "bad-search" && (source.Enabled || source.CheckStatus != "unsearchable" || !source.DisabledByCheck) {
			t.Fatalf("不可搜索源未禁用: %+v", source)
		}
		if source.ID == "offline" && (source.Enabled || source.CheckStatus != "unreachable") {
			t.Fatalf("连接失败源未禁用: %+v", source)
		}
	}
	home, err := service.Home(context.Background(), 1, "")
	if err != nil || home.Source.ID != "fast" || len(home.Videos) != 1 || !home.HasMore {
		t.Fatalf("首页未使用默认源: %+v %v", home, err)
	}
}

func TestProbeAcceptsValidEmptySearchAndPreservesManualDisable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":1,"list":[]}`))
	}))
	defer server.Close()
	store, err := OpenStore(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Update(func(st *State) error {
		st.Sources = []Source{{ID: "manual", Name: "手动停用", API: server.URL, Enabled: false}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	service := NewService(store)
	service.CMS = localTestCMS()
	if _, err := service.CheckSources(context.Background()); err != nil {
		t.Fatal(err)
	}
	source := store.Snapshot().Sources[0]
	if source.Enabled || source.CheckStatus != "ok" || source.Default {
		t.Fatalf("空搜索结果或手动停用状态被误处理: %+v", source)
	}
	if err := store.Update(func(st *State) error { st.Sources[0].DisabledByCheck = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CheckSources(context.Background()); err != nil {
		t.Fatal(err)
	}
	source = store.Snapshot().Sources[0]
	if !source.Enabled || !source.Default || source.DisabledByCheck {
		t.Fatalf("检测恢复后未重新启用站源: %+v", source)
	}
}
