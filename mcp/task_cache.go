package mcp

import (
	"fmt"
	"sync"

	"github.com/QuantumNous/new-api/common"
)

// taskStatusCache 记「每个任务上一次下发时的状态」。
//
// 用途只有一个：状态没变时不要重复下发同一份结果。轮询是按 poll_after_ms
// 来的，一个 3 分钟的视频会有 30 多次返回，其中绝大多数只是「还是 running」。
//
// 进程内、按 token+task_id 记账：多副本各存各的，最坏情况是多下发一次完整
// 结果，不影响正确性。终态（completed/failed）在 TTL 内可以直接返回，连
// relay 都不用查 —— 终态不会再变。
type taskCacheEntry struct {
	status string
	out    taskOutput
	at     int64
}

const (
	taskCacheMaxEntries = 4096
	taskCacheMaxAge     = 3600 // 秒，超过这个年龄的条目在清理时丢弃
)

var (
	taskCacheMu sync.Mutex
	taskCache   = make(map[string]taskCacheEntry)
)

func taskCacheKey(tokenID int, taskID string) string {
	return fmt.Sprintf("%d|%s", tokenID, taskID)
}

// loadTaskCache 取条目。ttl<=0 时不看年龄，只用于「比较状态」。
func loadTaskCache(key string, ttl int) (taskCacheEntry, bool) {
	taskCacheMu.Lock()
	defer taskCacheMu.Unlock()
	e, ok := taskCache[key]
	if !ok {
		return taskCacheEntry{}, false
	}
	if ttl > 0 && common.GetTimestamp()-e.at > int64(ttl) {
		return taskCacheEntry{}, false
	}
	return e, true
}

func storeTaskCache(key, status string, out taskOutput) {
	now := common.GetTimestamp()
	taskCacheMu.Lock()
	defer taskCacheMu.Unlock()
	if len(taskCache) >= taskCacheMaxEntries {
		for k, e := range taskCache {
			if now-e.at > taskCacheMaxAge {
				delete(taskCache, k)
			}
		}
		// 清完还是满的：丢掉最老的一批，避免无界增长。
		if len(taskCache) >= taskCacheMaxEntries {
			oldest, oldestAt := "", int64(1<<62)
			for k, e := range taskCache {
				if e.at < oldestAt {
					oldest, oldestAt = k, e.at
				}
			}
			delete(taskCache, oldest)
		}
	}
	taskCache[key] = taskCacheEntry{status: status, out: out, at: now}
}

func resetTaskCache() {
	taskCacheMu.Lock()
	taskCache = make(map[string]taskCacheEntry)
	taskCacheMu.Unlock()
}
