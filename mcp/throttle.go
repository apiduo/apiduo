package mcp

import (
	"sync"
	"time"
)

// models.list 是唯一不经过 relay 的读入口：没有额度、没有 relay 的限流，
// 一个令牌可以把这条路径打到任意 QPS。快照缓存挡住的是数据库，挡不住请求，
// 所以还要一个 per-token 的窗口。
const (
	catalogListLimit  = 60
	catalogListWindow = time.Minute
)

type rateWindow struct {
	start time.Time
	count int
}

var (
	rateMu    sync.Mutex
	rateTable = make(map[int]*rateWindow)
)

// allowCatalogList 是固定窗口限流。返回需要等待多久（0 表示放行）。
//
// 固定窗口而不是令牌桶：这里只需要「挡住脚本」，不需要平滑突发；
// 少一个依赖、少一份状态，代价只是窗口边界可能放过 2 倍。
func allowCatalogList(tokenId int) (time.Duration, bool) {
	now := time.Now()
	rateMu.Lock()
	defer rateMu.Unlock()

	// 令牌数量有界，但被删掉的令牌不会自己消失。超过阈值时顺手清一遍，
	// 避免长期运行后 map 只增不减。
	if len(rateTable) > 4096 {
		for id, w := range rateTable {
			if now.Sub(w.start) >= catalogListWindow {
				delete(rateTable, id)
			}
		}
	}

	w := rateTable[tokenId]
	if w == nil || now.Sub(w.start) >= catalogListWindow {
		rateTable[tokenId] = &rateWindow{start: now, count: 1}
		return 0, true
	}
	if w.count >= catalogListLimit {
		return catalogListWindow - now.Sub(w.start), false
	}
	w.count++
	return 0, true
}

// resetCatalogRateLimit 清空窗口。测试用：同一个进程里 105 个测试共用一个令牌 0。
func resetCatalogRateLimit() {
	rateMu.Lock()
	rateTable = make(map[int]*rateWindow)
	rateMu.Unlock()
}
