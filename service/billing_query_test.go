package service

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 当月没有出账快照时，管理员的全站视图要能实时回答「这个月谁用得多」，
// 并支持按用户名/用户 ID 找人。
func TestLoadBillingUserSummariesFallsBackToLiveAggregate(t *testing.T) {
	db := setupBillingTestDB(t)
	start, _, err := BillingPeriodBounds("2026-11")
	require.NoError(t, err)

	seedBillingLog(t, db, model.Log{
		UserId: 31, Username: "alice", CreatedAt: start + 10, Type: model.LogTypeConsume,
		ModelName: "gpt-4o-mini", Quota: 30000, PromptTokens: 100, CompletionTokens: 50,
	})
	seedBillingLog(t, db, model.Log{
		UserId: 31, Username: "alice", CreatedAt: start + 20, Type: model.LogTypeRefund,
		ModelName: "gpt-4o-mini", Quota: 10000,
	})
	seedBillingLog(t, db, model.Log{
		UserId: 32, Username: "bob", CreatedAt: start + 30, Type: model.LogTypeConsume,
		ModelName: "gpt-4o-mini", Quota: 90000,
	})
	// 只有错误行的用户不进列表：没有金额的账单没有对账价值。
	seedBillingLog(t, db, model.Log{
		UserId: 33, Username: "carol", CreatedAt: start + 40, Type: model.LogTypeError,
	})

	rows, isDraft, err := LoadBillingUserSummaries(context.Background(), "2026-11", "")
	require.NoError(t, err)
	assert.True(t, isDraft, "未出账账期必须标成实时")

	require.Len(t, rows, 2)
	assert.Equal(t, 32, rows[0].UserId, "金额大的在前")
	assert.EqualValues(t, 90000, rows[0].Quota)
	assert.Equal(t, 31, rows[1].UserId)
	assert.EqualValues(t, 20000, rows[1].Quota, "退款要冲减")
	assert.Equal(t, 1, rows[1].Requests, "退款不算请求数")
	assert.Equal(t, 1, rows[1].Refunds)
	assert.False(t, rows[1].IsIssued)
	// 测试库没有 users 表，用户名回落成 user#id，而不是整页失败。
	assert.Equal(t, "user#31", rows[1].Username)
}

func TestFilterBillingUserRows(t *testing.T) {
	rows := []BillingUserSummaryRow{
		{UserId: 41, Username: "zhangsan"},
		{UserId: 42, Username: "lisi"},
		{UserId: 43, Username: "WangWu"},
	}

	// 完整 id 精确命中，不会把 43 也带出来。
	matched := filterBillingUserRows(rows, "42")
	require.Len(t, matched, 1)
	assert.Equal(t, 42, matched[0].UserId)

	// 用户名片段忽略大小写。
	matched = filterBillingUserRows(rows, "ZHANG")
	require.Len(t, matched, 1)
	assert.Equal(t, 41, matched[0].UserId)

	// 空关键词返回原列表，不过滤。
	assert.Len(t, filterBillingUserRows(rows, "  "), 3)

	// 搜不到时返回空列表，由上层区分「无匹配」与「未出账」。
	assert.Empty(t, filterBillingUserRows(rows, "nobody"))
}
