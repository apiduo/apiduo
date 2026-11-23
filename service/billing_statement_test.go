package service

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 出账测试用内存 SQLite，把主库与日志库都指向它：账单查询跨两边，
// 分开测反而漏掉「lines 写进去了、statement 没更新」这类问题。
func setupBillingTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:billing-%d?mode=memory&cache=shared", time.Now().UnixNano())),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)

	previousDB, previousLogDB := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = db, db
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
	})

	require.NoError(t, db.AutoMigrate(
		&model.Log{},
		&model.BillingStatement{},
		&model.BillingStatementLine{},
		&model.BillingStatementExport{},
	))
	return db
}

func seedBillingLog(t *testing.T, db *gorm.DB, log model.Log) {
	t.Helper()
	require.NoError(t, db.Create(&log).Error)
}

// 账期边界按东八区算：9 月 30 日 23:59 属于 9 月，10 月 1 日 00:00 属于 10 月。
func TestBillingPeriodBounds(t *testing.T) {
	start, end, err := BillingPeriodBounds("2026-10")
	require.NoError(t, err)

	assert.Equal(t, "2026-10-01T00:00:00+08:00", time.Unix(start, 0).In(time.FixedZone("CST", 8*3600)).Format(time.RFC3339))
	assert.Equal(t, "2026-11-01T00:00:00+08:00", time.Unix(end, 0).In(time.FixedZone("CST", 8*3600)).Format(time.RFC3339))
	assert.EqualValues(t, 31*24*3600, end-start)

	_, _, err = BillingPeriodBounds("2026-13")
	assert.Error(t, err, "month 13 must be rejected")
	_, _, err = BillingPeriodBounds("2026/10")
	assert.Error(t, err)
}

func TestBuildBillingStatementFoldsLogs(t *testing.T) {
	db := setupBillingTestDB(t)
	start, _, err := BillingPeriodBounds("2026-10")
	require.NoError(t, err)

	seedBillingLog(t, db, model.Log{
		UserId: 1, Username: "alice", CreatedAt: start + 3600,
		Type: model.LogTypeConsume, ModelName: "claude-sonnet-4.5",
		Group: "vip", TokenName: "sk-alice", RequestId: "a-1",
		Quota: 100000, PromptTokens: 1000, CompletionTokens: 500,
		Other: `{"cache_tokens":200,"cache_write_tokens":100}`,
	})
	seedBillingLog(t, db, model.Log{
		UserId: 1, Username: "alice", CreatedAt: start + 7200,
		Type: model.LogTypeRefund, ModelName: "seedance-1.0-pro",
		Group: "vip", RequestId: "a-2", Quota: 20000,
		Other: `{"task_id":"task-9"}`,
	})
	seedBillingLog(t, db, model.Log{
		UserId: 1, Username: "alice", CreatedAt: start + 9000,
		Type: model.LogTypeError, ModelName: "gpt-5.1",
		Group: "vip", RequestId: "a-3",
	})
	// 别的用户、别的月份都不该进来
	seedBillingLog(t, db, model.Log{UserId: 2, CreatedAt: start + 60, Type: model.LogTypeConsume, Quota: 999999})
	seedBillingLog(t, db, model.Log{UserId: 1, CreatedAt: start - 60, Type: model.LogTypeConsume, Quota: 888888})

	snapshot := BillingSnapshot{QuotaPerUnit: 500000, SettleRate: 7.3, Currency: "CNY"}
	statement, created, err := BuildBillingStatement(context.Background(), "2026-10", 1, "alice", snapshot)
	require.NoError(t, err)
	require.True(t, created)

	assert.Equal(t, model.BillingStatusIssued, statement.Status)
	assert.Equal(t, 2, statement.Requests, "consume + error")
	assert.Equal(t, 1, statement.Errors)
	assert.Equal(t, 1, statement.Refunds)
	assert.EqualValues(t, 80000, statement.TotalQuota, "refund is subtracted, not added")
	assert.Equal(t, "1.168000", statement.TotalAmount)
	assert.InDelta(t, 7.3, statement.SettleRate, 1e-9)

	lines, err := model.ListBillingStatementLines(statement.Id, model.BillingLineFilter{}, 0, 50)
	require.NoError(t, err)
	require.Len(t, lines, 3)

	// 时间正序：消费 → 退款 → 错误
	assert.Equal(t, "a-1", lines[0].RequestId)
	assert.Equal(t, 700, lines[0].InputTokens, "prompt minus cache read and write")
	assert.Equal(t, 200, lines[0].CacheReadTokens)
	assert.Equal(t, 100, lines[0].CacheWriteTokens)
	assert.Equal(t, "1.460000", lines[0].Amount, "100000 quota = 0.2 USD = 1.46 CNY")

	assert.Equal(t, "a-2", lines[1].RequestId)
	assert.EqualValues(t, -20000, lines[1].Quota, "refunds are stored signed")
	assert.Equal(t, "-0.292000", lines[1].Amount)
	assert.Equal(t, "task-9", lines[1].TaskId)

	assert.Equal(t, "a-3", lines[2].RequestId)
	assert.EqualValues(t, 0, lines[2].Quota)
}

