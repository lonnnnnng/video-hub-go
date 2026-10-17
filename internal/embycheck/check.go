package embycheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

type Status struct {
	State        string    `json:"state,omitempty"`
	ItemID       string    `json:"itemId,omitempty"`
	CheckedAt    time.Time `json:"checkedAt,omitempty"`
	ConfirmedAt  time.Time `json:"confirmedAt,omitempty"`
	NextCheckAt  time.Time `json:"nextCheckAt,omitempty"`
	WaitingSince time.Time `json:"waitingSince,omitempty"`
	Error        string    `json:"error,omitempty"`
}

type Target struct {
	ID        string
	LocalPath string
	Suffix    string
	Previous  Status
}

type Source struct {
	Path     string `json:"Path"`
	Protocol string `json:"Protocol"`
}

type Item struct {
	ID           string   `json:"Id"`
	Type         string   `json:"Type"`
	Path         string   `json:"Path"`
	MediaSources []Source `json:"MediaSources"`
}

func Pending(now time.Time) Status {
	return Status{State: "waiting", WaitingSince: now, NextCheckAt: now.Add(5 * time.Second)}
}

func normalize(value string) string { return path.Clean(strings.ReplaceAll(value, "\\", "/")) }

func matches(value string, target Target) bool {
	value = normalize(value)
	if value == normalize(target.LocalPath) {
		return true
	}
	suffix := strings.TrimPrefix(normalize(target.Suffix), "/")
	// long: 容器可把共享目录挂载在不同前缀；只匹配带剧集身份的完整相对路径，重复挂载仍需报错。
	return suffix != "" && suffix != "." && strings.HasSuffix(value, "/"+suffix)
}

func Evaluate(target Target, items []Item, now time.Time) Status {
	result := target.Previous
	result.CheckedAt = now
	result.NextCheckAt = now.Add(time.Minute)
	result.ItemID = ""
	info, err := os.Lstat(target.LocalPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		result.State, result.Error = "error", "本地 MP4 不存在或不是有效普通文件，请检查媒体目录"
		return result
	}
	found := map[string]bool{}
	matched := map[string]bool{}
	wrongProtocol := false
	for _, item := range items {
		if item.Type != "Episode" || item.ID == "" || !matches(item.Path, target) {
			continue
		}
		matched[item.ID] = true
		file := false
		for _, source := range item.MediaSources {
			if strings.EqualFold(source.Protocol, "File") && matches(source.Path, target) && strings.EqualFold(path.Ext(normalize(source.Path)), ".mp4") {
				file = true
			}
		}
		if file {
			found[item.ID] = true
		} else {
			wrongProtocol = true
		}
	}
	if len(matched) > 1 {
		result.State, result.Error = "error", "Emby 中存在重复媒体条目，请检查重复的媒体库挂载"
		return result
	}
	if len(found) == 1 {
		for id := range found {
			result.ItemID = id
		}
		result.State, result.Error = "confirmed", ""
		result.ConfirmedAt = now
		result.WaitingSince = time.Time{}
		result.NextCheckAt = now.Add(5 * time.Minute)
		return result
	}
	if wrongProtocol {
		result.State, result.Error = "error", "Emby 条目尚未识别为本地 MP4，请检查旧 STRM 和媒体库路径"
		return result
	}
	if result.WaitingSince.IsZero() {
		result.WaitingSince = now
	}
	result.State, result.Error = "waiting", ""
	result.NextCheckAt = now.Add(30 * time.Second)
	if now.Sub(result.WaitingSince) >= 10*time.Minute {
		result.State, result.Error = "error", "Emby 尚未发现此 MP4，请检查媒体库目录、读取权限并补扫"
		result.NextCheckAt = now.Add(time.Minute)
	}
	return result
}

func Check(ctx context.Context, server, key string, targets []Target) (map[string]Status, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	items, err := episodes(ctx, server, key)
	now := time.Now()
	result := make(map[string]Status, len(targets))
	for _, target := range targets {
		if err == nil {
			result[target.ID] = Evaluate(target, items, now)
			continue
		}
		status := target.Previous
		status.State, status.Error, status.CheckedAt, status.NextCheckAt = "error", err.Error(), now, now.Add(time.Minute)
		result[target.ID] = status
	}
	return result, err
}

func episodes(ctx context.Context, server, key string) ([]Item, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	items := []Item{}
	seen := map[string]bool{}
	for start := 0; start < 100000; {
		query := url.Values{"Recursive": {"true"}, "IncludeItemTypes": {"Episode"}, "Fields": {"Path,MediaSources"}, "EnableImages": {"false"}, "Limit": {"500"}, "StartIndex": {strconv.Itoa(start)}}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(server, "/")+"/Items?"+query.Encode(), nil)
		if err != nil {
			return nil, errors.New("Emby 入库确认地址无效")
		}
		req.Header.Set("X-Emby-Token", key)
		res, err := client.Do(req)
		if err != nil {
			return nil, errors.New("Emby 入库确认连接失败，将自动重试")
		}
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			res.Body.Close()
			return nil, fmt.Errorf("Emby 入库确认返回 HTTP %d，请检查地址和 API Key", res.StatusCode)
		}
		body, readErr := io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
		res.Body.Close()
		var page struct {
			Items            *[]Item `json:"Items"`
			TotalRecordCount int     `json:"TotalRecordCount"`
		}
		if readErr != nil || len(body) > 8<<20 || json.Unmarshal(body, &page) != nil || page.Items == nil {
			return nil, errors.New("Emby 入库确认响应无效或过大")
		}
		added := 0
		for _, item := range *page.Items {
			if item.ID != "" && !seen[item.ID] {
				seen[item.ID] = true
				items = append(items, item)
				added++
			}
		}
		if len(*page.Items) > 0 && added == 0 {
			return nil, errors.New("Emby 入库确认分页未前进")
		}
		if len(*page.Items) == 0 && page.TotalRecordCount > start {
			return nil, errors.New("Emby 入库确认分页不完整，将自动重试")
		}
		start += len(*page.Items)
		if len(*page.Items) == 0 || page.TotalRecordCount > 0 && start >= page.TotalRecordCount || page.TotalRecordCount == 0 && len(*page.Items) < 500 {
			return items, nil
		}
	}
	return nil, errors.New("Emby 分集超过 100000 条，入库确认未完成")
}
