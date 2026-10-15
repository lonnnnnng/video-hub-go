package hub

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

var stateFiles = [...]string{"settings.json", "sources.json", "follows.json", "jobs.json", "emby.json"}

const transactionFile = "transaction.json"

type sourcesStateFile struct {
	Sources          []Source  `json:"sources"`
	SourcesUpdatedAt time.Time `json:"sourcesUpdatedAt"`
}

type stateTransaction struct {
	Version int                        `json:"version"`
	Files   map[string]json.RawMessage `json:"files"`
}

type Store struct {
	mu    sync.RWMutex
	state State
	dir   string
	lock  *os.File
}

func OpenStore(dir, mediaDir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "service.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	// long: 文件重命名只保证单份 JSON 完整，进程锁还要阻止两个服务同时提交不同快照。
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("数据目录已被另一个进程占用: %w", err)
	}
	s := &Store{dir: dir, lock: lock}
	if err = s.recoverTransaction(); err == nil {
		err = s.load(mediaDir)
	}
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("读取状态失败，原文件已保留: %w", err)
	}
	// long: 旧站源没有默认源标记；读取后补齐选择，下一次站源更新会写回对应文件。
	sortSources(s.state.Sources)
	chooseDefaultSource(s.state.Sources)
	slog.Info("JSON 状态已加载", "sources", len(s.state.Sources), "follows", len(s.state.Follows), "jobs", len(s.state.Jobs), "emby_pending", s.state.Emby.Pending)
	return s, nil
}

