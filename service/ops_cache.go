package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/model"
)

// opsCache 是分析接口的结果缓存。
//
// 为什么必须有：看板是 60 秒轮询的，同参数查询会被重复打到几十次。
// 每次查询要把全部原始日志取进内存排序算分位，20 万行时单次 260–320ms，
// 几个并发就能把 CPU 吃满，relay 转发跟着劣化（实测吞吐从 85 掉到 23）。
//
// TTL 取 30 秒而不是 60：轮询间隔是 60 秒，但用户可能手动点刷新，
// 30 秒能让「刚刷新过」的场景直接命中，又不会让数据看起来太旧。
type opsCacheEntry struct {
	value  any
	expire time.Time
}

var (
	opsCacheMu sync.RWMutex
	opsCache   = map[string]opsCacheEntry{}
)

const opsCacheTTL = 30 * time.Second

// cachedOps 命中返回缓存，否则执行 fn 并写入。
//
// 没有做 singleflight：缓存未命中时的并发查询仍会各自执行。
// 这是刻意的取舍——加 singleflight 需要引入额外依赖或手写等待队列，
// 而真实场景下「几十个人同一秒点开看板」并不存在，
// 限流（2 req/s）已经把这里的并发压得很低。
func cachedOps[T any](key string, fn func() (T, error)) (T, error) {
	opsCacheMu.RLock()
	entry, ok := opsCache[key]
	opsCacheMu.RUnlock()
	if ok && time.Now().Before(entry.expire) {
		if v, typeOK := entry.value.(T); typeOK {
			return v, nil
		}
	}

	value, err := fn()
	if err != nil {
		var zero T
		return zero, err
	}

	opsCacheMu.Lock()
	// 顺手清理过期项，避免长期运行后 map 无限增长
	for k, e := range opsCache {
		if time.Now().After(e.expire) {
			delete(opsCache, k)
		}
	}
	opsCache[key] = opsCacheEntry{value: value, expire: time.Now().Add(opsCacheTTL)}
	opsCacheMu.Unlock()
	return value, nil
}

// 下面四个是具体入口：缓存 key 必须包含全部查询参数，
// 否则改了时间窗却拿到旧结果——这类 bug 不会报错，只会让人看到错的数据。

func CachedOpsSeries(ctx context.Context, from, to, bucket int64) ([]model.OpsSeriesPoint, error) {
	key := fmt.Sprintf("series:%d:%d:%d", from, to, bucket)
	return cachedOps(key, func() ([]model.OpsSeriesPoint, error) {
		return model.ScanOpsSeries(ctx, from, to, bucket)
	})
}

func CachedOpsBreakdown(ctx context.Context, from, to int64, dim string) ([]model.OpsBreakdownRow, error) {
	key := fmt.Sprintf("breakdown:%d:%d:%s", from, to, dim)
	return cachedOps(key, func() ([]model.OpsBreakdownRow, error) {
		return model.ScanOpsBreakdown(ctx, from, to, dim)
	})
}

func CachedErrorClusters(ctx context.Context, from, to int64, limit int) ([]model.ErrorCluster, error) {
	key := fmt.Sprintf("clusters:%d:%d:%d", from, to, limit)
	return cachedOps(key, func() ([]model.ErrorCluster, error) {
		return model.ScanErrorClusters(ctx, from, to, limit)
	})
}

func CachedChannelMargins(ctx context.Context, from, to int64) ([]model.ChannelMarginRow, error) {
	key := fmt.Sprintf("margins:%d:%d", from, to)
	return cachedOps(key, func() ([]model.ChannelMarginRow, error) {
		return model.ScanChannelMargins(ctx, from, to)
	})
}

// InvalidateOpsCache 清掉指定前缀的缓存。
//
// 写操作后必须调用：值班认领完却看到「未认领」，会以为自己没点成功，
// 然后重复点、收到 409、再怀疑系统坏了。
// 按前缀清而不是清空整表：改一条事项不该让图表缓存一起失效。
func InvalidateOpsCache(prefix string) {
	opsCacheMu.Lock()
	defer opsCacheMu.Unlock()
	for k := range opsCache {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(opsCache, k)
		}
	}
}

// CachedOpsIncidents 缓存事项列表。
//
// 前端 30 秒轮询一次，不加缓存等于每 30 秒占一个 MySQL 连接。
// 这张表只有几十行，但「几十行」不是理由——它不该出现在轮询路径上。
func CachedOpsIncidents(includeClosed bool, limit int) ([]model.OpsIncident, error) {
	flag := "0"
	if includeClosed {
		flag = "1"
	}
	key := fmt.Sprintf("incidents:%s:%d", flag, limit)
	return cachedOps(key, func() ([]model.OpsIncident, error) {
		return model.ListIncidents(includeClosed, limit)
	})
}

// CachedOpsShifts 缓存班次列表。前端 60 秒轮询。
func CachedOpsShifts(limit int) (*model.OpsShift, []model.OpsShift, error) {
	type bundle struct {
		current *model.OpsShift
		shifts  []model.OpsShift
	}
	key := fmt.Sprintf("shift:%d", limit)
	b, err := cachedOps(key, func() (bundle, error) {
		cur, err := model.CurrentShift()
		if err != nil {
			return bundle{}, err
		}
		shifts, err := model.ListShifts(limit)
		return bundle{current: cur, shifts: shifts}, err
	})
	return b.current, b.shifts, err
}
