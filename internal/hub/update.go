package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const releaseRepo = "lonnnnnng/video-hub-go"

var releaseVersion = regexp.MustCompile(`^v([0-9]+)\.([0-9]+)\.([0-9]+)$`)
var serviceNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

type githubRelease struct {
	Tag         string `json:"tag_name"`
	PublishedAt string `json:"published_at"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
	Assets      []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}
type updateTarget struct{ Binary, Service, Scope, HealthURL string }
type updateRecord struct {
	State     string        `json:"state"`
	Version   string        `json:"version"`
	Message   string        `json:"message"`
	StartedAt time.Time     `json:"startedAt"`
	UpdatedAt time.Time     `json:"updatedAt"`
	Target    updateTarget  `json:"target"`
	Release   githubRelease `json:"release"`
}
type updateInfo struct {
	Version          string    `json:"version"`
	ReleasedAt       string    `json:"releasedAt"`
	LatestVersion    string    `json:"latestVersion,omitempty"`
	LatestReleasedAt string    `json:"latestReleasedAt,omitempty"`
	Available        bool      `json:"available"`
	Supported        bool      `json:"supported"`
	Reason           string    `json:"reason,omitempty"`
	CheckedAt        time.Time `json:"checkedAt"`
	State            string    `json:"state"`
	Message          string    `json:"message,omitempty"`
	TargetVersion    string    `json:"targetVersion,omitempty"`
}
type updateManager struct {
	mu             sync.Mutex
	path           string
	client         *http.Client
	latest         githubRelease
	checkedAt      time.Time
	releasedAt     string
	currentChecked bool
}

func newUpdateManager(dataDir string) *updateManager {
	abs, _ := filepath.Abs(dataDir)
	return &updateManager{path: filepath.Join(abs, "web-update.json"), client: releaseClient(), releasedAt: ReleaseTime}
}
func releaseClient() *http.Client {
	return &http.Client{Timeout: 3 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 || !trustedReleaseURL(req.URL) {
			return errors.New("Release 重定向地址不受信任")
		}
		return nil
	}}
}
func trustedReleaseURL(u *url.URL) bool {
	if u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	switch u.Hostname() {
	case "api.github.com", "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
		return true
	}
	return false
}
func releaseRead(ctx context.Context, client *http.Client, address string, limit int64) ([]byte, error) {
	u, err := url.Parse(address)
	if err != nil || !trustedReleaseURL(u) {
		return nil, errors.New("Release 地址不受信任")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "video-hub-go-updater")
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := client.Do(req)
	if err != nil {
		return nil, errors.New("无法连接 GitHub，请检查服务器网络后重试")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub 返回 HTTP %d，请稍后重试", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, errors.New("Release 下载中断，请重试")
	}
	if int64(len(b)) > limit {
		return nil, errors.New("Release 文件超过大小限制")
	}
	return b, nil
}
func readRelease(ctx context.Context, client *http.Client, suffix string) (githubRelease, error) {
	var release githubRelease
	b, err := releaseRead(ctx, client, "https://api.github.com/repos/"+releaseRepo+"/releases/"+suffix, 1<<20)
	if err != nil {
		return release, err
	}
	if json.Unmarshal(b, &release) != nil || !releaseVersion.MatchString(release.Tag) || release.Draft || release.Prerelease {
		return release, errors.New("GitHub Release 不是有效正式版本")
	}
	if _, err = time.Parse(time.RFC3339, release.PublishedAt); err != nil {
		return release, errors.New("Release 发行时间无效")
	}
	return release, nil
}
func newerRelease(latest, current string) bool {
	a, b := releaseVersion.FindStringSubmatch(latest), releaseVersion.FindStringSubmatch(current)
	if a == nil || b == nil {
		return false
	}
	for i := 1; i <= 3; i++ {
		x, e := strconv.ParseUint(a[i], 10, 64)
		y, f := strconv.ParseUint(b[i], 10, 64)
		if e != nil || f != nil {
			return false
		}
		if x != y {
			return x > y
		}
	}
	return false
}
func releaseAsset(release githubRelease, name string) (string, error) {
	found := ""
	for _, asset := range release.Assets {
		if asset.Name == name {
			if found != "" {
				return "", errors.New("Release 资产重复")
			}
			found = asset.URL
		}
	}
	prefix := "https://github.com/" + releaseRepo + "/releases/download/" + release.Tag + "/"
	if found != prefix+name {
		return "", errors.New("Release 缺少有效的 " + name)
	}
	return found, nil
}
func updateCommand(ctx context.Context, scope string, args ...string) ([]byte, error) {
	if scope == "user" {
		args = append([]string{"--user"}, args...)
	}
	return exec.CommandContext(ctx, "systemctl", args...).Output()
}
func localUpdateTarget(ctx context.Context) (updateTarget, string) {
	target := updateTarget{Service: os.Getenv("MOVIEHUB_SERVICE_NAME"), Scope: os.Getenv("MOVIEHUB_SYSTEMD_SCOPE")}
	if target.Service == "" {
		target.Service = "video-hub-go"
	}
	if target.Scope == "" {
		target.Scope = "user"
	}
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return target, "网页更新支持 Linux amd64/arm64 的 systemd 服务"
	}
	if !serviceNamePattern.MatchString(target.Service) || (target.Scope != "user" && target.Scope != "system") {
		return target, "服务管理环境变量无效"
	}
	if target.Scope == "system" && os.Geteuid() != 0 {
		return target, "当前系统服务用户无权在线重启，请使用管理脚本升级"
	}
	for _, name := range []string{"systemctl", "systemd-run"} {
		if _, err := exec.LookPath(name); err != nil {
			return target, "网页更新需要 systemd-run 与 systemctl"
		}
	}
	pid, err := updateCommand(ctx, target.Scope, "show", target.Service+".service", "-p", "MainPID", "--value")
	if err != nil || strings.TrimSpace(string(pid)) != strconv.Itoa(os.Getpid()) {
		return target, "当前进程未由指定的 systemd 服务管理"
	}
	target.Binary, err = os.Executable()
	if err != nil {
		return target, "无法定位当前程序"
	}
	info, err := os.Lstat(target.Binary)
	if err != nil || !info.Mode().IsRegular() {
		return target, "当前程序必须是可替换的普通文件"
	}
	f, err := os.CreateTemp(filepath.Dir(target.Binary), ".update-permission-*")
	if err != nil {
		return target, "程序目录不可写，请使用管理脚本升级"
	}
	f.Close()
	os.Remove(f.Name())
	output, err := updateCommand(ctx, target.Scope, "show", target.Service+".service", "-p", "ExecStart", "--value")
	if err != nil {
		return target, "无法读取 systemd 监听地址"
	}
	match := regexp.MustCompile(`(?:^|\s)--listen(?:=|\s+)([^\s;]+)`).FindStringSubmatch(string(output))
	if len(match) != 2 {
		return target, "systemd 启动参数缺少 --listen"
	}
	host, port, err := net.SplitHostPort(match[1])
	if err != nil {
		return target, "systemd 监听地址无效"
	}
	if host == "0.0.0.0" || host == "" {
		host = "127.0.0.1"
	}
	if host == "::" {
		host = "::1"
	}
	if net.ParseIP(host) == nil && host != "localhost" {
		return target, "健康检查需要本机 IP 或 localhost"
	}
	target.HealthURL = "http://" + net.JoinHostPort(host, port) + "/healthz"
	return target, ""
}
func readUpdateRecord(path string) (updateRecord, error) {
	var record updateRecord
	b, err := os.ReadFile(path)
	if err != nil {
		return record, err
	}
	err = json.Unmarshal(b, &record)
	return record, err
}
func saveUpdateRecord(path string, record updateRecord) error {
	record.UpdatedAt = time.Now()
	b, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return atomicWrite(path, b, 0600)
}
func (u *updateManager) info(ctx context.Context, check bool) (updateInfo, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if (!u.currentChecked || check) && releaseVersion.MatchString(Version) && u.releasedAt == "" {
		u.currentChecked = true
		release, err := readRelease(ctx, u.client, "tags/"+Version)
		if err == nil && release.Tag == Version {
			u.releasedAt = release.PublishedAt
		}
	}
	if check {
		latest, err := readRelease(ctx, u.client, "latest")
		if err != nil {
			return updateInfo{}, err
		}
		if runtime.GOOS == "linux" {
			for _, name := range []string{"moviehub-linux-" + runtime.GOARCH, "SHA256SUMS"} {
				if _, err := releaseAsset(latest, name); err != nil {
					return updateInfo{}, err
				}
			}
		}
		u.latest = latest
		u.checkedAt = time.Now()
		if latest.Tag == Version {
			u.releasedAt = latest.PublishedAt
		}
	}
	_, reason := localUpdateTarget(ctx)
	result := updateInfo{Version: Version, ReleasedAt: u.releasedAt, LatestVersion: u.latest.Tag, LatestReleasedAt: u.latest.PublishedAt, Available: newerRelease(u.latest.Tag, Version), Supported: reason == "", Reason: reason, CheckedAt: u.checkedAt, State: "idle"}
	record, err := readUpdateRecord(u.path)
	if err == nil {
		result.State, result.Message, result.TargetVersion = record.State, record.Message, record.Version
		if record.State == "running" && time.Since(record.StartedAt) > 5*time.Second {
			state, e := updateCommand(ctx, record.Target.Scope, "is-active", record.Target.Service+"-web-update.service")
			if e != nil || strings.TrimSpace(string(state)) != "active" {
				result.State = "failed"
				result.Message = "更新任务已退出，请检查服务日志后重试"
			}
		}
	}
	return result, nil
}
func (u *updateManager) start(ctx context.Context, version string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if version != u.latest.Tag || !newerRelease(version, Version) || time.Since(u.checkedAt) > 30*time.Minute {
		return errors.New("请重新检测更新后选择最新版本")
	}
	target, reason := localUpdateTarget(ctx)
	if reason != "" {
		return errors.New(reason)
	}
	unit := target.Service + "-web-update.service"
	if state, _ := updateCommand(ctx, target.Scope, "is-active", unit); strings.TrimSpace(string(state)) == "active" {
		return errors.New("正在更新，请等待完成")
	}
	record := updateRecord{State: "running", Version: version, Message: "正在下载并校验更新…", StartedAt: time.Now(), Target: target, Release: u.latest}
	if err := saveUpdateRecord(u.path, record); err != nil {
		return errors.New("无法保存更新任务")
	}
	args := []string{"--collect", "--unit=" + target.Service + "-web-update", "--property=Type=exec", "--property=UMask=0077"}
	if target.Scope == "user" {
		args = append([]string{"--user"}, args...)
	}
	// long: 更新监督程序独立于主服务的 cgroup，主服务重启后仍继续核验目标版本并在失败时回滚。
	args = append(args, "--", target.Binary, "--update-worker", u.path)
	if err := exec.CommandContext(ctx, "systemd-run", args...).Run(); err != nil {
		record.State = "failed"
		record.Message = "无法启动独立更新任务，请查看 systemd 日志"
		_ = saveUpdateRecord(u.path, record)
		return errors.New(record.Message)
	}
	slog.Info("网页更新任务已启动", "version", version, "service", target.Service)
	return nil
}

// RunUpdateWorker 由独立 systemd 任务调用；long: HTTP 请求仅选择已核验版本，不能传入命令或下载地址。
func RunUpdateWorker(path string) error {
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("已有更新任务正在执行")
	}
	record, err := readUpdateRecord(path)
	if err != nil {
		return err
	}
	binary, err := os.Executable()
	if err != nil || binary != record.Target.Binary || record.State != "running" || !serviceNamePattern.MatchString(record.Target.Service) || (record.Target.Scope != "user" && record.Target.Scope != "system") {
		return errors.New("更新任务参数无效")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	restart := func() error {
		_, err := updateCommand(ctx, record.Target.Scope, "restart", record.Target.Service+".service")
		return err
	}
	health := func(version string) bool {
		client := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		for i := 0; i < 30; i++ {
			req, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, record.Target.HealthURL, nil)
			if requestErr != nil {
				return false
			}
			res, err := client.Do(req)
			if err == nil {
				var h struct{ Status, Version string }
				e := json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&h)
				res.Body.Close()
				if e == nil && res.StatusCode == 200 && h.Status == "ok" && h.Version == version {
					return true
				}
			}
			select {
			case <-ctx.Done():
				return false
			case <-time.After(time.Second):
			}
		}
		return false
	}
	slog.Info("开始执行网页更新", "version", record.Version, "service", record.Target.Service)
	err = installRelease(ctx, releaseClient(), record, restart, health, func(message string) {
		slog.Info("网页更新进度", "version", record.Version, "message", message)
		record.Message = message
		_ = saveUpdateRecord(path, record)
	})
	record.State = "completed"
	record.Message = "更新完成"
	if err != nil {
		record.State = "failed"
		record.Message = err.Error()
		slog.Error("网页更新失败", "version", record.Version, "error", err)
	} else {
		slog.Info("网页更新完成", "version", record.Version)
	}
	if writeErr := saveUpdateRecord(path, record); writeErr != nil {
		return writeErr
	}
	return err
}
func installRelease(ctx context.Context, client *http.Client, record updateRecord, restart func() error, healthy func(string) bool, report func(string)) error {
	if !releaseVersion.MatchString(record.Version) || record.Release.Tag != record.Version {
		return errors.New("更新版本无效")
	}
	name := "moviehub-linux-" + runtime.GOARCH
	address, err := releaseAsset(record.Release, name)
	if err != nil {
		return err
	}
	sumsURL, err := releaseAsset(record.Release, "SHA256SUMS")
	if err != nil {
		return err
	}
	sums, err := releaseRead(ctx, client, sumsURL, 1<<20)
	if err != nil {
		return err
	}
	expected := ""
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == name {
			if expected != "" {
				return errors.New("SHA256SUMS 校验项重复")
			}
			expected = strings.ToLower(fields[0])
		}
	}
	digest, e := hex.DecodeString(expected)
	if e != nil || len(digest) != sha256.Size {
		return errors.New("SHA256SUMS 缺少有效校验项")
	}
	binary, err := releaseRead(ctx, client, address, 128<<20)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(binary)
	if hex.EncodeToString(hash[:]) != expected {
		return errors.New("程序 SHA256 校验失败，未替换旧程序")
	}
	if len(binary) < 4 || string(binary[:4]) != "\x7fELF" {
		return errors.New("Release 程序不是 Linux 可执行文件")
	}
	info, err := os.Lstat(record.Target.Binary)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("当前程序不是普通文件")
	}
	stage, err := os.CreateTemp(filepath.Dir(record.Target.Binary), ".moviehub-update-*")
	if err != nil {
		return errors.New("程序目录不可写")
	}
	defer os.Remove(stage.Name())
	if _, err = stage.Write(binary); err == nil {
		err = stage.Chmod(0755)
	}
	if err == nil {
		err = stage.Sync()
	}
	closeErr := stage.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return errors.New("写入更新程序失败")
	}
	backup := record.Target.Binary + ".previous-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err = os.Link(record.Target.Binary, backup); err != nil {
		return errors.New("备份旧程序失败，未替换程序")
	}
	if err = os.Rename(stage.Name(), record.Target.Binary); err != nil {
		return errors.New("替换程序失败，旧程序已备份")
	}
	report("正在重启服务并核验版本…")
	if err = restart(); err == nil && healthy(record.Version) {
		return nil
	}
	// long: 健康检查失败后只回退二进制，保留 JSON 数据和媒体；回滚结果明确反馈给管理台。
	if err = os.Rename(backup, record.Target.Binary); err != nil {
		return errors.New("新版本启动失败且恢复旧程序失败，请人工检查备份")
	}
	if err = restart(); err != nil {
		return errors.New("已恢复旧程序但重启失败，请检查服务日志")
	}
	if !healthy(Version) {
		return errors.New("已恢复旧程序，但健康检查仍失败，请检查服务日志")
	}
	return errors.New("新版本启动失败，已回滚到旧版本")
}
