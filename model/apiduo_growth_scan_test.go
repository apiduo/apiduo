package model

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// setupGrowthScanDB 把一个内存库装到全局 DB 上，测试结束还原。
func setupGrowthScanDB(t testing.TB) *gorm.DB {
	t.Helper()
	previous := DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&QuotaData{}))
	DB = db
	t.Cleanup(func() {
		DB = previous
	})
	return db
}

// 四个窗口共用一次扫描，值必须和分开查完全一致。
// 7 天数据落在 14 天里、14 天落在 30 天里，这种嵌套关系是最容易写错的地方。
func TestScanQuotaRollupWindows(t *testing.T) {
	db := setupGrowthScanDB(t)

	now := time.Now().Unix()
	type point struct {
		daysAgo int
		quota   int
		count   int
		tokens  int
	}
	points := []point{
		{daysAgo: 1, quota: 100, count: 1, tokens: 10},
		{daysAgo: 10, quota: 200, count: 2, tokens: 20},
		{daysAgo: 20, quota: 300, count: 3, tokens: 30},
		{daysAgo: 60, quota: 400, count: 4, tokens: 40},
	}
	for _, p := range points {
		require.NoError(t, db.Create(&QuotaData{
			UserID:    42,
			Username:  "u42",
			ModelName: "m",
			CreatedAt: now - int64(p.daysAgo)*86400,
			Quota:     p.quota,
			Count:     p.count,
			TokenUsed: p.tokens,
		}).Error)
	}
	// 一个只落在 90 天窗口外的点，不应计入任何窗口。
	require.NoError(t, db.Create(&QuotaData{
		UserID: 42, Username: "u42", ModelName: "m",
		CreatedAt: now - 120*86400, Quota: 9999, Count: 99,
	}).Error)

	d3 := now - 2*86400
	d7 := now - 6*86400
	d14 := now - 13*86400
	d30 := now - 29*86400
	d90 := now - 89*86400

	got, err := ScanQuotaRollup(context.Background(), d3, d7, d14, d30, d90)
	require.NoError(t, err)
	row, ok := got[42]
	require.True(t, ok)

	require.Equal(t, 100, row.Quota3d)
	require.Equal(t, 100, row.Quota7d)
	require.Equal(t, 300, row.Quota14d)
	require.Equal(t, 600, row.Quota30d)
	require.Equal(t, 1000, row.Quota90d)
	require.Equal(t, 1, row.Requests7d)
	require.Equal(t, 6, row.Requests30d)
	require.Equal(t, 60, row.Tokens30d)
}

// 性能参考：一次扫描 5 万行（约等于 200 个用户 × 5 模型 × 24 小时 × 90 天）。
// SQLite 内存库比 MySQL 快，这里的数字是下界，用来确认复杂度是线性的。
func BenchmarkScanQuotaRollup(b *testing.B) {
	db := setupGrowthScanDB(b)

	now := time.Now().Unix()
	batch := make([]QuotaData, 0, 1000)
	for i := 0; i < 50000; i++ {
		batch = append(batch, QuotaData{
			UserID:    i%200 + 1,
			Username:  "u",
			ModelName: "m",
			CreatedAt: now - int64(i%2160)*3600,
			Quota:     10,
			Count:     1,
		})
		if len(batch) == 1000 {
			require.NoError(b, db.CreateInBatches(batch, 1000).Error)
			batch = batch[:0]
		}
	}

	d3 := now - 2*86400
	d7 := now - 6*86400
	d14 := now - 13*86400
	d30 := now - 29*86400
	d90 := now - 89*86400

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, err := ScanQuotaRollup(context.Background(), d3, d7, d14, d30, d90)
		if err != nil {
			b.Fatal(err)
		}
		if len(rows) != 200 {
			b.Fatalf("expected 200 users, got %d", len(rows))
		}
	}
	b.ReportMetric(50000, "rows")
	_ = fmt.Sprintf("%d", len(batch))
}

