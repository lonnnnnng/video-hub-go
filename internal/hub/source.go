package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type CMS struct{ Client *http.Client }

func NewCMS() *CMS {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// long: 保留 Go 默认 HTTP 环境设置，拨号仍沿机器现有 DNS 和网络路由。
	transport.DialContext = safePublicDial
	return &CMS{Client: &http.Client{Timeout: 18 * time.Second, Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("资源站重定向次数过多")
		}
		if err := validateSourceTarget(req.URL); err != nil {
			return errors.New("资源站重定向地址无效")
		}
		return nil
	}}}
}

func validateSourceTarget(u *url.URL) error {
	if _, err := webURL(u.String()); err != nil {
		return err
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return errors.New("资源站地址不能指向本机")
	}
	if ip, err := netip.ParseAddr(host); err == nil && !publicAddress(ip.Unmap()) {
		return errors.New("资源站地址不能指向非公网 IP")
	}
	return nil
}

func safePublicDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips := []netip.Addr{}
	if ip, parseErr := netip.ParseAddr(host); parseErr == nil {
		ips = append(ips, ip.Unmap())
	} else {
		resolved, resolveErr := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if resolveErr != nil {
			return nil, resolveErr
		}
		for _, ip := range resolved {
			ips = append(ips, ip.Unmap())
		}
	}
	if len(ips) == 0 {
		return nil, errors.New("资源站域名没有解析到地址")
	}
	for _, ip := range ips {
		// long: 系统网络可能将域名映射到 198.18/15 后透明转发；仅域名解析允许该网段，IP 字面量仍拒绝。
		if !publicAddress(ip) && !(netip.MustParsePrefix("198.18.0.0/15").Contains(ip) && net.ParseIP(host) == nil) {
			return nil, errors.New("资源站域名解析到非公网地址")
		}
	}
	dialer := net.Dialer{Timeout: 8 * time.Second}
	var lastErr error
	for _, ip := range ips {
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	return nil, lastErr
}

func publicAddress(ip netip.Addr) bool {
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	blocked := [...]string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "2001:db8::/32"}
	for _, raw := range blocked {
		if netip.MustParsePrefix(raw).Contains(ip) {
			return false
		}
	}
	return true
}

func identity(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:8]) }
func webURL(raw string) (*url.URL, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return nil, errors.New("只接受不含账号的 HTTP/HTTPS 地址")
	}
	return u, nil
}
func (c *CMS) get(ctx context.Context, raw string, headers map[string]string, value any) error {
	if _, err := webURL(raw); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return errors.New("请求地址无效")
	}
	req.Header.Set("User-Agent", "MovieHubGo/0.1")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := c.Client.Do(req)
	if err != nil {
		return errors.New("资源站连接失败或超时")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("资源站返回 HTTP %d", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 16<<20+1))
	if err != nil {
		return errors.New("读取资源站响应失败")
	}
	if len(b) > 16<<20 {
		return errors.New("资源站响应超过 16 MiB")
	}
	b = []byte(strings.TrimPrefix(string(b), "\ufeff"))
	if json.Unmarshal(b, value) != nil {
		return errors.New("资源站未返回有效 JSON")
	}
	return nil
}
func (c *CMS) Manifest(ctx context.Context, raw string, old []Source) ([]Source, error) {
	var data map[string]struct {
		Name   string `json:"name"`
		API    string `json:"api"`
		Detail string `json:"detail"`
	}
	if err := c.get(ctx, raw, nil, &data); err != nil {
		return nil, err
	}
	previous := map[string]Source{}
	for _, s := range old {
		previous[s.ID] = s
	}
	result := []Source{}
	for id, s := range data {
		if id == "" || s.Name == "" {
			continue
		}
		if _, e := webURL(s.API); e != nil {
			continue
		}
		item := Source{ID: id, Name: s.Name, API: s.API, Detail: s.Detail, Enabled: true}
		if p, ok := previous[id]; ok {
			// long: 清单刷新只更新站点元数据，保留用户启用状态和上次检测结果。
			item = p
			item.Name, item.API, item.Detail = s.Name, s.API, s.Detail
			if p.API != s.API {
				item.CheckStatus, item.CheckError, item.LatencyMS = "", "", 0
				item.CheckedAt = time.Time{}
			}
		}
		result = append(result, item)
	}
	if len(result) == 0 {
		return nil, errors.New("站源清单为空或没有有效 JSON API，保留原清单")
	}
	sortSources(result)
	chooseDefaultSource(result)
	return result, nil
}

