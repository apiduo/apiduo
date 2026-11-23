package service

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 保留期是「含当月往前数 N 个月」：2026-11 留 6 个月 → 保留 06..11，cutoff=2026-06。
func TestBillingRetentionCutoff(t *testing.T) {
	now := time.Date(2026, 11, 24, 8, 0, 0, 0, time.FixedZone("CST", billingZoneOffset))
	assert.Equal(t, "2026-06", billingRetentionCutoff(now, 6))
	assert.Equal(t, "2026-11", billingRetentionCutoff(now, 1))
	assert.Equal(t, "2026-02", billingRetentionCutoff(now, 10))

	january := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)
	assert.Equal(t, "2025-08", billingRetentionCutoff(january, 6), "跨年要往前退一年")
}

// 只删完全早于 cutoff 的账期；明细、归档记录与磁盘文件一起删，保留下来的一个都不能动。
func TestPurgeBillingBeforeRemovesOlderPeriodsOnly(t *testing.T) {
	db := setupBillingTestDB(t)
	root := t.TempDir()
	t.Setenv(ExportFileDirEnv, root)

	seed := func(period string) (statement *model.BillingStatement, objectKey string) {
		statement = &model.BillingStatement{
			Period: period, UserId: 7, Username: "u7", Status: model.BillingStatusIssued,
			PeriodStart: 1, PeriodEnd: 2,
		}
		require.NoError(t, db.Create(statement).Error)
		require.NoError(t, db.Create(&model.BillingStatementLine{
			StatementId: statement.Id, UserId: 7, CreatedAt: 1,
			Type: model.LogTypeConsume, Quota: 100, Amount: "0.000200",
		}).Error)

		objectKey = BillingExportKey(period, statement.Id, 7, BillingProjectionAdmin)
		_, _, err := WriteExportFile(objectKey, func(w io.Writer) error {
			_, writeErr := w.Write([]byte("xlsx"))
			return writeErr
		})
		require.NoError(t, err)
		require.NoError(t, model.UpsertBillingStatementExport(&model.BillingStatementExport{
			StatementId: statement.Id, Projection: string(BillingProjectionAdmin),
			ObjectKey: objectKey, Bytes: 4, Rows: 1, Sha256: "x",
		}))
		return statement, objectKey
	}

	_, oldKey := seed("2026-05")
	_, keptKey := seed("2026-06") // cutoff 自身要保留

	result, err := PurgeBillingBefore(context.Background(), "2026-06")
	require.NoError(t, err)
	assert.Equal(t, "2026-06", result.Cutoff)
	assert.Equal(t, 1, result.Statements)
	assert.EqualValues(t, 1, result.Lines)
	assert.Equal(t, 1, result.Exports)
	assert.Equal(t, 1, result.Files)
	assert.Greater(t, result.Bytes, int64(0))

	_, found, err := model.FindBillingStatement("2026-05", 7)
	require.NoError(t, err)
	assert.False(t, found, "早于 cutoff 的账单头要删掉")

	oldPath, err := ExportFilePath(oldKey)
	require.NoError(t, err)
	_, statErr := os.Stat(oldPath)
	assert.True(t, os.IsNotExist(statErr), "归档文件要跟着删")

	_, found, err = model.FindBillingStatement("2026-06", 7)
	require.NoError(t, err)
	assert.True(t, found, "cutoff 自身不删")

	keptPath, err := ExportFilePath(keptKey)
	require.NoError(t, err)
	_, statErr = os.Stat(keptPath)
	assert.NoError(t, statErr, "保留期内的文件不能动")

	// 幂等：再跑一次什么都不删。
	again, err := PurgeBillingBefore(context.Background(), "2026-06")
	require.NoError(t, err)
	assert.Zero(t, again.Statements)
}

// 保留期设成 0 表示永久保留，不能有任何删除动作。
func TestPurgeBillingRetentionDisabled(t *testing.T) {
	setupBillingTestDB(t)
	t.Setenv("BILLING_RETENTION_MONTHS", "0")

	result, err := PurgeBillingRetention(context.Background(), time.Now())
	require.NoError(t, err)
	assert.Zero(t, result.Statements)
	assert.Empty(t, result.Cutoff)
}

// 手动补跑与定时出账共用一把锁：同时在跑时第二次启动必须失败。
func TestTryStartBillingIssueIsExclusive(t *testing.T) {
	assert.True(t, TryStartBillingIssue("2026-10"))
	status := BillingIssueStatusSnapshot()
	assert.True(t, status.Running)
	assert.Equal(t, "2026-10", status.Period)

	assert.False(t, TryStartBillingIssue("2026-09"), "已有出账在跑时不能再起一份")

	FinishBillingIssue("2026-10", BillingIssueResult{Period: "2026-10", Issued: 2, Skipped: 1}, nil)
	status = BillingIssueStatusSnapshot()
	assert.False(t, status.Running)
	assert.Equal(t, 2, status.Issued)
	assert.Equal(t, 1, status.Skipped)
	assert.Empty(t, status.Error)
	assert.NotZero(t, status.FinishedAt)

	assert.True(t, TryStartBillingIssue("2026-09"), "上一次跑完可以再起")
	FinishBillingIssue("2026-09", BillingIssueResult{}, os.ErrDeadlineExceeded)
	status = BillingIssueStatusSnapshot()
	assert.False(t, status.Running)
	assert.Equal(t, os.ErrDeadlineExceeded.Error(), status.Error)
}