func TestBuildBillingStatementIsIdempotent(t *testing.T) {
	db := setupBillingTestDB(t)
	start, _, err := BillingPeriodBounds("2026-10")
	require.NoError(t, err)
	seedBillingLog(t, db, model.Log{
		UserId: 1, CreatedAt: start + 60, Type: model.LogTypeConsume,
		ModelName: "gpt-5.1", Quota: 1000,
	})

	snapshot := BillingSnapshot{QuotaPerUnit: 500000, SettleRate: 7.3, Currency: "CNY"}
	first, created, err := BuildBillingStatement(context.Background(), "2026-10", 1, "alice", snapshot)
	require.NoError(t, err)
	require.True(t, created)

	second, created, err := BuildBillingStatement(context.Background(), "2026-10", 1, "alice", snapshot)
	require.NoError(t, err)
	assert.False(t, created, "a second run must not produce a second statement")
	assert.Equal(t, first.Id, second.Id)
	assert.Equal(t, first.TotalAmount, second.TotalAmount)

	total, err := model.CountBillingStatementLines(first.Id)
	require.NoError(t, err)
	assert.EqualValues(t, 1, total, "rerunning must not duplicate lines")
}

func TestBuildBillingStatementChoosesSnapshotOnce(t *testing.T) {
	db := setupBillingTestDB(t)
	start, _, err := BillingPeriodBounds("2026-10")
	require.NoError(t, err)
	seedBillingLog(t, db, model.Log{
		UserId: 1, CreatedAt: start + 60, Type: model.LogTypeConsume,
		ModelName: "gpt-5.1", Quota: 500000,
	})

	// 出账时汇率是 7.3，之后管理员改成 9.0，历史账单不能跟着变。
	previousRate := operation_setting.USDExchangeRate
	t.Cleanup(func() { operation_setting.USDExchangeRate = previousRate })
	operation_setting.USDExchangeRate = 7.3

	statement, _, err := BuildBillingStatement(context.Background(), "2026-10", 1, "alice", CurrentBillingSnapshot())
	require.NoError(t, err)
	assert.Equal(t, "7.300000", statement.TotalAmount)

	operation_setting.USDExchangeRate = 9.0
	reloaded, err := model.GetBillingStatement("2026-10", 1)
	require.NoError(t, err)
	assert.Equal(t, "7.300000", reloaded.TotalAmount, "the statement keeps its own snapshot")
	assert.InDelta(t, 7.3, reloaded.SettleRate, 1e-9)
}

func TestCurrentBillingPeriodUsesShanghai(t *testing.T) {
	// UTC 9 月 30 日 16:30 = 东八区 10 月 1 日 00:30
	utc := time.Date(2026, 9, 30, 16, 30, 0, 0, time.UTC)
	assert.Equal(t, "2026-10", CurrentBillingPeriod(utc))
	assert.Equal(t, "2026-09", CurrentBillingPeriod(utc.Add(-time.Hour)))
}