func sortSources(sources []Source) {
	// long: 已测得的延迟始终排在未知或失败项之前；同延迟用名称和 ID 保持界面稳定。
	sort.SliceStable(sources, func(i, j int) bool {
		a, b := sources[i], sources[j]
		if (a.LatencyMS > 0) != (b.LatencyMS > 0) {
			return a.LatencyMS > 0
		}
		if a.LatencyMS > 0 && a.LatencyMS != b.LatencyMS {
			return a.LatencyMS < b.LatencyMS
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
}

func chooseDefaultSource(sources []Source) {
	selected := -1
	for i := range sources {
		if sources[i].Enabled && sources[i].Default {
			selected = i
			break
		}
	}
	if selected < 0 {
		for i := range sources {
			if sources[i].Enabled {
				selected = i
				break
			}
		}
	}
	for i := range sources {
		sources[i].Default = i == selected
	}
}

type SourceProbe struct {
	Status    string
	Error     string
	LatencyMS int64
}

func (c *CMS) ProbeSource(ctx context.Context, s Source) SourceProbe {
	request := func(values url.Values) (map[string]json.RawMessage, error) {
		u, err := webURL(s.API)
		if err != nil {
			return nil, err
		}
		q := u.Query()
		q.Set("ac", "videolist")
		for k, v := range values {
			q[k] = v
		}
		u.RawQuery = q.Encode()
		var response map[string]json.RawMessage
		err = c.get(ctx, u.String(), s.Headers, &response)
		if err == nil && response == nil {
			err = errors.New("资源站未返回 JSON 对象")
		}
		return response, err
	}
	// long: 第一请求只判定接口可达，第二请求带 wd 验证 MacCMS 搜索协议；零结果仍可能是正常搜索。
	if _, err := request(url.Values{"pg": {"1"}}); err != nil {
		return SourceProbe{Status: "unreachable", Error: err.Error()}
	}
	started := time.Now()
	result, err := request(url.Values{"pg": {"1"}, "wd": {"测试"}})
	if err != nil {
		return SourceProbe{Status: "unsearchable", Error: err.Error()}
	}
	var code scalar
	if raw, exists := result["code"]; exists {
		if json.Unmarshal(raw, &code) != nil || (code != "" && code != "1") {
			return SourceProbe{Status: "unsearchable", Error: "搜索接口拒绝请求"}
		}
	}
	var list []vodRecord
	if raw, exists := result["list"]; !exists || string(raw) == "null" || json.Unmarshal(raw, &list) != nil {
		return SourceProbe{Status: "unsearchable", Error: "搜索接口未返回影片列表"}
	}
	for _, record := range list {
		if record.ID == "" || strings.TrimSpace(record.Title) == "" {
			return SourceProbe{Status: "unsearchable", Error: "搜索结果缺少影片身份或名称"}
		}
	}
	latency := time.Since(started).Milliseconds()
	if latency < 1 {
		latency = 1
	}
	return SourceProbe{Status: "ok", LatencyMS: latency}
}

type scalar string

func (s *scalar) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*s = ""
		return nil
	}
	var v string
	if len(b) > 0 && b[0] == '"' {
		if e := json.Unmarshal(b, &v); e != nil {
			return e
		}
	} else {
		v = string(b)
	}
	*s = scalar(v)
	return nil
}

type vodRecord struct {
	ID      scalar `json:"vod_id"`
	Title   string `json:"vod_name"`
	Kind    string `json:"type_name"`
	Year    scalar `json:"vod_year"`
	Remarks string `json:"vod_remarks"`
	Poster  string `json:"vod_pic"`
	Plot    string `json:"vod_content"`
	From    string `json:"vod_play_from"`
	URLs    string `json:"vod_play_url"`
}
type vodClass struct {
	ID   scalar `json:"type_id"`
	Name string `json:"type_name"`
}
type cmsResponse struct {
	Code      scalar      `json:"code"`
	Page      scalar      `json:"page"`
	PageCount scalar      `json:"pagecount"`
	Class     []vodClass  `json:"class"`
	List      []vodRecord `json:"list"`
}

type HomeResult struct {
	Source  Source     `json:"source"`
	Videos  []Video    `json:"videos"`
	Classes []vodClass `json:"classes"`
	Page    int        `json:"page"`
	HasMore bool       `json:"hasMore"`
}

func (c *CMS) Home(ctx context.Context, source Source, page int, typeID string) (HomeResult, error) {
	values := url.Values{"pg": {strconv.Itoa(page)}}
	if typeID != "" {
		values.Set("t", typeID)
	}
	r, err := c.query(ctx, source, "videolist", values)
	if err != nil {
		return HomeResult{}, err
	}
	result := HomeResult{Source: Source{ID: source.ID, Name: source.Name}, Videos: []Video{}, Classes: r.Class, Page: page}
	for _, item := range r.List {
		if item.ID != "" && strings.TrimSpace(item.Title) != "" {
			result.Videos = append(result.Videos, parseVideo(source, item))
		}
	}
	pages, _ := strconv.Atoi(string(r.PageCount))
	result.HasMore = (pages > 0 && page < pages) || (pages == 0 && len(r.List) >= 20)
	if page == 1 && len(result.Classes) == 0 {
		// long: 分类接口失败不阻断首页影片；部分站点把分类放在独立的 ac=list 响应中。
		if categories, categoryErr := c.query(ctx, source, "list", url.Values{"pg": {"1"}}); categoryErr == nil {
			result.Classes = categories.Class
		}
	}
	return result, nil
}

var tags = regexp.MustCompile(`<[^>]*>`)
var numberLabel = regexp.MustCompile(`(?i)^(?:第\s*)?(\d+)(?:\s*[集话期]|$)`)
var seasonLabel = regexp.MustCompile(`(?i)^S\d+E(\d+)`)

func episodeNumber(name string, index int) int {
	m := seasonLabel.FindStringSubmatch(name)
	if m == nil {
		m = numberLabel.FindStringSubmatch(name)
	}
	if len(m) > 1 {
		n, _ := strconv.Atoi(m[1])
		if n > 0 && n <= 100000 {
			return n
		}
	}
	return index + 1
}
func parseVideo(s Source, r vodRecord) Video {
	v := Video{SourceID: s.ID, SourceName: s.Name, ID: string(r.ID), Title: html.UnescapeString(r.Title), Kind: r.Kind, Year: string(r.Year), Remarks: r.Remarks, Poster: r.Poster, Plot: html.UnescapeString(tags.ReplaceAllString(r.Plot, "")), Lines: []Line{}}
	names := strings.Split(strings.NewReplacer("|||", "$$$", ",", "$$$").Replace(r.From), "$$$")
	occurrences := map[string]int{}
	for i, group := range strings.Split(r.URLs, "$$$") {
		name := "线路 " + strconv.Itoa(i+1)
		if i < len(names) && strings.TrimSpace(names[i]) != "" {
			name = strings.TrimSpace(names[i])
		}
		occurrences[name]++
		line := Line{ID: identity(name + ":" + strconv.Itoa(occurrences[name])), Name: name, Episodes: []Episode{}}
		seen := map[string]int{}
		for j, entry := range strings.Split(group, "#") {
			label, raw, ok := strings.Cut(entry, "$")
			if !ok {
				continue
			}
			label = strings.TrimSpace(label)
			raw = strings.TrimSpace(raw)
			if strings.HasPrefix(raw, "//") {
				raw = "https:" + raw
			}
			u, e := webURL(raw)
			if e != nil || !strings.Contains(strings.ToLower(u.Path), ".m3u8") {
				continue
			}
			if label == "" {
				label = fmt.Sprintf("第%d集", j+1)
			}
			stable := strings.ToLower(strings.Join(strings.Fields(label), ""))
			seen[stable]++
			key := identity(stable)
			if seen[stable] > 1 {
				// long: 重名集按出现顺序区分，签名 URL 更新时追更仍能识别同一集。
				key = identity(stable + "\x00" + strconv.Itoa(seen[stable]))
			}
			line.Episodes = append(line.Episodes, Episode{Key: key, Name: label, Number: episodeNumber(label, j), URL: raw})
		}
		if len(line.Episodes) > 0 {
			v.Lines = append(v.Lines, line)
		}
	}
	sort.SliceStable(v.Lines, func(i, j int) bool {
		return strings.Contains(strings.ToLower(v.Lines[i].Name), "m3u8") && !strings.Contains(strings.ToLower(v.Lines[j].Name), "m3u8")
	})
	return v
}

func (c *CMS) query(ctx context.Context, s Source, action string, values url.Values) (cmsResponse, error) {
	var result cmsResponse
	u, e := webURL(s.API)
	if e != nil {
		return result, e
	}
	q := u.Query()
	q.Set("ac", action)
	for k, v := range values {
		q[k] = v
	}
	u.RawQuery = q.Encode()
	e = c.get(ctx, u.String(), s.Headers, &result)
	if e == nil && result.Code != "" && result.Code != "1" {
		e = errors.New("资源站拒绝请求")
	}
	return result, e
}
func (c *CMS) Detail(ctx context.Context, s Source, id string) (Video, error) {
	for _, action := range []string{"detail", "videolist"} {
		r, e := c.query(ctx, s, action, url.Values{"ids": {id}})
		if e != nil {
			if action == "videolist" {
				return Video{}, e
			}
			continue
		}
		for _, v := range r.List {
			if string(v.ID) == id {
				return parseVideo(s, v), nil
			}
		}
	}
	return Video{}, errors.New("资源站没有返回该影片详情")
}

type SearchResult struct {
	Videos []Video           `json:"videos"`
	Errors map[string]string `json:"errors"`
	Pages  map[string]int    `json:"pages"`
}

func (c *CMS) Search(ctx context.Context, sources []Source, keyword string, page int) SearchResult {
	result := SearchResult{Videos: []Video{}, Errors: map[string]string{}, Pages: map[string]int{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for _, s := range sources {
		if !s.Enabled {
			continue
		}
		wg.Add(1)
		go func(s Source) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			r, e := c.query(ctx, s, "videolist", url.Values{"wd": {keyword}, "pg": {strconv.Itoa(page)}})
			mu.Lock()
			defer mu.Unlock()
			if e != nil {
				result.Errors[s.Name] = e.Error()
				return
			}
			n, _ := strconv.Atoi(string(r.PageCount))
			result.Pages[s.ID] = n
			for _, v := range r.List {
				result.Videos = append(result.Videos, parseVideo(s, v))
			}
		}(s)
	}
	wg.Wait()
	sort.SliceStable(result.Videos, func(i, j int) bool {
		a, b := result.Videos[i], result.Videos[j]
		if a.SourceID != b.SourceID {
			return a.SourceID < b.SourceID
		}
		return a.ID < b.ID
	})
	return result
}
func findLine(v Video, id string) (Line, error) {
	for _, l := range v.Lines {
		if l.ID == id {
			return l, nil
		}
	}
	return Line{}, errors.New("线路已失效，请重新选择")
}
