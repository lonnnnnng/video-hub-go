package hub

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"movie-hub-go/internal/diskspace"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type hlsPart struct {
	url    string
	name   string
	start  int64
	length int64
	key    []byte
	iv     []byte
}

type hlsFetcher struct {
	client     *http.Client
	headers    map[string]string
	onBytes    func(int)
	spaceCheck func(string, uint64) error
}

func (f *hlsFetcher) requireSpace(path string, reserve uint64) error {
	if f.spaceCheck != nil {
		return f.spaceCheck(path, reserve)
	}
	return diskspace.Require(path, reserve)
}

func newHLSFetcher(headers map[string]string) *hlsFetcher {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// long: 请求沿用 Go 的系统环境代理和机器路由，不保存或强制指定代理地址。
	return &hlsFetcher{client: &http.Client{Transport: transport, Timeout: 35 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("HLS 重定向次数过多")
		}
		return validateSourceTarget(req.URL)
	}}, headers: headers}
}

func (f *hlsFetcher) get(ctx context.Context, address string, start, length int64, limit int64) ([]byte, string, error) {
	parsed, err := webURL(address)
	if err != nil {
		return nil, "", errors.New("HLS 资源地址无效")
	}
	if err = validateSourceTarget(parsed); err != nil {
		return nil, "", err
	}
	var last error
	var body []byte
	for attempt := 0; attempt < 3; attempt++ {
		if err = ctx.Err(); err != nil {
			return nil, "", err
		}
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
		if reqErr != nil {
			return nil, "", reqErr
		}
		req.Header.Set("User-Agent", "MovieHub/1.0")
		for k, v := range f.headers {
			req.Header.Set(k, v)
		}
		if length > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, start+length-1))
		}
		resp, requestErr := f.client.Do(req)
		if requestErr == nil {
			func() {
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
					last = fmt.Errorf("HLS 资源返回 HTTP %d", resp.StatusCode)
					return
				}
				if length > 0 && resp.StatusCode != http.StatusPartialContent {
					last = errors.New("HLS 分片服务器不支持字节范围")
					return
				}
				if length > 0 && !strings.HasPrefix(resp.Header.Get("Content-Range"), fmt.Sprintf("bytes %d-%d/", start, start+length-1)) {
					last = errors.New("HLS 分片服务器返回错误的字节范围")
					return
				}
				var reader io.Reader = resp.Body
				if f.onBytes != nil {
					reader = &hlsCountingReader{Reader: reader, count: f.onBytes}
				}
				body, last = io.ReadAll(io.LimitReader(reader, limit+1))
				if last == nil && int64(len(body)) > limit {
					last = errors.New("HLS 资源超过大小限制")
				}
				if last == nil && length > 0 && int64(len(body)) != length {
					last = errors.New("HLS 字节范围长度不符")
				}
				if last == nil && len(body) == 0 {
					last = errors.New("HLS 资源为空")
				}
			}()
			if last == nil {
				return body, resp.Request.URL.String(), nil
			}
		} else {
			// long: 网络错误可能含分集签名 URL，任务状态只记录可公开的失败类别。
			last = errors.New("HLS 网络请求失败")
		}
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return nil, "", ctx.Err()
			case <-time.After(time.Duration(attempt+1) * time.Second):
			}
		}
	}
	return nil, "", last
}

func hlsURL(base, ref string) (string, error) {
	b, err := webURL(base)
	if err != nil {
		return "", err
	}
	r, err := url.Parse(strings.TrimSpace(ref))
	if err != nil {
		return "", errors.New("HLS 相对地址无效")
	}
	u := b.ResolveReference(r)
	if err = validateSourceTarget(u); err != nil {
		return "", err
	}
	return u.String(), nil
}

func hlsAttr(line, key string) string {
	// long: 签名 URL 的引号内可能含逗号，只有属性分隔符才能切开键值。
	items := []string{}
	start, quoted := 0, false
	for i, r := range line {
		if r == '"' {
			quoted = !quoted
		}
		if r == ',' && !quoted {
			items = append(items, line[start:i])
			start = i + 1
		}
	}
	items = append(items, line[start:])
	for _, item := range items {
		name, value, ok := strings.Cut(strings.TrimSpace(item), "=")
		if ok && name == key {
			return strings.Trim(value, `"`)
		}
	}
	return ""
}