func TestBuildBillingStatementRejectsBadPeriod(t *testing.T) {
	setupBillingTestDB(t)
	_, _, err := BuildBillingStatement(context.Background(), "2026-1", 1, "alice", BillingSnapshot{QuotaPerUnit: 500000})
	require.Error(t, err)

	_ = common.QuotaPerUnit
}

// 业务逻辑在 SQLite 上跑得快，但 SQLite 证明不了 MySQL 的唯一索引、varchar 长度
// 与批量插入行为。这里用同一个用例跑多个库：没有 DSN 就跳过，不假装验证过。
func TestBuildBillingStatementDatabaseMatrix(t *testing.T) {
	cases := []struct {
		name   string
		env    string
		driver string
	}{
		{"sqlite", "", "sqlite"},
		{"mysql", "BILLING_TEST_MYSQL_DSN", "mysql"},
		{"postgres", "BILLING_TEST_POSTGRES_DSN", "postgres"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dsn := os.Getenv(tc.env)
			if tc.env != "" && dsn == "" {
				t.Skip(tc.env + " is not configured")
			}

			var dialector gorm.Dialector
			switch tc.driver {
			case "mysql":
				dialector = mysql.Open(dsn)
			case "postgres":
				dialector = postgres.Open(dsn)
			default:
				dialector = sqlite.Open(fmt.Sprintf("file:matrix-%d?mode=memory&cache=shared", time.Now().UnixNano()))
			}

			db, err := gorm.Open(dialector, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			require.NoError(t, err)

			previousDB, previousLogDB := model.DB, model.LOG_DB
			model.DB, model.LOG_DB = db, db
			t.Cleanup(func() {
				model.DB, model.LOG_DB = previousDB, previousLogDB
			})

			// 每次从空表开始，避免上一轮残留撞唯一键
			require.NoError(t, db.Migrator().DropTable(
				&model.Log{},
				&model.BillingStatementLine{},
				&model.BillingStatementExport{},
				&model.BillingStatement{},
			))
			require.NoError(t, db.AutoMigrate(
				&model.Log{},
				&model.BillingStatement{},
				&model.BillingStatementLine{},
				&model.BillingStatementExport{},
			))

			start, _, err := BillingPeriodBounds("2026-10")
			require.NoError(t, err)
			seedBillingLog(t, db, model.Log{
				UserId: 7, Username: "matrix", CreatedAt: start + 60,
				Type: model.LogTypeConsume, ModelName: "gpt-5.1", Group: "vip",
				TokenName: "sk-m", RequestId: "m-1", Quota: 250000,
				PromptTokens: 800, CompletionTokens: 200,
				Other: `{"cache_tokens":300,"cache_write_tokens":100}`,
			})
			seedBillingLog(t, db, model.Log{
				UserId: 7, Username: "matrix", CreatedAt: start + 120,
				Type: model.LogTypeRefund, ModelName: "gpt-5.1", Group: "vip",
				RequestId: "m-2", Quota: 50000,
			})

			statement, created, err := BuildBillingStatement(context.Background(), "2026-10", 7, "matrix",
				BillingSnapshot{QuotaPerUnit: 500000, SettleRate: 7.3, Currency: "CNY"})
			require.NoError(t, err)
			require.True(t, created)
			assert.EqualValues(t, 200000, statement.TotalQuota)
			assert.Equal(t, "2.920000", statement.TotalAmount)

			lines, err := model.ListBillingStatementLines(statement.Id, model.BillingLineFilter{}, 0, 10)
			require.NoError(t, err)
			require.Len(t, lines, 2)
			assert.Equal(t, 400, lines[0].InputTokens, "800 - 300 - 100")
			assert.EqualValues(t, -50000, lines[1].Quota)

			// 唯一键必须真的挡住第二次出账
			_, created, err = BuildBillingStatement(context.Background(), "2026-10", 7, "matrix",
				BillingSnapshot{QuotaPerUnit: 500000, SettleRate: 7.3, Currency: "CNY"})
			require.NoError(t, err)
			assert.False(t, created)
		})
	}
}