// T1 判据 1：按用户×模型聚合，主用模型取消耗最大者，数量取不同模型数。
func TestScanUserModels(t *testing.T) {
	setupGrowthScanDB(t)
	now := time.Now().Unix()
	d30 := now - 30*86400

	rows := []QuotaData{
		// 用户 1：gpt-4o 800 占主导，另有 claude 200
		{UserID: 1, Username: "u1", ModelName: "gpt-4o", CreatedAt: now - 2*86400, Quota: 800, Count: 1},
		{UserID: 1, Username: "u1", ModelName: "claude-3", CreatedAt: now - 3*86400, Quota: 200, Count: 1},
		// 用户 2：单一模型
		{UserID: 2, Username: "u2", ModelName: "gpt-4o", CreatedAt: now - 1*86400, Quota: 500, Count: 1},
		// 用户 3：窗口外的数据不应计入
		{UserID: 3, Username: "u3", ModelName: "gpt-4o", CreatedAt: now - 40*86400, Quota: 9999, Count: 1},
	}
	require.NoError(t, DB.Create(&rows).Error)

	got, err := ScanUserModels(context.Background(), d30)
	require.NoError(t, err)

	require.Equal(t, "gpt-4o", got[1].PrimaryModel)
	require.Equal(t, 2, got[1].ModelCount)
	require.Equal(t, 800, got[1].TopQuota)
	require.Equal(t, 1000, got[1].TotalQuota)

	require.Equal(t, "gpt-4o", got[2].PrimaryModel)
	require.Equal(t, 1, got[2].ModelCount)

	_, ok := got[3]
	require.False(t, ok, "窗口外的用户不应出现")
}

// T1 判据 3：全局模型占比要同时给出消耗与去重用户数。
func TestScanModelBreakdown(t *testing.T) {
	setupGrowthScanDB(t)
	now := time.Now().Unix()
	d30 := now - 30*86400

	rows := []QuotaData{
		{UserID: 1, Username: "u1", ModelName: "gpt-4o", CreatedAt: now - 86400, Quota: 600, Count: 3},
		{UserID: 2, Username: "u2", ModelName: "gpt-4o", CreatedAt: now - 86400, Quota: 400, Count: 2},
		{UserID: 3, Username: "u3", ModelName: "claude-3", CreatedAt: now - 86400, Quota: 900, Count: 5},
	}
	require.NoError(t, DB.Create(&rows).Error)

	got, err := ScanModelBreakdown(context.Background(), d30)
	require.NoError(t, err)
	require.Len(t, got, 2)

	byModel := map[string]ModelStat{}
	for _, m := range got {
		byModel[m.ModelName] = m
	}
	require.Equal(t, 1000, byModel["gpt-4o"].Quota)
	require.Equal(t, 2, byModel["gpt-4o"].Users)
	require.Equal(t, 5, byModel["gpt-4o"].Requests)
	require.Equal(t, 900, byModel["claude-3"].Quota)
	require.Equal(t, 1, byModel["claude-3"].Users)
	// 占比按消耗算，最大者在前
	// 两个模型的消耗必须不同，否则同值下排序不确定。
	require.Equal(t, "gpt-4o", got[0].ModelName)
}

// 第一批判据 1：失败数必须按请求去重。
// 分母是成功请求数，分子若用尝试数，重试会把它放大——一个重试 3 次才成功的请求，
// 会同时算进"1 次成功"和"2 次失败"，失败率立刻失真。
func TestScanFailuresDedup(t *testing.T) {
	db := setupGrowthScanDB(t)
	require.NoError(t, db.AutoMigrate(&Log{}))
	now := time.Now().Unix()
	prev := LOG_DB
	LOG_DB = db
	t.Cleanup(func() { LOG_DB = prev })

	logs := []Log{
		{UserId: 1, Type: LogTypeError, CreatedAt: now - 100, RequestId: "rq_a"},
		{UserId: 1, Type: LogTypeError, CreatedAt: now - 90, RequestId: "rq_a"}, // 同一请求重试
		{UserId: 1, Type: LogTypeError, CreatedAt: now - 80, RequestId: "rq_a"}, // 第三次
		{UserId: 1, Type: LogTypeError, CreatedAt: now - 70, RequestId: "rq_b"},
		{UserId: 2, Type: LogTypeError, CreatedAt: now - 60, RequestId: "rq_c"},
		{UserId: 2, Type: LogTypeError, CreatedAt: now - 50, RequestId: "rq_c"},
	}
	require.NoError(t, db.Create(&logs).Error)

	got, err := ScanFailures(context.Background(), now-86400, now-30*86400)
	require.NoError(t, err)
	require.Equal(t, 2, got[1].Failures7d, "用户1有 4 条尝试但只有 2 个不同请求")
	require.Equal(t, 1, got[2].Failures7d, "用户2有 2 条尝试但只有 1 个不同请求")
}

