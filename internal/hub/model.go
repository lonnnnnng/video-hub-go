package hub

import "time"

const SourceListURL = "https://raw.githubusercontent.com/lonnnnnng/resources/main/dist/video/lite.json"

type Settings struct {
	SourceURL    string `json:"sourceUrl"`
	MediaDir     string `json:"mediaDir"`
	FFmpeg       string `json:"ffmpeg"`
	FFprobe      string `json:"ffprobe"`
	Workers      int    `json:"workers"`
	Threads      int    `json:"threads"`
	MaxAttempts  int    `json:"maxAttempts"`
	CheckMinutes int    `json:"checkMinutes"`
	EmbyURL      string `json:"embyUrl"`
	EmbyKey      string `json:"embyKey,omitempty"`
	EmbyEnabled  bool   `json:"embyEnabled"`
}

type Source struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	API             string            `json:"api"`
	Detail          string            `json:"detail,omitempty"`
	Enabled         bool              `json:"enabled"`
	Default         bool              `json:"default"`
	DisabledByCheck bool              `json:"disabledByCheck,omitempty"`
	CheckStatus     string            `json:"checkStatus,omitempty"`
	CheckError      string            `json:"checkError,omitempty"`
	LatencyMS       int64             `json:"latencyMs,omitempty"`
	CheckedAt       time.Time         `json:"checkedAt,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
}

type Episode struct {
	Key    string `json:"key"`
	Name   string `json:"name"`
	Number int    `json:"number"`
	URL    string `json:"url"`
}

type Line struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Episodes []Episode `json:"episodes"`
}

type Video struct {
	SourceID   string `json:"sourceId"`
	SourceName string `json:"sourceName"`
	ID         string `json:"id"`
	Title      string `json:"title"`
	Year       string `json:"year"`
	Kind       string `json:"kind"`
	Remarks    string `json:"remarks"`
	Poster     string `json:"poster"`
	Plot       string `json:"plot"`
	Lines      []Line `json:"lines"`
}

type Follow struct {
	ID           string `json:"id"`
	Video        Video  `json:"video"`
	LineID       string `json:"lineId"`
	Season       int    `json:"season"`
	AutoDownload bool   `json:"autoDownload"`
	Enabled      bool   `json:"enabled"`
	// long: 用集名身份保存完整快照，资源站补录中间集或更新签名 URL 时都不会遗漏或重复入队。
	Known     []string  `json:"known"`
	LastCheck time.Time `json:"lastCheck"`
	LastError string    `json:"lastError,omitempty"`
	NewCount  int       `json:"newCount"`
	CreatedAt time.Time `json:"createdAt"`
}

type Job struct {
	ID          string    `json:"id"`
	SourceID    string    `json:"sourceId"`
	VideoID     string    `json:"videoId"`
	Title       string    `json:"title"`
	Year        string    `json:"year"`
	Plot        string    `json:"plot"`
	LineID      string    `json:"lineId"`
	Episode     Episode   `json:"episode"`
	Season      int       `json:"season"`
	Status      string    `json:"status"`
	Progress    float64   `json:"progress"`
	Speed       string    `json:"speed,omitempty"`
	Attempts    int       `json:"attempts"`
	NextAttempt time.Time `json:"nextAttempt"`
	Error       string    `json:"error,omitempty"`
	Output      string    `json:"output,omitempty"`
	// long: 入队时冻结落盘目录，调整全局设置不会把暂停任务的分片和最终文件拆到不同位置。
	MediaDir  string    `json:"mediaDir"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type EmbyState struct {
	Pending     bool      `json:"pending"`
	Revision    uint64    `json:"revision"`
	LastRequest time.Time `json:"lastRequest"`
	NextAttempt time.Time `json:"nextAttempt"`
	Error       string    `json:"error,omitempty"`
}

type State struct {
	Version          int       `json:"version"`
	Settings         Settings  `json:"settings"`
	Sources          []Source  `json:"sources"`
	Follows          []Follow  `json:"follows"`
	Jobs             []Job     `json:"jobs"`
	Emby             EmbyState `json:"emby"`
	SourcesUpdatedAt time.Time `json:"sourcesUpdatedAt"`
}

func defaultState(mediaDir string) State {
	return State{Version: 1, Settings: Settings{SourceURL: SourceListURL, MediaDir: mediaDir,
		FFmpeg: "ffmpeg", FFprobe: "ffprobe", Workers: 2, Threads: 8,
		MaxAttempts: 3, CheckMinutes: 30}, Sources: []Source{}, Follows: []Follow{}, Jobs: []Job{}}
}
