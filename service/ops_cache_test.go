package service

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// 缓存判据 1：TTL 内第二次调用不执行 fn。
func TestCachedOpsHitsWithinTTL(t *testing.T) {
	var calls int32
	fn := func() ([]int, error) {
		atomic.AddInt32(&calls, 1)
		return []int{int(atomic.LoadInt32(&calls))}, nil
	}

	key := "test-hit"
	first, err := cachedOps(key, fn)
	if err != nil || len(first) != 1 {
		t.Fatalf("first: %v %v", first, err)
	}
	second, err := cachedOps(key, fn)
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("TTL 内应只有一次实际查询，实际 %d", calls)
	}
	if second[0] != first[0] {
		t.Error("缓存值不一致")
	}
}

// 缓存判据 2：不同 key 不能互相命中。
// 这个 bug 不会报错，只会让人看到错的数据——所以必须测。
func TestCachedOpsKeyIsolation(t *testing.T) {
	var calls int32
	fn := func() (string, error) {
		atomic.AddInt32(&calls, 1)
		return "v", nil
	}
	_, _ = cachedOps("key-a", fn)
	_, _ = cachedOps("key-b", fn)
	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("不同 key 应各查一次，实际 %d", calls)
	}
}

// 缓存判据 3：失败结果不缓存，否则一次抖动会让看板 30 秒空白。
func TestCachedOpsDoesNotCacheError(t *testing.T) {
	var calls int32
	fn := func() (int, error) {
		atomic.AddInt32(&calls, 1)
		return 0, errors.New("boom")
	}
	for range 2 {
		if _, err := cachedOps("test-err", fn); err == nil {
			t.Fatal("应返回错误")
		}
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("错误不应被缓存，期望 2 次调用，实际 %d", calls)
	}
}

// 缓存判据 4：过期后重新查询。
func TestCachedOpsExpires(t *testing.T) {
	var calls int32
	fn := func() (int, error) {
		return int(atomic.AddInt32(&calls, 1)), nil
	}
	_, _ = cachedOps("test-expire", fn)

	// 直接把过期时间拨到过去，避免测试真的等 30 秒
	opsCacheMu.Lock()
	e := opsCache["test-expire"]
	e.expire = time.Now().Add(-time.Second)
	opsCache["test-expire"] = e
	opsCacheMu.Unlock()

	got, _ := cachedOps("test-expire", fn)
	if got != 2 {
		t.Errorf("过期后应重新查询，期望 2，实际 %d", got)
	}
}

// 写时失效判据：认领之后立刻查列表，必须看到新状态。
//
// 这一条比「缓存有效」更重要——值班刚认领完却看到「未认领」，
// 会以为自己没点成功，然后重复点，最后收到 409 而困惑。
func TestInvalidateOpsCacheByPrefix(t *testing.T) {
	var incidentsCalls int32
	incidentsFn := func() (string, error) {
		atomic.AddInt32(&incidentsCalls, 1)
		return "list", nil
	}

	key := "incidents:0:0:50"
	_, _ = cachedOps(key, incidentsFn)
	_, _ = cachedOps("margins:1:2", func() (string, error) { return "m", nil })

	InvalidateOpsCache("incidents:")

	_, _ = cachedOps(key, incidentsFn)
	if atomic.LoadInt32(&incidentsCalls) != 2 {
		t.Errorf("失效后应重新查询，实际调用 %d 次", incidentsCalls)
	}
}