// 第四批判据 1：时间线要同时给出「多少请求、多少失败、慢到什么程度」。
// 只看错误率会漏掉最隐蔽的一类劣化：一条错误都不报，但整体变慢。
func TestScanOpsSeries(t *testing.T) {
	db := setupGrowthScanDB(t)
	require.NoError(t, db.AutoMigrate(&Log{}))
	prev := LOG_DB
	LOG_DB = db
	t.Cleanup(func() { LOG_DB = prev })

	const bucket = int64(600)
	base := int64(1000000) // 对齐到桶边界
	base = base / bucket * bucket

	logs := []Log{
		// 桶 1：2 条成功（1s、3s）+ 2 条失败（同一请求重试）
		{UserId: 1, Type: LogTypeConsume, CreatedAt: base + 10, UseTime: 1, RequestId: "ok1"},
		{UserId: 1, Type: LogTypeConsume, CreatedAt: base + 20, UseTime: 3, RequestId: "ok2"},
		{UserId: 1, Type: LogTypeError, CreatedAt: base + 30, RequestId: "bad1"},
		{UserId: 1, Type: LogTypeError, CreatedAt: base + 40, RequestId: "bad1"},
		// 桶 2：1 条成功（2s），无失败
		{UserId: 2, Type: LogTypeConsume, CreatedAt: base + bucket + 10, UseTime: 2, RequestId: "ok3"},
	}
	require.NoError(t, db.Create(&logs).Error)

	got, err := ScanOpsSeries(context.Background(), base, base+2*bucket, bucket)
	require.NoError(t, err)
	require.Len(t, got, 2, "两个桶各一行，空桶不补")

	require.Equal(t, base, got[0].Ts)
	require.Equal(t, 2, got[0].Requests)
	require.Equal(t, 1, got[0].Failures, "同一 request_id 重试两次只算一个失败请求")
	require.InDelta(t, 1.0/3.0, got[0].FailRate, 0.001, "失败占比 = 1/(2+1)")
	require.Equal(t, 1000.0, got[0].P50ms, "两条成功请求的中位是 1s")
	require.Equal(t, 3000.0, got[0].P95ms, "P95 取最慢那条")

	require.Equal(t, 1, got[1].Requests)
	require.Equal(t, 0, got[1].Failures)
	require.Equal(t, 0.0, got[1].FailRate)
}

// 第四批判据 2：下钻维度必须同时给出量、失败与延迟，
// 否则「哪个渠道最差」无法回答——慢的渠道失败率可能很低。
func TestScanOpsBreakdown(t *testing.T) {
	db := setupGrowthScanDB(t)
	require.NoError(t, db.AutoMigrate(&Log{}))
	prev := LOG_DB
	LOG_DB = db
	t.Cleanup(func() { LOG_DB = prev })

	from, to := int64(1000), int64(2000)
	logs := []Log{
		{Type: LogTypeConsume, CreatedAt: 1500, UseTime: 1, ModelName: "gpt-4o", ChannelId: 1, RequestId: "a"},
		{Type: LogTypeConsume, CreatedAt: 1501, UseTime: 9, ModelName: "gpt-4o", ChannelId: 1, RequestId: "b"},
		{Type: LogTypeError, CreatedAt: 1502, ModelName: "gpt-4o", ChannelId: 1, RequestId: "c"},
		{Type: LogTypeConsume, CreatedAt: 1503, UseTime: 2, ModelName: "claude-3", ChannelId: 2, RequestId: "d"},
	}
	require.NoError(t, db.Create(&logs).Error)

	byModel, err := ScanOpsBreakdown(context.Background(), from, to, "model")
	require.NoError(t, err)
	require.Len(t, byModel, 2)
	require.Equal(t, "gpt-4o", byModel[0].Key, "按请求量倒序")
	require.Equal(t, 2, byModel[0].Requests)
	require.Equal(t, 1, byModel[0].Failures)
	require.Equal(t, 9000.0, byModel[0].P95ms, "慢的那个模型要能看出慢")

	byChannel, err := ScanOpsBreakdown(context.Background(), from, to, "channel")
	require.NoError(t, err)
	require.Len(t, byChannel, 2)
	keys := map[string]bool{}
	for _, r := range byChannel {
		keys[r.Key] = true
	}
	require.True(t, keys["1"] && keys["2"], "渠道维度用 channel_id 作 key")

	// 未知维度必须报错，不能静默返回全量——
	// 那会让调用方以为筛选生效了。
	_, err = ScanOpsBreakdown(context.Background(), from, to, "eval")
	require.Error(t, err)
}

