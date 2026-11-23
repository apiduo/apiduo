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
