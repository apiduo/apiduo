package model

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// sqlCaptureLogger 记录 GORM 实际执行的 SQL，供「列清单」类断言使用。
type sqlCaptureLogger struct {
	queries []string
}

func (l *sqlCaptureLogger) LogMode(logger.LogLevel) logger.Interface { return l }
func (l *sqlCaptureLogger) Info(_ context.Context, msg string, data ...any) {
	l.queries = append(l.queries, fmt.Sprintf(msg, data...))
}
func (l *sqlCaptureLogger) Warn(_ context.Context, msg string, data ...any) {
	l.queries = append(l.queries, fmt.Sprintf(msg, data...))
}
func (l *sqlCaptureLogger) Error(_ context.Context, msg string, data ...any) {
	l.queries = append(l.queries, fmt.Sprintf(msg, data...))
}
func (l *sqlCaptureLogger) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	l.queries = append(l.queries, sql)
}

// Omit 会让 GORM 从 SELECT * 切换成逐列枚举，gorm:"->" 的只读字段也会被写进
// 列清单。ClickHouse 的 logs 表没有 channel_name，任一查询漏排这一列都会在
// 生产报「Identifier 'logs.channel_name' cannot be resolved」—— 而 SQLite /
// MySQL / PostgreSQL 都建了这个字段（只读只是不写它），本地矩阵永远看不见。
//
// 这个用例走真实查询入口，改回 Omit("content") 就会红。
func TestLogQueriesOmitReadOnlyChannelName(t *testing.T) {
	loggerCapture := &sqlCaptureLogger{}
	db, err := gorm.Open(sqlite.Open("file:log-omit-columns?mode=memory&cache=shared"),
		&gorm.Config{Logger: loggerCapture})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Log{}))
	// 建表语句本身就含列定义，清掉后再看真实查询。
	loggerCapture.queries = nil

	previousLogDB := LOG_DB
	LOG_DB = db
	t.Cleanup(func() { LOG_DB = previousLogDB })

	start, end := int64(1_700_000_000), int64(1_700_100_000)
	require.NoError(t, db.Create(&Log{
		UserId: 1, CreatedAt: start + 10, Type: LogTypeConsume,
		ModelName: "m", Quota: 1, Other: `{}`,
	}).Error)

	if _, err := ListUserLogsForBilling(context.Background(), 1, start, end, BillingLineFilter{}, BillingLineCursor{}, 10); err != nil {
		t.Fatalf("ListUserLogsForBilling: %v", err)
	}
	if err := StreamUserLogsForBilling(context.Background(), 1, start, end, BillingLineFilter{}, func(*Log) error { return nil }); err != nil {
		t.Fatalf("StreamUserLogsForBilling: %v", err)
	}

	var selects []string
	for _, query := range loggerCapture.queries {
		if strings.Contains(query, "FROM `logs`") {
			selects = append(selects, query)
		}
	}
	require.Len(t, selects, 2, "分页与流式各执行一次查询")
	for _, query := range selects {
		assert.NotContains(t, query, "channel_name",
			"Omit 模式下不能枚举只读列：ClickHouse 的 logs 表没有这一列")
		assert.NotContains(t, query, "content", "请求正文不参与导出与账单")
		assert.Contains(t, query, "model_name", "业务列仍要查")
	}
}