// 第五批判据 1：变更事件要能按时间窗取回，并且按发生时间排序。
// 排障时看的是「错误开始前 6 分钟改过什么」，顺序错了就白看。
func TestChangeEventsWindow(t *testing.T) {
	db := setupGrowthScanDB(t)
	require.NoError(t, db.AutoMigrate(&ChangeEvent{}))

	evs := []ChangeEvent{
		{Kind: "channel_disable", Title: "渠道 #1 自动禁用", OccurredAt: 300, Actor: "system"},
		{Kind: "deploy", Title: "发布 v1.2.3", OccurredAt: 100, Actor: "root"},
		{Kind: "channel_enable", Title: "渠道 #1 恢复", OccurredAt: 500, Actor: "root"},
	}
	require.NoError(t, db.Create(&evs).Error)

	got, err := ListChangeEvents(150, 550, 50)
	require.NoError(t, err)
	require.Len(t, got, 2, "窗口外的 deploy(100) 应被排除")
	require.Equal(t, int64(300), got[0].OccurredAt, "按发生时间正序")
	require.Equal(t, int64(500), got[1].OccurredAt)

	// 默认条数上限要生效：排障看最近几条就够，
	// 一次拉几百条会把时间线压成一坨。
	all, err := ListChangeEvents(0, 1000, 2)
	require.NoError(t, err)
	require.Len(t, all, 2)
}

// 错误聚类判据 1：同一条错误的多次出现必须归到同一个指纹。
// 日志里的消息带 request id、余额数字、渠道名，直接按原文分组
// 会把「同一个根因」拆成几百个只出现一次的簇。
func TestNormalizeErrorMessage(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"request id 不同不算不同错误",
			"Insufficient user quota, remaining quota: $0.00 (request id: 202611160432089329755078268d9d642igetZm)",
			"insufficient user quota, remaining quota: $ (request id: )",
		},
		{
			"余额数字归一",
			"Insufficient user quota, remaining quota: $12.50",
			"insufficient user quota, remaining quota: $",
		},
		{
			"大小写归一",
			"Model Not Found",
			"model not found",
		},
		{
			"空白归一",
			"upstream   timeout",
			"upstream timeout",
		},
		{
			"端口号归一（同一上游不同端口是同一根因）",
			"dial tcp 10.0.0.5:8443 failed",
			"dial tcp ..: failed",
		},
	}
	for _, c := range cases {
		if got := normalizeErrorMessage(c.in); got != c.want {
			t.Errorf("%s:\n  got  %q\n  want %q", c.name, got, c.want)
		}
	}
}

