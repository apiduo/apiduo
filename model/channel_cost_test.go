package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

// 「贵」判据 1：成本按「美元/百万 token」算，收入按 quota 换算。
func TestComputeCost(t *testing.T) {
	prev := common.QuotaPerUnit
	common.QuotaPerUnit = 500000
	t.Cleanup(func() { common.QuotaPerUnit = prev })

	cost := &ChannelCost{InputPerMillion: 3, OutputPerMillion: 15}

	// 100 万 input + 100 万 output = $3 + $15 = $18
	got := ComputeCost(1_000_000, 1_000_000, 9_000_000, cost)
	require.InDelta(t, 18.0, got.CostUSD, 0.001)
	require.InDelta(t, 18.0, got.RevenueUSD, 0.001, "9000000/500000 = $18")
	require.InDelta(t, 0.0, got.MarginUSD, 0.001)

	// 倒挂：成本 $18，收入 $9
	loss := ComputeCost(1_000_000, 1_000_000, 4_500_000, cost)
	require.InDelta(t, -9.0, loss.MarginUSD, 0.001)
	require.InDelta(t, -100.0, loss.MarginPct, 0.001)

	// 没有单价配置时不臆造成本：成本记 0，毛利等于全部收入。
	// 这比「按 0 元成本算成 100% 毛利」更诚实——它说明的是「不知道」。
	unknown := ComputeCost(1000, 1000, 500_000, nil)
	require.Equal(t, 0.0, unknown.CostUSD)

	// 收入为 0 时不能除零
	zero := ComputeCost(0, 0, 0, cost)
	require.Equal(t, 0.0, zero.MarginPct)
}

// 「贵」判据 2：单价配置按渠道 upsert，重复写入不产生重复行。
func TestUpsertChannelCost(t *testing.T) {
	setupGrowthScanDB(t)
	require.NoError(t, DB.AutoMigrate(&ChannelCost{}))

	require.NoError(t, UpsertChannelCost(ChannelCost{
		ChannelId: 7, InputPerMillion: 1, OutputPerMillion: 2,
	}))
	require.NoError(t, UpsertChannelCost(ChannelCost{
		ChannelId: 7, InputPerMillion: 3, OutputPerMillion: 6, Note: "调价",
	}))

	list, err := ListChannelCosts()
	require.NoError(t, err)
	require.Len(t, list, 1, "同一渠道只能有一行")
	require.Equal(t, 3.0, list[0].InputPerMillion)
	require.Equal(t, "调价", list[0].Note)

	require.NoError(t, DeleteChannelCost(7))
	list, err = ListChannelCosts()
	require.NoError(t, err)
	require.Len(t, list, 0)
}

// 缓存判据：写操作后立即可见，不需要等 TTL。
// 这条比"缓存有效"更重要——运维改完价看不见会以为没生效。
func TestChannelCostCacheRefreshesOnWrite(t *testing.T) {
	setupGrowthScanDB(t)
	require.NoError(t, DB.AutoMigrate(&ChannelCost{}))
	require.NoError(t, LoadChannelCosts())

	require.Empty(t, CachedChannelCosts())

	require.NoError(t, UpsertChannelCost(ChannelCost{ChannelId: 9, InputPerMillion: 7}))
	got := CachedChannelCosts()
	require.NotNil(t, got[9], "写入后缓存应立即可见")
	require.Equal(t, 7.0, got[9].InputPerMillion)

	require.NoError(t, DeleteChannelCost(9))
	require.Nil(t, CachedChannelCosts()[9], "删除后缓存应立即消失")
}