func resolveHLS(ctx context.Context, f *hlsFetcher, address string) (string, string, error) {
	for depth := 0; depth < 4; depth++ {
		body, final, err := f.get(ctx, address, 0, 0, 4<<20)
		if err != nil {
			return "", "", err
		}
		raw := strings.TrimPrefix(string(body), "\ufeff")
		if !strings.HasPrefix(strings.TrimSpace(raw), "#EXTM3U") {
			return "", "", errors.New("资源不是 HLS 片单")
		}
		if !strings.Contains(raw, "#EXT-X-STREAM-INF:") {
			return raw, final, nil
		}
		if strings.Contains(raw, "#EXT-X-MEDIA:") {
			return "", "", errors.New("暂不支持独立音轨的 HLS 主片单")
		}
		best, bandwidth, expect := "", int64(-1), false
		var current int64
		for _, rawLine := range strings.Split(raw, "\n") {
			line := strings.TrimSpace(rawLine)
			if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
				if hlsAttr(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"), "AUDIO") != "" {
					return "", "", errors.New("暂不支持独立音轨的 HLS 主片单")
				}
				current, _ = strconv.ParseInt(hlsAttr(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"), "BANDWIDTH"), 10, 64)
				expect = true
			} else if expect && line != "" && !strings.HasPrefix(line, "#") {
				if current > bandwidth {
					best, bandwidth = line, current
				}
				expect = false
			}
		}
		if best == "" {
			return "", "", errors.New("HLS 主片单没有可用视频线路")
		}
		address, err = hlsURL(final, best)
		if err != nil {
			return "", "", err
		}
	}
	return "", "", errors.New("HLS 主片单嵌套过深")
}

func hlsRange(spec string, previous int64) (int64, int64, error) {
	sizeText, startText, explicit := strings.Cut(spec, "@")
	size, err := strconv.ParseInt(sizeText, 10, 64)
	if err != nil || size <= 0 || size > 128<<20 {
		return 0, 0, errors.New("HLS 字节范围无效")
	}
	start := previous
	if explicit {
		start, err = strconv.ParseInt(startText, 10, 64)
	}
	if err != nil || start < 0 || start > 1<<60 {
		return 0, 0, errors.New("HLS 字节范围偏移无效")
	}
	return start, size, nil
}

func parseHLS(ctx context.Context, f *hlsFetcher, raw, base string) ([]hlsPart, string, error) {
	var parts []hlsPart
	var local strings.Builder
	var key []byte
	var explicitIV []byte
	var pendingRange string
	var lastRangeEnd int64
	var lastRangeURL string
	var sequence uint64
	var mapSeen bool
	var segmentCount int
	keyCache := map[string][]byte{}
	for _, rawLine := range strings.Split(raw, "\n") {
		line := strings.TrimSpace(rawLine)
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			n, err := strconv.ParseUint(strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"), 10, 64)
			if err != nil {
				return nil, "", errors.New("HLS 媒体序号无效")
			}
			sequence = n
		case strings.HasPrefix(line, "#EXT-X-KEY:"):
			attrs := strings.TrimPrefix(line, "#EXT-X-KEY:")
			method := hlsAttr(attrs, "METHOD")
			if method == "NONE" {
				key, explicitIV = nil, nil
				continue
			}
			if method != "AES-128" {
				return nil, "", errors.New("HLS 加密方式暂不支持：" + method)
			}
			ref := hlsAttr(attrs, "URI")
			if ref == "" {
				return nil, "", errors.New("HLS 密钥地址缺失")
			}
			address, err := hlsURL(base, ref)
			if err != nil {
				return nil, "", err
			}
			key = keyCache[address]
			if key == nil {
				key, _, err = f.get(ctx, address, 0, 0, 16)
				if err != nil || len(key) != 16 {
					return nil, "", errors.New("HLS AES-128 密钥获取失败")
				}
				keyCache[address] = key
			}
			explicitIV = nil
			if ivText := hlsAttr(attrs, "IV"); ivText != "" {
				ivText = strings.TrimPrefix(strings.ToLower(ivText), "0x")
				if len(ivText) > 32 {
					return nil, "", errors.New("HLS IV 无效")
				}
				ivText = strings.Repeat("0", 32-len(ivText)) + ivText
				explicitIV, err = hex.DecodeString(ivText)
				if err != nil {
					return nil, "", errors.New("HLS IV 无效")
				}
			}
		case strings.HasPrefix(line, "#EXT-X-BYTERANGE:"):
			pendingRange = strings.TrimPrefix(line, "#EXT-X-BYTERANGE:")
		case strings.HasPrefix(line, "#EXT-X-MAP:"):
			if mapSeen {
				return nil, "", errors.New("暂不支持多段初始化文件的 HLS 片单")
			}
			if len(key) > 0 {
				return nil, "", errors.New("暂不支持加密的 fMP4 初始化文件")
			}
			attrs := strings.TrimPrefix(line, "#EXT-X-MAP:")
			address, err := hlsURL(base, hlsAttr(attrs, "URI"))
			if err != nil {
				return nil, "", err
			}
			part := hlsPart{url: address, name: "init.mp4"}
			if spec := hlsAttr(attrs, "BYTERANGE"); spec != "" {
				part.start, part.length, err = hlsRange(spec, 0)
				if err != nil {
					return nil, "", err
				}
			}
			parts = append(parts, part)
			mapSeen = true
			local.WriteString("#EXT-X-MAP:URI=\"init.mp4\"\n")
		case strings.HasPrefix(line, "#EXT-X-PART:") || strings.HasPrefix(line, "#EXT-X-PRELOAD-HINT:"):
			return nil, "", errors.New("暂不支持低延迟 HLS 分片")
		case strings.HasPrefix(line, "#EXT-X-SESSION-KEY:") || strings.HasPrefix(line, "#EXT-X-DEFINE:"):
			return nil, "", errors.New("暂不支持该 HLS 主片单扩展")
		case strings.HasPrefix(line, "#"):
			local.WriteString(line + "\n")
		default:
			if len(parts) >= 20001 {
				return nil, "", errors.New("HLS 分片数量超过限制")
			}
			address, err := hlsURL(base, line)
			if err != nil {
				return nil, "", err
			}
			part := hlsPart{url: address, name: fmt.Sprintf("segment-%06d.ts", segmentCount), key: key}
			if mapSeen {
				part.name = fmt.Sprintf("segment-%06d.m4s", segmentCount)
			}
			if pendingRange != "" {
				if len(key) > 0 {
					return nil, "", errors.New("暂不支持 AES-128 字节范围分片")
				}
				if !strings.Contains(pendingRange, "@") && lastRangeURL != address {
					return nil, "", errors.New("HLS 字节范围缺少起始偏移")
				}
				part.start, part.length, err = hlsRange(pendingRange, lastRangeEnd)
				if err != nil {
					return nil, "", err
				}
				lastRangeEnd = part.start + part.length
				lastRangeURL = address
				pendingRange = ""
			} else {
				lastRangeEnd = 0
				lastRangeURL = ""
			}
			if len(key) > 0 {
				part.iv = explicitIV
				if part.iv == nil {
					part.iv = make([]byte, 16)
					binary.BigEndian.PutUint64(part.iv[8:], sequence)
				}
			}
			parts = append(parts, part)
			local.WriteString(part.name + "\n")
			sequence++
			segmentCount++
		}
	}
	if segmentCount == 0 {
		return nil, "", errors.New("HLS 片单没有分片")
	}
	if !strings.Contains(local.String(), "#EXT-X-ENDLIST") {
		return nil, "", errors.New("暂不下载仍在更新的直播片单")
	}
	return parts, local.String(), nil
}

func decryptHLS(data, key, iv []byte) ([]byte, error) {
	if len(key) != 16 || len(iv) != 16 || len(data)%aes.BlockSize != 0 {
		return nil, errors.New("HLS AES-128 分片长度无效")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	plain := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, data)
	if len(plain) == 0 {
		return nil, errors.New("HLS AES-128 分片为空")
	}
	padding := int(plain[len(plain)-1])
	if padding == 0 || padding > aes.BlockSize || padding > len(plain) || !bytes.Equal(plain[len(plain)-padding:], bytes.Repeat([]byte{byte(padding)}, padding)) {
		return nil, errors.New("HLS AES-128 填充无效")
	}
	return plain[:len(plain)-padding], nil
}

// long: 在读取响应体时计数，大分片尚未接收完也能显示速度；重试产生的真实流量同样计入。
type hlsCountingReader struct {
	io.Reader
	count func(int)
}

func (r *hlsCountingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if n > 0 {
		r.count(n)
	}
	return n, err
}
func formatDownloadSpeed(value float64) string {
	units := []string{"B/s", "KB/s", "MB/s", "GB/s"}
	i := 0
	for value >= 1024 && i < len(units)-1 {
		value /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", value, units[i])
}
func fetchHLSParts(ctx context.Context, f *hlsFetcher, parts []hlsPart, dir string, threads int, progress func(float64, string)) error {
	if threads < 1 {
		threads = 1
	}
	if threads > 32 {
		threads = 32
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	queue := make(chan hlsPart)
	results := make(chan error, threads)
	var done, received atomic.Int64
	// long: fetcher 可用于多个并行任务，计数回调仅绑定本次分片下载副本，避免不同剧集串速度。
	tracked := *f
	tracked.onBytes = func(n int) { received.Add(int64(n)) }
	var wg sync.WaitGroup
	for i := 0; i < threads; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for part := range queue {
				data, _, err := tracked.get(ctx, part.url, part.start, part.length, 128<<20)
				if err == nil && len(part.key) > 0 {
					data, err = decryptHLS(data, part.key, part.iv)
				}
				if err == nil {
					err = tracked.requireSpace(dir, uint64(len(data)))
				}
				if err == nil {
					err = os.WriteFile(filepath.Join(dir, part.name), data, 0600)
				}
				if err != nil {
					select {
					case results <- err:
					default:
					}
					cancel()
					return
				}
				done.Add(1)
			}
		}()
	}
	go func() {
		defer close(queue)
		for _, part := range parts {
			select {
			case queue <- part:
			case <-ctx.Done():
				return
			}
		}
	}()
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	lastTime, lastBytes := time.Now(), int64(0)
	report := func() {
		if progress == nil || len(parts) == 0 {
			return
		}
		now, total := time.Now(), received.Load()
		speed := float64(total-lastBytes) / now.Sub(lastTime).Seconds()
		progress(float64(done.Load())/float64(len(parts))*0.95, formatDownloadSpeed(speed))
		lastTime, lastBytes = now, total
	}
	// long: 按时间节流落盘，同时覆盖长分片接收和无流量等待，避免沿用失真的历史速度。
	for {
		select {
		case <-ticker.C:
			report()
		case <-finished:
			report()
			select {
			case err := <-results:
				return fmt.Errorf("HLS 分片下载失败: %w", err)
			default:
			}
			return ctx.Err()
		}
	}
}

func downloadHLS(ctx context.Context, address string, headers map[string]string, staging, output, ffmpeg string, threads int, progress func(float64, string), guards ...func(string, uint64) error) error {
	f := newHLSFetcher(headers)
	if len(guards) > 0 {
		f.spaceCheck = guards[0]
	}
	return downloadHLSWithFetcher(ctx, f, address, staging, output, ffmpeg, threads, progress)
}

func downloadHLSWithFetcher(ctx context.Context, f *hlsFetcher, address, staging, output, ffmpeg string, threads int, progress func(float64, string)) error {
	started := time.Now()
	raw, base, err := resolveHLS(ctx, f, address)
	if err != nil {
		return err
	}
	parts, playlist, err := parseHLS(ctx, f, raw, base)
	if err != nil {
		return err
	}
	// long: 只记录聚合分片数和阶段耗时，签名播放地址、加密密钥 URI 与分片地址均不进入日志。
	slog.Info("HLS 片单已解析", "parts", len(parts), "threads", threads)
	// long: 并发只影响取回顺序，片单仍按原媒体顺序引用本地文件，避免画面和声音乱序。
	if err = fetchHLSParts(ctx, f, parts, staging, threads, progress); err != nil {
		return err
	}
	if progress != nil {
		progress(0.95, "")
	}
	slog.Info("HLS 分片已取回", "parts", len(parts), "duration_ms", time.Since(started).Milliseconds())
	playlistPath := filepath.Join(staging, "local.m3u8")
	var reserve uint64
	files, err := os.ReadDir(staging)
	if err != nil {
		return err
	}
	for _, file := range files {
		info, e := file.Info()
		if e != nil {
			return e
		}
		if info.Mode().IsRegular() {
			reserve += uint64(info.Size())
		}
	}
	// long: 分片已占用磁盘，封装还会生成一份 MP4；开始 FFmpeg 前为成品另留空间。
	if err := f.requireSpace(staging, reserve); err != nil {
		return err
	}
	if err = os.WriteFile(playlistPath, []byte(playlist), 0600); err != nil {
		return err
	}
	muxCtx, cancel := context.WithCancel(ctx)
	spaceResult := diskspace.Watch(muxCtx, staging, cancel)
	cmd := command(muxCtx, ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-protocol_whitelist", "file,crypto", "-allowed_extensions", "ALL", "-i", playlistPath, "-c", "copy", "-movflags", "+faststart", "-f", "mp4", output)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err = cmd.Run()
	cancel()
	spaceErr := <-spaceResult
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if spaceErr != nil {
		return spaceErr
	}
	if err != nil {
		if spaceErr := diskspace.FFmpegError(stderr.String()); spaceErr != nil {
			return spaceErr
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("FFmpeg 合并 MP4 失败: %s", strings.TrimSpace(stderr.String()))
	}
	if progress != nil {
		progress(1, "")
	}
	slog.Info("FFmpeg 封装完成", "parts", len(parts), "duration_ms", time.Since(started).Milliseconds())
	return nil
}
