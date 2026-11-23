package model

import (
	"context"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 存在性判断必须是「要一行就走」，不能退化成 count。
//
// 线上 user_id=64 的详情页间歇返回 User not found.，日志里是
// billing_draft_query.go 的 clickhouse error 241（服务端总内存被聚合查询
// 打满，count 在 12ms 内被直接拒绝）。用 LIMIT 1 之后这条查询的代价与
// 账期行数无关，也不会因为别人吃满预算就被判成「这个用户没有账单」。
func TestBillingUserHasUsageProbesOneRow(t *testing.T) {
	capture := &sqlCaptureLogger{}
	db, err := gorm.Open(sqlite.Open("file:billing-usage-probe?mode=memory&cache=shared"),
		&gorm.Config{Logger: capture})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Log{}))
	// 建表语句里也有 "logs"，清掉后再看真实查询。
	capture.queries = nil

	previousLogDB := LOG_DB
	LOG_DB = db
	t.Cleanup(func() { LOG_DB = previousLogDB })

	require.NoError(t, db.Create(&Log{
		UserId: 64, CreatedAt: 500, Type: LogTypeConsume, ModelName: "m", Other: `{}`,
	}).Error)
	capture.queries = nil

	exists, err := BillingUserHasUsage(context.Background(), 64, 400, 600)
	require.NoError(t, err)
	assert.True(t, exists)

	require.NotEmpty(t, capture.queries)
	query := strings.ToLower(capture.queries[0])
	assert.Contains(t, query, "limit 1", "探测必须只取一行")
	assert.NotContains(t, query, "count(", "存在性判断不该数总数")

	// 没行时返回 (false, nil)：Take 的 ErrRecordNotFound 是正常结果，不是错误，
	// 漏判会让上层把一次成功的「没有」当成查询故障。
	capture.queries = nil
	exists, err = BillingUserHasUsage(context.Background(), 64, 1000, 2000)
	require.NoError(t, err)
	assert.False(t, exists)

	// 账期外的行不算：边界是 [start, end)。
	exists, err = BillingUserHasUsage(context.Background(), 64, 501, 600)
	require.NoError(t, err)
	assert.False(t, exists)
}
