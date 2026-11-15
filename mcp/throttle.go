package mcp

import (
	"sync"
	"time"
)

// 读类工具的 per-token 窗口。
//
// models.list 与 task.get 都不经过 relay：没有额度、没有 relay 的限流，
// 一个令牌可以把它们打到任意 QPS。快照缓存挡的是数据库，挡不住请求。
const (
	catalogListLimit = 60
	// 一个任务按 poll_after_ms(5s) 轮询是 12 次/分钟，300 次够同时盯 25 个任务；
	// 120 只够 10 个，多开几个视频就会被自己的轮询卡住。
	taskGetLimit  = 300
	rateWindowLen = time.Minute
)

type rateWindow struct {
	start time.Time
	count int
}

// rateKey 带上桶名：否则 models.list 的 60 次额度会把 task.get 一起吃掉。
type rateKey struct {
	bucket string
	token  int
}

var (
	rateMu    sync.Mutex
	rateTable = make(map[rateKey]*rateWindow)
)

// allowRate 是固定窗口限流。返回需要等待多久（0 表示放行）。
//
// 固定窗口而不是令牌桶：这里只需要「挡住脚本」，不需要平滑突发；
// 少一个依赖、少一份状态，代价只是窗口边界可能放过 2 倍。
func allowRate(bucket string, tokenId, limit int) (time.Duration, bool) {
	now := time.Now()
	key := rateKey{bucket: bucket, token: tokenId}
	rateMu.Lock()
	defer rateMu.Unlock()

	// 令牌数量有界，但被删掉的令牌不会自己消失。超过阈值时顺手清一遍，
	// 避免长期运行后 map 只增不减。
	if len(rateTable) > 4096 {
		for k, w := range rateTable {
			if now.Sub(w.start) >= rateWindowLen {
				delete(rateTable, k)
			}
		}
	}

	w := rateTable[key]
	if w == nil || now.Sub(w.start) >= rateWindowLen {
		rateTable[key] = &rateWindow{start: now, count: 1}
		return 0, true
	}
	if w.count >= limit {
		return rateWindowLen - now.Sub(w.start), false
	}
	w.count++
	return 0, true
}

func allowCatalogList(tokenId int) (time.Duration, bool) {
	return allowRate("models.list", tokenId, LoadSettings().ModelsListPerMin)
}

func allowTaskGet(tokenId int) (time.Duration, bool) {
	return allowRate("task.get", tokenId, LoadSettings().TaskGetPerMin)
}

// resetRateLimits 清空窗口。测试用：同一个进程里上百个测试共用一个令牌。
func resetRateLimits() {
	rateMu.Lock()
	rateTable = make(map[rateKey]*rateWindow)
	rateMu.Unlock()
}

// resetCatalogRateLimit 保留旧名字，测试里已有调用点。
func resetCatalogRateLimit() { resetRateLimits() }
