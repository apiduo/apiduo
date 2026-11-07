package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The migration derives the replacement DDL from the upstream builder instead
// of copying the column list, so this pins the two substitutions that turn it
// into a replacing table.
func TestReplacingLogTableDDL(t *testing.T) {
	ddl, err := replacingLogTableDDL("logs_replacing_staging", 0)
	require.NoError(t, err)
	assert.Contains(t, ddl, "CREATE TABLE IF NOT EXISTS logs_replacing_staging")
	assert.Contains(t, ddl, "ENGINE = ReplacingMergeTree()")
	assert.Contains(t, ddl, "ORDER BY (created_at, request_id)",
		"去重键就是排序键；改掉它 ReplacingMergeTree 会按错误的键折叠")
	assert.NotContains(t, ddl, "ENGINE = MergeTree()")

	withTTL, err := replacingLogTableDDL("logs_replacing_staging", 30)
	require.NoError(t, err)
	assert.Contains(t, withTTL, "TTL toDateTime(created_at)",
		"迁移必须带上 TTL，否则换表后保留策略丢失")
}

// Only ClickHouse needs FINAL; the other log databases insert once and would
// reject the keyword.
func TestLogQueryTable(t *testing.T) {
	common.SetLogDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() { common.SetLogDatabaseType(common.DatabaseTypeSQLite) })
	assert.Equal(t, "logs", logQueryTable(), "默认（SQLite/MySQL/PG）应返回裸表名")

	common.SetLogDatabaseType(common.DatabaseTypeClickHouse)
	assert.Equal(t, "logs FINAL", logQueryTable(),
		"ClickHouse 上必须读 FINAL；漏掉它，重放的重复行会直接进入 sum(quota)")
}

// The branch functions must hand non-ClickHouse databases back to the upstream
// query instead of running FINAL against a database that rejects it.
func TestFinalBranchesSkipNonClickHouse(t *testing.T) {
	common.SetLogDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() { common.SetLogDatabaseType(common.DatabaseTypeSQLite) })

	_, ok, err := sumUsedQuotaFinal(2, 0, 0, "", "", "", 0, "")
	assert.False(t, ok, "非 ClickHouse 必须交回上游查询")
	assert.NoError(t, err)

	token, ok := sumUsedTokenFinal(0, 0, "", "", "")
	assert.False(t, ok, "非 ClickHouse 必须交回上游查询")
	assert.Zero(t, token)
}

func TestLogWhereClause(t *testing.T) {
	w := &logWhere{}
	require.NoError(t, w.text("username", "alice"))
	w.eq("type", 2)
	w.raw("created_at >= ?", 100)
	assert.Equal(t, " WHERE username = ? AND type = ? AND created_at >= ?", w.clause())
	assert.Equal(t, []any{"alice", 2, 100}, w.args)

	// Empty text filter contributes nothing.
	empty := &logWhere{}
	require.NoError(t, empty.text("username", ""))
	assert.Equal(t, "", empty.clause())
}