// 错误聚类判据 2：聚类要按去重后的请求数统计，
// 并且给出影响面（多少个用户、多少个渠道）。
func TestScanErrorClusters(t *testing.T) {
	db := setupGrowthScanDB(t)
	require.NoError(t, db.AutoMigrate(&Log{}))
	prev := LOG_DB
	LOG_DB = db
	t.Cleanup(func() { LOG_DB = prev })

	base := int64(1000)
	logs := []Log{
		// 同一根因（配额不足），3 个请求，2 个用户，2 个渠道，其中一个请求重试两次
		{UserId: 1, Type: LogTypeError, CreatedAt: base, ChannelId: 1, RequestId: "a", Content: "Insufficient user quota, remaining quota: $0.00 (request id: x)"},
		{UserId: 1, Type: LogTypeError, CreatedAt: base + 1, ChannelId: 1, RequestId: "a", Content: "Insufficient user quota, remaining quota: $0.00 (request id: y)"},
		{UserId: 2, Type: LogTypeError, CreatedAt: base + 2, ChannelId: 2, RequestId: "b", Content: "Insufficient user quota, remaining quota: $0.00 (request id: z)"},
		// 另一根因
		{UserId: 3, Type: LogTypeError, CreatedAt: base + 3, ChannelId: 3, RequestId: "c", Content: "model not found"},
	}
	require.NoError(t, db.Create(&logs).Error)

	got, err := ScanErrorClusters(context.Background(), base-10, base+100, 20)
	require.NoError(t, err)
	require.Len(t, got, 2, "两个根因两个簇")

	byKey := map[string]ErrorCluster{}
	for _, c := range got {
		byKey[c.Fingerprint] = c
	}
	quotaKey := normalizeErrorMessage(logs[0].Content)
	q := byKey[quotaKey]
	require.Equal(t, 2, q.Requests, "同一 request_id 重试两次只算一个")
	require.Equal(t, 2, q.Users)
	require.Equal(t, 2, q.Channels)
	require.Equal(t, int64(base), q.FirstSeen)
	require.Equal(t, int64(base+2), q.LastSeen)
}

// 延迟精度判据：有毫秒值时不得退化成秒。
// use_time 是 int 秒，P95 落在 1s 时看不出 1.2s 与 1.9s 的差别，
// 而这恰恰是「要不要扩容」的关键区间。
func TestOpsSeriesPrefersMilliseconds(t *testing.T) {
	db := setupGrowthScanDB(t)
	require.NoError(t, db.AutoMigrate(&Log{}))
	prev := LOG_DB
	LOG_DB = db
	t.Cleanup(func() { LOG_DB = prev })

	base := int64(1000000) / 600 * 600
	// 两个请求的秒值相同、毫秒值不同。秒级聚合会把它们都算成 1000ms，
	// 这正是要修的问题——P95 落在 1s 附近时看不出 1.2s 与 1.9s。
	logs := []Log{
		{Type: LogTypeConsume, CreatedAt: base + 1, UseTime: 1, UseTimeMs: 1250, RequestId: "a"},
		{Type: LogTypeConsume, CreatedAt: base + 2, UseTime: 1, UseTimeMs: 1890, RequestId: "b"},
	}
	require.NoError(t, db.Create(&logs).Error)

	got, err := ScanOpsSeries(context.Background(), base, base+600, 600)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, 1250.0, got[0].P50ms, "中位应取毫秒值")
	require.Equal(t, 1890.0, got[0].P95ms, "P95 应取毫秒值，而不是都算成 1000")
}

// 可读性判据：数字带单位时保留单位形状。
// 直接抹数字会剩一个孤零零的 "s"，值班读起来像脏数据。
func TestNormalizeKeepsUnitShape(t *testing.T) {
	got := normalizeErrorMessage("upstream timeout after 30s")
	require.Equal(t, "upstream timeout after Ns", got)

	same := normalizeErrorMessage("upstream timeout after 45.5s")
	require.Equal(t, got, same, "30s 与 45.5s 必须归一到同一形态")

	ms := normalizeErrorMessage("read timeout 1500ms")
	require.Equal(t, "read timeout Nms", ms)
}

