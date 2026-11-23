package service

import (
	"sync/atomic"
	"testing"
)

// 重算互斥判据：第二次点击不能并发起第二个全量扫描。
//
// 没有互斥时，运营连点几下就会把「扫 90 天配额 + 扫日志」叠起来跑，
// 数据库被占满，看板所有请求跟着排队——线上表现就是整页卡死。
func TestTryStartGrowthRefreshIsExclusive(t *testing.T) {
	FinishGrowthRefresh()
	t.Cleanup(FinishGrowthRefresh)

	if !TryStartGrowthRefresh() {
		t.Fatal("第一次抢占应成功")
	}
	if !GrowthRefreshRunning() {
		t.Fatal("抢占后应处于运行中")
	}
	if TryStartGrowthRefresh() {
		t.Fatal("已有重算在跑时，第二次抢占必须失败")
	}

	FinishGrowthRefresh()
	if GrowthRefreshRunning() {
		t.Fatal("释放后不应还在运行中")
	}
	if !TryStartGrowthRefresh() {
		t.Fatal("释放后应能重新抢占")
	}
}

// 失效判据：写接口清的是增长读数，不能顺手把服务质量看板的缓存一起清掉。
//
// 两个看板共用同一个缓存池（cachedOps）。前缀写错不会报错，
// 只会让 ops 的图表每改一次分层就重扫一遍日志。
//
// 顺序很关键：先失效，再各读一次。增长读数必须重新算（calls=2），
// 服务质量缓存必须仍然命中（calls=1）。
func TestInvalidateGrowthCacheOnlyClearsGrowthReadings(t *testing.T) {
	var growthCalls, opsCalls int32
	growthFn := func() (int, error) {
		return int(atomic.AddInt32(&growthCalls, 1)), nil
	}
	opsFn := func() (int, error) {
		return int(atomic.AddInt32(&opsCalls, 1)), nil
	}

	if _, err := cachedOps("growth:overview", growthFn); err != nil {
		t.Fatal(err)
	}
	if _, err := cachedOps("series:1:2:3:", opsFn); err != nil {
		t.Fatal(err)
	}

	InvalidateGrowthCache()

	if _, err := cachedOps("growth:overview", growthFn); err != nil {
		t.Fatal(err)
	}
	if _, err := cachedOps("series:1:2:3:", opsFn); err != nil {
		t.Fatal(err)
	}

	if got := atomic.LoadInt32(&growthCalls); got != 2 {
		t.Errorf("增长读数应被失效并重算，期望 2 次，实际 %d", got)
	}
	if got := atomic.LoadInt32(&opsCalls); got != 1 {
		t.Errorf("服务质量缓存不应被增长失效影响，期望 1 次，实际 %d", got)
	}
}
