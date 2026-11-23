package model

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// billingAggregateContext 只对 ClickHouse 生效：其他方言没有这几个 parameter，
// 下发过去会被当成未知标识符。这一条守住「不把 ClickHouse 专属设置泄漏给别的库」。
func TestBillingAggregateContextLeavesOtherDialectsAlone(t *testing.T) {
	original := common.LogDatabaseType()
	t.Cleanup(func() { common.SetLogDatabaseType(original) })

	for _, dialect := range []common.DatabaseType{
		common.DatabaseTypeSQLite,
		common.DatabaseTypeMySQL,
		common.DatabaseTypePostgreSQL,
	} {
		common.SetLogDatabaseType(dialect)
		ctx := billingAggregateContext(context.Background())
		assert.Equal(t, context.Background(), ctx, "dialect %s 不应改写 context", dialect)
	}
}

// 设置必须真的到服务端，而不是只挂在 Go 的 context 上。
//
// 本地 ClickHouse 默认 max_memory_usage=0（不限），所以「查询能跑通」不能证明
// 设置生效——不限内存当然跑通。这里反过来验：经过 billingAggregateContext 的
// 连接读到的是我们给的值，且把预算压到 1 字节时查询立刻失败。两者同时成立，
// 才能说明这组参数确实随查询下发。
//
// 需要一个真实 ClickHouse：PROBE_CH_DSN，未设置则跳过。
func TestBillingAggregateContextReachesClickHouse(t *testing.T) {
	dsn := os.Getenv("PROBE_CH_DSN")
	if dsn == "" {
		t.Skip("PROBE_CH_DSN not set")
	}
	options, err := clickhouse.ParseDSN(dsn)
	require.NoError(t, err)
	conn, err := clickhouse.Open(options)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	original := common.LogDatabaseType()
	t.Cleanup(func() { common.SetLogDatabaseType(original) })
	common.SetLogDatabaseType(common.DatabaseTypeClickHouse)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tuned := billingAggregateContext(ctx)

	var memory, spill, threads string
	require.NoError(t, conn.QueryRow(tuned,
		"SELECT toString(getSetting('max_memory_usage')), "+
			"toString(getSetting('max_bytes_before_external_group_by')), "+
			"toString(getSetting('max_threads'))",
	).Scan(&memory, &spill, &threads))
	assert.Equal(t, fmt.Sprint(billingAggregateMemoryUsage), memory)
	assert.Equal(t, fmt.Sprint(billingAggregateSpillBytes), spill)
	assert.Equal(t, fmt.Sprint(billingAggregateThreads), threads)

	// 1 字节预算：设置没下发就会正常返回，下发了就必须报内存超限。
	tiny := clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"max_memory_usage": int64(1),
	}))
	var rows uint64
	err = conn.QueryRow(tiny, "SELECT count() FROM numbers(1000000)").Scan(&rows)
	require.Error(t, err, "max_memory_usage=1 应当让查询失败")
	assert.Contains(t, err.Error(), "memory limit")
}

// 并发闸门必须有确定上限，且排队时能被 ctx 打断。
//
// 单条聚合实测 400~600MB，服务端总预算 4G、基线已占 1.3G：不限并发就会重演
// 「聚合吃满预算 → 无关查询被拒成 241」。同时要保证一条卡住的查询不会把
// 后面所有账单请求永远堵死 —— 靠 ctx 的 deadline 兜底。
func TestBeginBillingAggregateBoundsConcurrency(t *testing.T) {
	require.Equal(t, 0, len(billingAggregateSlots), "前置条件：测试开始时名额必须全空")

	var held []func()
	releaseHeld := func() {
		for _, done := range held {
			done()
		}
		held = nil
	}
	t.Cleanup(releaseHeld)

	for i := range cap(billingAggregateSlots) {
		aggCtx, done, err := beginBillingAggregate(context.Background())
		require.NoError(t, err)
		held = append(held, done)
		// 每条查询都带 deadline，卡住时能自己退出。
		_, hasDeadline := aggCtx.Deadline()
		assert.True(t, hasDeadline, "第 %d 个名额没有 deadline", i)
	}

	// 名额用尽后再申请：ctx 一到期就必须返回错误，而不是无限等下去。
	waited, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_, _, err := beginBillingAggregate(waited)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	// 释放一个之后立刻可以再拿到，说明名额是循环使用的。
	held[0]()
	held = held[1:]
	_, done, err := beginBillingAggregate(context.Background())
	require.NoError(t, err)
	done()

	releaseHeld()
	require.Equal(t, 0, len(billingAggregateSlots), "清理后名额必须全部归还")
}

// 241 只重试一次，别的错误不重试。
//
// 服务端内存打满是瞬时的（失败那一秒 RSS 顶在上限，几十秒后自己回落），
// 重试一次能把「加载失败」变成「慢一秒」；但重试必须有界，否则压力大时
// 重试本身会变成新一轮压力。语法错误、字段错误这类不该被重试掩盖。
func TestRunBillingAggregateRetriesOnlyOnMemoryLimit(t *testing.T) {
	t.Run("241 一次后成功", func(t *testing.T) {
		attempts := 0
		err := runBillingAggregate(context.Background(), func(context.Context) error {
			attempts++
			if attempts == 1 {
				return &clickhouse.Exception{Code: 241}
			}
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, 2, attempts)
	})

	t.Run("非 241 不重试", func(t *testing.T) {
		attempts := 0
		err := runBillingAggregate(context.Background(), func(context.Context) error {
			attempts++
			return &clickhouse.Exception{Code: 62, Message: "syntax error"}
		})
		require.Error(t, err)
		assert.Equal(t, 1, attempts, "语法错误重试没有意义，只会掩盖问题")
	})

	t.Run("一直 241 也只试两次", func(t *testing.T) {
		attempts := 0
		err := runBillingAggregate(context.Background(), func(context.Context) error {
			attempts++
			return &clickhouse.Exception{Code: 241}
		})
		require.Error(t, err)
		assert.Equal(t, 2, attempts)
	})

	require.Equal(t, 0, len(billingAggregateSlots), "重试等待期间必须已释放名额")
}
