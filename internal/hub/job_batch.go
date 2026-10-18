package hub

import (
	"context"
	"errors"
	"strings"
	"sync"
)

type JobActionResult struct {
	ID      string `json:"id"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

func (s *Service) BatchJobAction(ctx context.Context, ids []string, action string) ([]JobActionResult, error) {
	switch action {
	case "pause", "resume", "remove", "remove-files":
	default:
		return nil, errors.New("不支持的批量任务操作")
	}
	if len(ids) == 0 {
		return nil, errors.New("请选择要操作的任务")
	}
	results := make([]JobActionResult, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return nil, errors.New("任务 ID 不能为空")
		}
		// long: 同一任务只执行一次，避免重复选择把已成功删除的任务再次报告为失败。
		if !seen[id] {
			seen[id] = true
			results = append(results, JobActionResult{ID: id})
		}
	}
	indices := make(chan int, len(results))
	for i := range results {
		indices <- i
	}
	close(indices)
	var workers sync.WaitGroup
	// long: 等待下载退出可以并行，但限制同时处理的数量，避免大队列创建大量等待协程。
	for range min(4, len(results)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range indices {
				if ctx.Err() != nil {
					results[i].Error = "请求已中断，任务未处理，请刷新队列后重试"
					continue
				}
				var err error
				// long: 批量删除沿用单项的停止下载、文件归属核验和持久化边界，单项失败不阻止其他任务。
				if action == "remove" || action == "remove-files" {
					err = s.DeleteJob(ctx, results[i].ID, action == "remove-files")
				} else {
					err = s.JobAction(results[i].ID, action)
				}
				results[i].Success = err == nil
				if err != nil {
					results[i].Error = err.Error()
				}
			}
		}()
	}
	workers.Wait()
	return results, nil
}
