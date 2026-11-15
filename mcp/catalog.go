package mcp

import (
	"fmt"
	"hash/fnv"
	"sync"
	"time"
)

// catalogTTL 是快照的兜底寿命。
//
// 主失效手段是写路径上的 invalidateCatalog：TTL 只负责兜住「别的副本改了库」
// 这一种情况。1 秒足够短到不产生可感知的陈旧，又足够长到把突发读合并成一次查询。
const catalogTTL = time.Second

// catalog 是一次能力快照：视图 + 版本号。
//
// 版本由每行的 version（行级乐观锁）哈希而来 —— 它覆盖新增、删除、启停与
// 任何字段修改：任何一次写都会让该行 version +1，哈希随之变化。
// 不要用 max(version)：删掉一行时它不变，缓存会永久陈旧。
type catalog struct {
	views   []modelView
	version string
}

var (
	catalogMu    sync.RWMutex
	catalogAt    time.Time
	catalogCache catalog
)

func loadCatalog() (catalog, error) {
	catalogMu.RLock()
	if !catalogAt.IsZero() && time.Since(catalogAt) < catalogTTL {
		c := catalogCache
		catalogMu.RUnlock()
		return c, nil
	}
	catalogMu.RUnlock()

	views, err := listEnabledViews()
	if err != nil {
		return catalog{}, err
	}
	c := catalog{views: views, version: catalogVersionOf(views)}

	catalogMu.Lock()
	catalogAt, catalogCache = time.Now(), c
	catalogMu.Unlock()
	return c, nil
}

// invalidateCatalog 让下一次读重新查库。
//
// 写路径显式调用，而不是等 TTL：管理员在面板上改完声明、紧接着看 models.list
// 发现还是旧的，会以为是保存失败。
func invalidateCatalog() {
	catalogMu.Lock()
	catalogAt = time.Time{}
	catalogMu.Unlock()
}

// resetCatalogCache 清空快照。测试用：每个测试是新库。
func resetCatalogCache() {
	catalogMu.Lock()
	catalogAt = time.Time{}
	catalogCache = catalog{}
	catalogMu.Unlock()
}

// catalogVersionOf 对「模型 ID + 行版本」做稳定哈希。
//
// views 的顺序由 listEnabledViews 固定（kind → priority desc → id），
// 所以同样的库内容在不同副本上算出同一个版本号。
func catalogVersionOf(views []modelView) string {
	h := fnv.New64a()
	for _, v := range views {
		fmt.Fprintf(h, "%s:%d;", v.ID, v.Version)
	}
	return fmt.Sprintf("%016x", h.Sum64())
}
