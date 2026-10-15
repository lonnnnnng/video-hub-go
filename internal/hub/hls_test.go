package hub

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type hlsTransport func(*http.Request) (*http.Response, error)

func (fn hlsTransport) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

func encryptFixture(t *testing.T, raw, key []byte, sequence uint64) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	padding := aes.BlockSize - len(raw)%aes.BlockSize
	plain := append(append([]byte(nil), raw...), bytes.Repeat([]byte{byte(padding)}, padding)...)
	var iv [16]byte
	binary.BigEndian.PutUint64(iv[8:], sequence)
	result := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, iv[:]).CryptBlocks(result, plain)
	return result
}

func TestEmbeddedHLSConcurrentMP4(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg 未安装")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("FFprobe 未安装")
	}
	fixture := t.TempDir()
	playlist := filepath.Join(fixture, "index.m3u8")
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=10:duration=1.2", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100:duration=1.2", "-c:v", "libx264", "-preset", "ultrafast", "-g", "5", "-sc_threshold", "0", "-c:a", "aac", "-f", "hls", "-hls_time", "0.5", "-hls_list_size", "0", "-hls_segment_filename", filepath.Join(fixture, "seg%03d.ts"), playlist)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("生成 HLS 样本失败: %v %s", err, output)
	}
	raw, err := os.ReadFile(playlist)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("0123456789abcdef")
	media := string(raw)
	secure := strings.Replace(media, "#EXTM3U\n", "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"../key.bin\"\n", 1)
	assets := map[string][]byte{"/plain/index.m3u8": raw, "/secure/master.m3u8": []byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=500000\nmedia/index.m3u8\n"), "/secure/media/index.m3u8": []byte(secure), "/secure/key.bin": key}
	var sequence uint64
	for _, line := range strings.Split(media, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasSuffix(line, ".ts") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(fixture, line))
		if err != nil {
			t.Fatal(err)
		}
		assets["/plain/"+line] = data
		assets["/secure/media/"+line] = encryptFixture(t, data, key, sequence)
		sequence++
	}
	f := &hlsFetcher{client: &http.Client{Transport: hlsTransport(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Referer") != "https://example.com/" {
			return nil, fmt.Errorf("缺少源请求头")
		}
		body, ok := assets[req.URL.Path]
		if !ok {
			return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("missing")), Header: make(http.Header), Request: req}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header), Request: req}, nil
	})}, headers: map[string]string{"Referer": "https://example.com/"}}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, name := range []string{"plain", "secure"} {
		name := name
		wg.Add(1)
		go func() {
			defer wg.Done()
			staging := filepath.Join(t.TempDir(), name)
			if err := os.MkdirAll(staging, 0700); err != nil {
				results <- err
				return
			}
			entry := "index.m3u8"
			if name == "secure" {
				entry = "master.m3u8"
			}
			output := filepath.Join(staging, "media.mp4")
			if err := downloadHLSWithFetcher(context.Background(), f, "https://example.com/"+name+"/"+entry, staging, output, ffmpeg, 3, nil); err != nil {
				results <- err
				return
			}
			results <- probe(context.Background(), ffprobe, output)
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestHLSRejectsIncompleteAndUnsupported(t *testing.T) {
	f := &hlsFetcher{}
	for _, raw := range []string{
		"#EXTM3U\n#EXTINF:1,\nseg.ts\n",
		"#EXTM3U\n#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"key\"\n#EXTINF:1,\nseg.ts\n#EXT-X-ENDLIST\n",
		"#EXTM3U\n#EXT-X-PART:DURATION=0.5,URI=\"part.ts\"\n#EXT-X-ENDLIST\n",
	} {
		if _, _, err := parseHLS(context.Background(), f, raw, "https://example.com/index.m3u8"); err == nil {
			t.Fatalf("未拒绝不支持的片单: %s", raw)
		}
	}
}

func TestHLSQuotedAttributeAndByteRange(t *testing.T) {
	if got := hlsAttr(`METHOD=AES-128,URI="key?token=a,b",IV=0x01`, "URI"); got != "key?token=a,b" {
		t.Fatalf("签名密钥地址被截断: %q", got)
	}
	start, length, err := hlsRange("15@30", 0)
	if err != nil || start != 30 || length != 15 {
		t.Fatalf("显式范围错误: %d %d %v", start, length, err)
	}
	start, length, err = hlsRange("12", start+length)
	if err != nil || start != 45 || length != 12 {
		t.Fatalf("连续范围错误: %d %d %v", start, length, err)
	}
}

func TestEmbeddedHLSFMP4ByteRange(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg 未安装")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("FFprobe 未安装")
	}
	fixture := t.TempDir()
	playlistPath := filepath.Join(fixture, "index.m3u8")
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=10:duration=1.2", "-c:v", "libx264", "-preset", "ultrafast", "-g", "5", "-sc_threshold", "0", "-f", "hls", "-hls_time", "0.5", "-hls_list_size", "0", "-hls_segment_type", "fmp4", "-hls_flags", "single_file", playlistPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("生成 fMP4 样本失败: %v %s", err, output)
	}
	playlist, err := os.ReadFile(playlistPath)
	if err != nil {
		t.Fatal(err)
	}
	mediaName := ""
	for _, line := range strings.Split(string(playlist), "\n") {
		if strings.HasPrefix(line, "#EXT-X-MAP:") {
			mediaName = hlsAttr(strings.TrimPrefix(line, "#EXT-X-MAP:"), "URI")
		}
	}
	if mediaName == "" {
		t.Fatal("样本没有 fMP4 初始化文件")
	}
	media, err := os.ReadFile(filepath.Join(fixture, mediaName))
	if err != nil {
		t.Fatal(err)
	}
	f := &hlsFetcher{client: &http.Client{Transport: hlsTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/index.m3u8" {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(playlist)), Header: make(http.Header), Request: req}, nil
		}
		if req.URL.Path != "/"+mediaName {
			return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("missing")), Header: make(http.Header), Request: req}, nil
		}
		rawRange := req.Header.Get("Range")
		var start, end int
		if _, err := fmt.Sscanf(rawRange, "bytes=%d-%d", &start, &end); err != nil || start < 0 || end >= len(media) || start > end {
			return nil, fmt.Errorf("范围无效: %s", rawRange)
		}
		headers := make(http.Header)
		headers.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(media)))
		return &http.Response{StatusCode: 206, Body: io.NopCloser(bytes.NewReader(media[start : end+1])), Header: headers, Request: req}, nil
	})}}
	staging := t.TempDir()
	output := filepath.Join(staging, "media.mp4")
	if err := downloadHLSWithFetcher(context.Background(), f, "https://example.com/index.m3u8", staging, output, ffmpeg, 4, nil); err != nil {
		t.Fatal(err)
	}
	if err := probe(context.Background(), ffprobe, output); err != nil {
		t.Fatal(err)
	}
}

