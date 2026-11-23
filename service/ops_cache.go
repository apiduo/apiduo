package service

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/model"
	"golang.org/x/sync/singleflight"
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
	// opsCacheFlights 把同一 key 的并发未命中合并成一次查询。
	opsCacheFlights singleflight.Group
	// opsCacheGeneration 每次失效自增：填充期间发生过失效的结果不能再写回，
	// 否则「刚点完的写操作」会被一个在途的旧查询盖住，页面 30 秒看不到变化。
	opsCacheGeneration atomic.Uint64
)

const opsCacheTTL = 30 * time.Second

// cachedOps 命中返回缓存，否则执行 fn 并写入。
//
// 同一个 key 只让一个请求去查（singleflight）。这条以前是刻意不做的：那时
// 缓存的调用方是几十毫秒的分析查询，2 req/s 的限流已经够。增长看板接进来之后
// 前提变了——它的三个读数各是一次 90 天全表扫描，而写接口每次都会失效缓存，
// 多开页签/切页签正好落在失效后的窗口里，几个请求就会同时扫同一张表。
// 并发扫描把数据库打满，看板所有请求一起排队，现象是整页卡死。
//
// 失败不写缓存（否则一次抖动会让看板空白一个 TTL），等待者拿到同一个错误。
func cachedOps[T any](key string, fn func() (T, error)) (T, error) {
	if value, ok := readOpsCache[T](key); ok {
		return value, nil
	}

	value, err, _ := opsCacheFlights.Do(key, func() (any, error) {
		// 拿到飞行权之前可能已经有人写好缓存：省掉一次重复查询。
		if cached, ok := readOpsCache[T](key); ok {
			return cached, nil
		}
		generation := opsCacheGeneration.Load()
		result, err := fn()
		if err != nil {
			return nil, err
		}
		// 填充期间有人失效过：这份结果可能已经过时，交给下一次查询重算，
		// 但不能让写回把刚发生的变更盖掉。等待者仍拿到这次的结果。
		if opsCacheGeneration.Load() == generation {
			writeOpsCache(key, result)
		}
		return result, nil
	})
	if err != nil {
		var zero T
		return zero, err
	}
	typed, ok := value.(T)
	if !ok {
		var zero T
		return zero, fmt.Errorf("ops cache %s holds %T", key, value)
	}
	return typed, nil
}

// readOpsCache 只读命中判断；过期与类型不符都算未命中。
func readOpsCache[T any](key string) (T, bool) {
	opsCacheMu.RLock()
	entry, ok := opsCache[key]
	opsCacheMu.RUnlock()
	if !ok || !time.Now().Before(entry.expire) {
		var zero T
		return zero, false
	}
	value, typeOK := entry.value.(T)
	return value, typeOK
}

// writeOpsCache 写入并顺手清理过期项，避免长期运行后 map 无限增长。
func writeOpsCache[T any](key string, value T) {
	now := time.Now()
	opsCacheMu.Lock()
	defer opsCacheMu.Unlock()
	for k, e := range opsCache {
		if now.After(e.expire) {
			delete(opsCache, k)
		}
	}
	opsCache[key] = opsCacheEntry{value: value, expire: now.Add(opsCacheTTL)}
}

// 下面四个是具体入口：缓存 key 必须包含全部查询参数，
// 否则改了时间窗却拿到旧结果——这类 bug 不会报错，只会让人看到错的数据。

func CachedOpsSeries(ctx context.Context, from, to, bucket int64, modelFilter string) ([]model.OpsSeriesPoint, error) {
	key := fmt.Sprintf("series:%d:%d:%d:%s", from, to, bucket, modelFilter)
	return cachedOps(key, func() ([]model.OpsSeriesPoint, error) {
		return model.ScanOpsSeries(ctx, from, to, bucket, modelFilter)
	})
}

func CachedOpsBreakdown(ctx context.Context, from, to int64, dim, modelFilter string) ([]model.OpsBreakdownRow, error) {
	key := fmt.Sprintf("breakdown:%d:%d:%s:%s", from, to, dim, modelFilter)
	return cachedOps(key, func() ([]model.OpsBreakdownRow, error) {
		return model.ScanOpsBreakdown(ctx, from, to, dim, modelFilter)
	})
}

func CachedErrorClusters(ctx context.Context, from, to int64, limit int, modelFilter string) ([]model.ErrorCluster, error) {
	key := fmt.Sprintf("clusters:%d:%d:%d:%s", from, to, limit, modelFilter)
	return cachedOps(key, func() ([]model.ErrorCluster, error) {
		return model.ScanErrorClusters(ctx, from, to, limit, modelFilter)
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
	opsCacheGeneration.Add(1)
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
