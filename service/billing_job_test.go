package service

import (
	"context"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 出账任务必须是幂等的：重复跑只做「已存在」查询，不产生第二份账单，
// 也不覆盖已经生成的文件。
func TestRunBillingIssueForPeriodIsIdempotent(t *testing.T) {
	t.Setenv(ExportFileDirEnv, t.TempDir())
	db := setupBillingTestDB(t)
	start, _, err := BillingPeriodBounds("2026-10")
	require.NoError(t, err)

	for _, userId := range []int{21, 22} {
		seedBillingLog(t, db, model.Log{
			UserId: userId, CreatedAt: start + int64(userId),
			Type: model.LogTypeConsume, ModelName: "claude-sonnet-4.5",
			Group: "default", Quota: 10000, PromptTokens: 100, CompletionTokens: 50,
			RequestId: "req-" + string(rune('a'+userId)),
		})
	}
	// 只有错误行的用户不出账：没有金额的账单没有对账价值。
	seedBillingLog(t, db, model.Log{
		UserId: 23, CreatedAt: start + 99, Type: model.LogTypeError, Quota: 0,
	})

	first, err := RunBillingIssueForPeriod(context.Background(), "2026-10")
	require.NoError(t, err)
	assert.Equal(t, 2, first.Issued, "两个有消费的用户各出一份")
	assert.Equal(t, 0, first.Failed)
	assert.Equal(t, 4, first.Exports, "每人 admin + customer 两份文件")

	for _, userId := range []int{21, 22} {
		statement, found, err := model.FindBillingStatement("2026-10", userId)
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, model.BillingStatusIssued, statement.Status)

		for _, projection := range []BillingProjection{BillingProjectionAdmin, BillingProjectionCustomer} {
			export, err := model.GetBillingStatementExport(statement.Id, string(projection))
			require.NoError(t, err, "both projections must be registered")
			assert.EqualValues(t, 1, export.Rows)
			assert.NotEmpty(t, export.Sha256)
			_, err = ExportFilePath(export.ObjectKey)
			require.NoError(t, err, "registered key must resolve to a real file")
		}
	}

	second, err := RunBillingIssueForPeriod(context.Background(), "2026-10")
	require.NoError(t, err)
	assert.Equal(t, 0, second.Issued, "重跑不能再出第二份")
	assert.Equal(t, 2, second.Skipped)
	assert.Equal(t, 0, second.Exports, "已出账账期不重新生成文件")

	count := int64(0)
	require.NoError(t, db.Model(&model.BillingStatement{}).Where("period = ?", "2026-10").Count(&count).Error)
	assert.EqualValues(t, 2, count)
}

func TestBillingIssueRejectsBadPeriod(t *testing.T) {
	setupBillingTestDB(t)
	_, err := RunBillingIssueForPeriod(context.Background(), "2026-10-01")
	assert.Error(t, err)
}

func TestPreviousBillingPeriod(t *testing.T) {
	cst := time.FixedZone("CST", billingZoneOffset)
	cases := map[string]string{
		"2026-11-01T00:10:00+08:00": "2026-10",
		"2026-11-02T09:00:00+08:00": "2026-10",
		"2026-01-01T00:10:00+08:00": "2025-12",
		"2026-03-31T23:59:00+08:00": "2026-02",
	}
	for input, want := range cases {
		parsed, err := time.Parse(time.RFC3339, input)
		require.NoError(t, err)
		assert.Equal(t, want, previousBillingPeriod(parsed.In(cst)), input)
	}
}

// 出账窗口的边界：早一分钟不出账，晚一分钟就等到第二天。
func TestBillingIssueDueAt(t *testing.T) {
	cst := time.FixedZone("CST", billingZoneOffset)
	cases := map[string]bool{
		"2026-11-01T00:09:00+08:00": false,
		"2026-11-01T00:10:00+08:00": true,
		"2026-11-01T03:30:00+08:00": true,
		"2026-11-01T06:00:00+08:00": true,
		"2026-11-01T06:01:00+08:00": false,
		"2026-11-01T14:00:00+08:00": false,
		"2026-11-02T00:30:00+08:00": true,
	}
	for input, want := range cases {
		parsed, err := time.Parse(time.RFC3339, input)
		require.NoError(t, err)
		assert.Equal(t, want, billingIssueDueAt(parsed.In(cst)), input)
	}
}