func (s *Store) load(mediaDir string) error {
	count := 0
	for _, name := range stateFiles {
		_, err := os.Stat(filepath.Join(s.dir, name))
		if err == nil {
			count++
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if count == len(stateFiles) {
		return s.loadSections(mediaDir)
	}
	if count != 0 {
		return fmt.Errorf("分类状态文件不完整（%d/%d），请检查数据目录", count, len(stateFiles))
	}

	previous := defaultState(mediaDir)
	legacy := filepath.Join(s.dir, "state.json")
	b, err := os.ReadFile(legacy)
	if err == nil {
		if err = json.Unmarshal(b, &previous); err != nil {
			return fmt.Errorf("旧状态 state.json 无效: %w", err)
		}
		if previous.Version != 1 {
			return fmt.Errorf("不支持的旧状态版本 %d", previous.Version)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// long: 首次启动和旧版迁移使用同一事务提交；崩溃后可重放，旧 state.json 始终保留。
	committed, err := s.commitSections(previous, nil)
	if committed {
		s.state = previous
	}
	return err
}

func (s *Store) loadSections(mediaDir string) error {
	state := defaultState(mediaDir)
	var sources sourcesStateFile
	items := []struct {
		name  string
		value any
	}{
		{"settings.json", &state.Settings},
		{"sources.json", &sources},
		{"follows.json", &state.Follows},
		{"jobs.json", &state.Jobs},
		{"emby.json", &state.Emby},
	}
	for _, item := range items {
		b, err := os.ReadFile(filepath.Join(s.dir, item.name))
		if err != nil {
			return fmt.Errorf("读取 %s: %w", item.name, err)
		}
		if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
			return fmt.Errorf("%s 不能为 null", item.name)
		}
		if err = json.Unmarshal(b, item.value); err != nil {
			return fmt.Errorf("解析 %s: %w", item.name, err)
		}
	}
	state.Sources = sources.Sources
	state.SourcesUpdatedAt = sources.SourcesUpdatedAt
	s.state = state
	return nil
}

func (s *Store) Close() error { return s.lock.Close() }

func cloneState(value State) State {
	b, _ := json.Marshal(value)
	var result State
	_ = json.Unmarshal(b, &result)
	return result
}

func (s *Store) Snapshot() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneState(s.state)
}

func (s *Store) Update(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.recoverTransaction(); err != nil {
		slog.Error("JSON 事务恢复失败", "error", err)
		return err
	}
	next := cloneState(s.state)
	if err := fn(&next); err != nil {
		return err
	}
	if next.Version != 1 {
		return fmt.Errorf("不支持的状态版本 %d", next.Version)
	}
	committed, err := s.commitSections(next, &s.state)
	if committed {
		// long: 事务记录写入后，重启也会推进到 next；内存必须与可恢复的逻辑状态一致。
		s.state = next
	}
	if err != nil {
		// long: 已写入事务记录但目录同步失败时，内存状态仍已推进；日志必须标明提交状态以便 Linux 运维判断恢复路径。
		slog.Error("JSON 状态提交失败", "committed", committed, "error", err)
	}
	return err
}

func sectionJSON(state State) (map[string]json.RawMessage, error) {
	values := []struct {
		name  string
		value any
	}{
		{"settings.json", state.Settings},
		{"sources.json", sourcesStateFile{Sources: state.Sources, SourcesUpdatedAt: state.SourcesUpdatedAt}},
		{"follows.json", state.Follows},
		{"jobs.json", state.Jobs},
		{"emby.json", state.Emby},
	}
	result := make(map[string]json.RawMessage, len(values))
	for _, item := range values {
		b, err := json.MarshalIndent(item.value, "", "  ")
		if err != nil {
			return nil, err
		}
		result[item.name] = append(b, '\n')
	}
	return result, nil
}

func (s *Store) commitSections(next State, previous *State) (bool, error) {
	newFiles, err := sectionJSON(next)
	if err != nil {
		return false, err
	}
	var oldFiles map[string]json.RawMessage
	if previous != nil {
		oldFiles, err = sectionJSON(*previous)
		if err != nil {
			return false, err
		}
	}
	changed := make(map[string]json.RawMessage)
	for _, name := range stateFiles {
		if previous != nil && bytes.Equal(newFiles[name], oldFiles[name]) {
			continue
		}
		changed[name] = newFiles[name]
		if previous != nil {
			// long: 每项单独留一份更新前快照；备份失败时不创建事务，也不改变正式文件。
			if err = atomicWrite(filepath.Join(s.dir, name+".bak"), oldFiles[name], 0600); err != nil {
				return false, err
			}
		}
	}
	if len(changed) == 0 {
		return false, nil
	}
	tx := stateTransaction{Version: 1, Files: changed}
	committed, journalErr := atomicJSONCommit(filepath.Join(s.dir, transactionFile), tx)
	if !committed {
		return false, journalErr
	}
	// long: 一次更新可能同时改变追更与队列，或队列与 Emby；事务记录使逐个 rename 可在重启后补齐。
	applyErr := s.applyTransaction(tx)
	return true, errors.Join(journalErr, applyErr)
}

func (s *Store) recoverTransaction() error {
	b, err := os.ReadFile(filepath.Join(s.dir, transactionFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	slog.Warn("发现未完成的 JSON 事务，开始重放")
	var tx stateTransaction
	if err = json.Unmarshal(b, &tx); err != nil {
		return fmt.Errorf("事务记录损坏: %w", err)
	}
	return s.applyTransaction(tx)
}

func (s *Store) applyTransaction(tx stateTransaction) error {
	if tx.Version != 1 || len(tx.Files) == 0 || len(tx.Files) > len(stateFiles) {
		return errors.New("事务记录版本或文件数量无效")
	}
	for name, payload := range tx.Files {
		validName := false
		for _, known := range stateFiles {
			validName = validName || name == known
		}
		if !validName || bytes.Equal(bytes.TrimSpace(payload), []byte("null")) {
			return fmt.Errorf("事务记录包含无效的 %q", name)
		}
		// long: 先检查全部分类的 JSON 类型，再写任何正式文件，防止损坏的事务记录造成部分覆盖。
		var target any
		switch name {
		case "settings.json":
			target = &Settings{}
		case "sources.json":
			target = &sourcesStateFile{}
		case "follows.json":
			target = &[]Follow{}
		case "jobs.json":
			target = &[]Job{}
		case "emby.json":
			target = &EmbyState{}
		}
		if err := json.Unmarshal(payload, target); err != nil {
			return fmt.Errorf("事务记录中 %s 无效: %w", name, err)
		}
	}
	for _, name := range stateFiles {
		payload, ok := tx.Files[name]
		if !ok {
			continue
		}
		if err := atomicWrite(filepath.Join(s.dir, name), payload, 0600); err != nil {
			return fmt.Errorf("提交 %s: %w", name, err)
		}
	}
	if err := os.Remove(filepath.Join(s.dir, transactionFile)); err != nil {
		return err
	}
	// long: 文件内容先各自落盘，最后同步事务记录的删除；重放多一次也不会改变最终内容。
	return syncDirectory(s.dir)
}

func atomicJSON(path string, value any) error {
	_, err := atomicJSONCommit(path, value)
	return err
}

func atomicJSONCommit(path string, value any) (bool, error) {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return false, err
	}
	return atomicWriteCommit(path, append(b, '\n'), 0600)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	_, err := atomicWriteCommit(path, data, mode)
	return err
}

func atomicWriteCommit(path string, data []byte, mode os.FileMode) (committed bool, err error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".write-*")
	if err != nil {
		return false, err
	}
	name := f.Name()
	defer func() {
		f.Close()
		if err != nil {
			_ = os.Remove(name)
		}
	}()
	if err = f.Chmod(mode); err != nil {
		return false, err
	}
	if _, err = f.Write(data); err != nil {
		return false, err
	}
	if err = f.Sync(); err != nil {
		return false, err
	}
	if err = f.Close(); err != nil {
		return false, err
	}
	if err = os.Rename(name, path); err != nil {
		return false, err
	}
	// long: 临时文件与目标同目录，rename 后同步目录，避免断电时只保存了内容却丢失目录项。
	if err = syncDirectory(filepath.Dir(path)); err != nil {
		return true, fmt.Errorf("JSON 已提交但目录同步失败，持久性未确认: %w", err)
	}
	return true, nil
}

func syncDirectory(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