func TestHLSCancelStopsInflightSegments(t *testing.T) {
	started := make(chan struct{})
	var startedOnce sync.Once
	f := &hlsFetcher{client: &http.Client{Transport: hlsTransport(func(req *http.Request) (*http.Response, error) {
		startedOnce.Do(func() { close(started) })
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- fetchHLSParts(ctx, f, []hlsPart{{url: "https://example.com/a.ts", name: "a.ts"}, {url: "https://example.com/b.ts", name: "b.ts"}}, t.TempDir(), 2, nil)
	}()
	<-started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("暂停未中断分片请求: %v", err)
	}
}

func TestHLSReportsSpeedBeforeSegmentCompletes(t *testing.T) {
	reader, writer := io.Pipe()
	f := &hlsFetcher{client: &http.Client{Transport: hlsTransport(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: reader, Request: req}, nil
	})}}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	defer reader.Close()
	defer writer.Close()
	go func() { _, _ = writer.Write(bytes.Repeat([]byte("a"), 4096)) }()
	live := false
	err := fetchHLSParts(ctx, f, []hlsPart{{url: "https://example.com/a.ts", name: "a.ts"}}, t.TempDir(), 1, func(p float64, speed string) {
		if p == 0 && speed != "0.0 B/s" && speed != "" {
			live = true
			writer.Close()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !live {
		t.Fatal("no speed reported during an unfinished segment")
	}
}
