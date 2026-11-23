package service

import (
	"errors"
	"sync"
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

// 缓存判据 5：同一 key 的并发未命中只查一次。
//
// 增长看板的三个读数各是一次 90 天全表扫描，而写接口每次都会失效缓存：
// 没有合并时，失效窗口内多开几个页签就是几次并发全表扫描，数据库被打满、
// 看板一起排队。这里用「fn 阻塞到所有 goroutine 都就位」来逼出并发。
func TestCachedOpsCoalescesConcurrentMisses(t *testing.T) {
	var calls int32
	release := make(chan struct{})
	fn := func() (int, error) {
		atomic.AddInt32(&calls, 1)
		<-release
		return 42, nil
	}

	const workers = 8
	var wg sync.WaitGroup
	results := make([]int, workers)
	wg.Add(workers)
	for i := range workers {
		go func() {
			defer wg.Done()
			results[i], _ = cachedOps("test-coalesce", fn)
		}()
	}
	// 给 goroutine 时间都走到 Do；第一个在 fn 里等 release，其余挂在飞行上。
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("并发未命中应只查一次，实际 %d 次", got)
	}
	for i, got := range results {
		if got != 42 {
			t.Fatalf("等待者 %d 拿到 %d，期望 42", i, got)
		}
	}
}

// 缓存判据 6：飞行失败时等待者拿到同一个错误，且不写缓存。
func TestCachedOpsSharesErrorWithoutCaching(t *testing.T) {
	var calls int32
	release := make(chan struct{})
	fn := func() (int, error) {
		atomic.AddInt32(&calls, 1)
		<-release
		return 0, errors.New("boom")
	}

	var wg sync.WaitGroup
	errs := make([]error, 4)
	wg.Add(len(errs))
	for i := range errs {
		go func() {
			defer wg.Done()
			_, errs[i] = cachedOps("test-coalesce-error", fn)
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("并发未命中应只查一次，实际 %d 次", got)
	}
	for i, err := range errs {
		if err == nil {
			t.Fatalf("等待者 %d 应拿到错误", i)
		}
	}
	// 下一次调用必须重新查：错误不缓存。
	if _, err := cachedOps("test-coalesce-error", func() (int, error) { return 7, nil }); err != nil {
		t.Fatalf("错误之后应能重新查询: %v", err)
	}
}

// 缓存判据 7：填充期间发生失效，旧结果不写回。
//
// 值班刚认领完预警、运营刚改完分层，页面必须立刻反映新状态；一个在途的旧查询
// 如果在这之后写回，会把新状态盖住整整一个 TTL，用户只会以为没点上。
func TestCachedOpsDoesNotStoreFillRacedByInvalidation(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	var calls int32
	fn := func() (int, error) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			close(entered)
			<-release
		}
		return int(n), nil
	}

	go func() {
		defer close(done)
		_, _ = cachedOps("test-race-invalidate", fn)
	}()
	<-entered
	InvalidateOpsCache("test-race")
	close(release)
	<-done

	got, err := cachedOps("test-race-invalidate", fn)
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Fatalf("失效后的第一次读取应重算（期望 2），实际 %d", got)
	}
}