// 异步写判据：RecordChange 必须立即返回，不等待落库。
// 调用点在 relay 的错误分支上，那里多等一毫秒都可能连锁失败。
func TestRecordChangeIsNonBlocking(t *testing.T) {
	db := setupGrowthScanDB(t)
	require.NoError(t, db.AutoMigrate(&ChangeEvent{}))
	prev := DB
	DB = db
	t.Cleanup(func() { DB = prev })

	done := make(chan struct{})
	go func() {
		for i := range 100 {
			RecordChange(ChangeEvent{Kind: "test", Title: "x", OccurredAt: int64(1000 + i)})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RecordChange 阻塞超过 2 秒")
	}

	// 后台最终会写进去（异步，给一点时间）
	require.Eventually(t, func() bool {
		var n int64
		DB.Model(&ChangeEvent{}).Count(&n)
		return n > 0
	}, 3*time.Second, 50*time.Millisecond, "异步写入应最终落库")
}

// 归一化判据：HTTP 状态码是分类信息，不能被当普通数字抹掉。
// 线上实测：status_code=502 与 503 被合并成同一条
// "status_code=, upstream error, please retry later"，两个不同根因合成一簇。
func TestNormalizeKeepsHTTPStatus(t *testing.T) {
	a := normalizeErrorMessage("status_code=502, upstream error, please retry later")
	b := normalizeErrorMessage("status_code=503, upstream error, please retry later")

	// 保留错误类别（4xx/5xx）而不是具体码：
	// 502 与 503 通常是同一根因（上游挂了，随机返回），合成一簇是对的；
	// 但 4xx（我们的参数错）与 5xx（上游的错）必须分开——这两类
	// 要分别找不同的人处理。
	require.Equal(t, "status_code=SERVERERROR, upstream error, please retry later", a)
	require.Equal(t, a, b, "502 与 503 同属 5xx，应合并")

	// 4xx 与 5xx 必须区分
	e := normalizeErrorMessage("status_code=404, model not found")
	f := normalizeErrorMessage("status_code=502, model not found")
	require.NotEqual(t, e, f, "404 与 502 是两个不同类别")

	// 样本里同时出现 502 与 503 时，归一后也应当区分
	c := normalizeErrorMessage("status_code=500, upstream error")
	d := normalizeErrorMessage("status_code=404, upstream error")
	require.NotEqual(t, c, d, "500 与 404 不同类")

	// 不带状态码的错误不受影响
	require.Equal(t, "upstream error, please retry later",
		normalizeErrorMessage("upstream error, please retry later"))

	// 依赖关系：状态码保护要发生在数字抹除之前。
	// 如果顺序反了，会先变成 "status_code=, ..."，保护规则再也匹配不到。
	require.NotContains(t, a, "502", "具体码被类别替代")
}

// 错误码判据：归一化把 502 抹成 SERVERERROR 之后，具体码不能一起消失。
// 运维要看的是"502 还是 503"，只知道"上游 5xx"没法判断是网关故障还是过载。
func TestErrorClusterKeepsStatusCode(t *testing.T) {
	db := setupGrowthScanDB(t)
	require.NoError(t, db.AutoMigrate(&Log{}))
	prev := LOG_DB
	LOG_DB = db
	t.Cleanup(func() { LOG_DB = prev })

	base := int64(1_800_000_000)
	rows := []Log{
		{CreatedAt: base, Type: 5, Content: "status_code=502, upstream error", RequestId: "a1", UserId: 1, ChannelId: 1},
		{CreatedAt: base + 1, Type: 5, Content: "status_code=502, upstream error", RequestId: "a2", UserId: 1, ChannelId: 1},
		{CreatedAt: base + 2, Type: 5, Content: "status_code=503, upstream error", RequestId: "a3", UserId: 2, ChannelId: 1},
		{CreatedAt: base + 3, Type: 5, Content: "status_code=429, rate limited", RequestId: "a4", UserId: 2, ChannelId: 2},
	}
	require.NoError(t, db.Create(&rows).Error)

	clusters, err := ScanErrorClusters(context.Background(), base-60, base+60, 10)
	require.NoError(t, err)
	require.NotEmpty(t, clusters)

	byFingerprint := map[string]ErrorCluster{}
	for _, c := range clusters {
		byFingerprint[c.Fingerprint] = c
	}

	// 502 与 503 归一后同属一簇（都是 SERVERERROR），但簇内要能分出具体码
	upstream := byFingerprint["status_code=SERVERERROR, upstream error"]
	require.Equal(t, 3, upstream.Requests, "502×2 + 503×1 应合成一簇")
	codes := map[string]int{}
	for _, sc := range upstream.StatusCodes {
		codes[sc.Code] = sc.Count
	}
	require.Equal(t, 2, codes["502"], "簇内应保留 502 的计数")
	require.Equal(t, 1, codes["503"], "簇内应保留 503 的计数")

	// 另一个类别单独成簇
	limited, ok := byFingerprint["status_code=CLIENTERROR, rate limited"]
	require.True(t, ok, "429 应落在 CLIENTERROR 簇")
	require.Len(t, limited.StatusCodes, 1)
	require.Equal(t, "429", limited.StatusCodes[0].Code)
}
