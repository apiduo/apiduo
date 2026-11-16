package service

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RateLimitSnapshot 是某个渠道最近一次观测到的上游额度水位。
//
// 只放内存，不落库。理由：这是分钟级的瞬时状态，落库要走请求路径，
// 而这条路径上每加一次 IO 都会乘以全部流量。代价是进程重启后归零、
// 多实例不共享——对一个「现在哪个渠道快打满」的看板来说可以接受，
// 因为它下一秒就会被下一次请求刷新。
type RateLimitSnapshot struct {
	ChannelId int    `json:"channel_id"`
	Limit     int    `json:"limit"`
	Remaining int    `json:"remaining"`
	ResetRaw  string `json:"reset"`
	Source    string `json:"source"`
	UpdatedAt int64  `json:"updated_at"`
}

var (
	rateLimitMu    sync.RWMutex
	rateLimitCache = map[int]RateLimitSnapshot{}
)

// 上游没有统一标准：OpenAI 用 x-ratelimit-*，Anthropic 用 anthropic-ratelimit-*。
// 两套都认，优先级按出现顺序。
var rateLimitHeaderSets = []struct {
	source     string
	limitKeys  []string
	remainKeys []string
	resetKeys  []string
}{
	{
		source:     "openai",
		limitKeys:  []string{"x-ratelimit-limit-requests", "x-ratelimit-limit-tokens"},
		remainKeys: []string{"x-ratelimit-remaining-requests", "x-ratelimit-remaining-tokens"},
		resetKeys:  []string{"x-ratelimit-reset-requests", "x-ratelimit-reset-tokens"},
	},
	{
		source:     "anthropic",
		limitKeys:  []string{"anthropic-ratelimit-requests-limit", "anthropic-ratelimit-tokens-limit"},
		remainKeys: []string{"anthropic-ratelimit-requests-remaining", "anthropic-ratelimit-tokens-remaining"},
		resetKeys:  []string{"anthropic-ratelimit-requests-reset", "anthropic-ratelimit-tokens-reset"},
	},
}

// ObserveRateLimit 读取上游响应头里的额度水位。
//
// 纯内存操作：几次 map 查找加一次加锁，不产生 IO，不阻塞转发。
// 没有相关响应头时立刻返回，绝大多数请求走的就是这条路径。
func ObserveRateLimit(channelId int, header http.Header) {
	if channelId <= 0 || header == nil {
		return
	}
	snap, ok := parseRateLimitHeaders(header)
	if !ok {
		return
	}
	snap.ChannelId = channelId
	snap.UpdatedAt = time.Now().Unix()
	rateLimitMu.Lock()
	rateLimitCache[channelId] = snap
	rateLimitMu.Unlock()
}

func parseRateLimitHeaders(header http.Header) (RateLimitSnapshot, bool) {
	for _, set := range rateLimitHeaderSets {
		remainingRaw := firstHeader(header, set.remainKeys)
		if remainingRaw == "" {
			continue
		}
		remaining, err := strconv.Atoi(strings.TrimSpace(remainingRaw))
		if err != nil {
			continue
		}
		limit := 0
		if v := firstHeader(header, set.limitKeys); v != "" {
			limit, _ = strconv.Atoi(strings.TrimSpace(v))
		}
		return RateLimitSnapshot{
			Limit:     limit,
			Remaining: remaining,
			ResetRaw:  firstHeader(header, set.resetKeys),
			Source:    set.source,
		}, true
	}
	return RateLimitSnapshot{}, false
}

func firstHeader(header http.Header, keys []string) string {
	for _, k := range keys {
		if v := header.Get(k); v != "" {
			return v
		}
	}
	return ""
}

// SnapshotRateLimits 返回各渠道最近一次观测到的水位，按剩余比例升序
// （最紧张的排前面）。
func SnapshotRateLimits() []RateLimitSnapshot {
	rateLimitMu.RLock()
	out := make([]RateLimitSnapshot, 0, len(rateLimitCache))
	for _, v := range rateLimitCache {
		out = append(out, v)
	}
	rateLimitMu.RUnlock()
	ratio := func(s RateLimitSnapshot) float64 {
		if s.Limit <= 0 {
			return 2 // 没有总量信息，排在有比例的后面
		}
		return float64(s.Remaining) / float64(s.Limit)
	}
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if ratio(out[j]) < ratio(out[i]) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// RateLimitCacheForAPI 是给 controller 的只读入口。
func RateLimitCacheForAPI() []RateLimitSnapshot { return SnapshotRateLimits() }
