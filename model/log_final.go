package model

import (
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// Replay de-duplication for the ClickHouse log table (review item L5).
//
// The log writer is at-least-once: crashing between the insert and the spool
// marker replays that batch on the next start. The table used to be a plain
// MergeTree, where ORDER BY (created_at, request_id) is only a sort key, so a
// replayed batch stayed as duplicate rows and every count(*)/sum(quota) that
// read them doubled.
//
// Two halves, and neither works alone:
//   - the engine becomes ReplacingMergeTree, whose background merges collapse
//     rows sharing the sorting key;
//   - the three aggregations read with FINAL, which collapses them for that
//     query instead of waiting for a merge.
//
// Everything here is additive: the upstream queries keep their own code path
// and only branch out when the log database is ClickHouse.

// logQueryTable is the FROM target for log aggregations. ClickHouse needs
// FINAL; the other log databases insert once, never replay, and reject it.
func logQueryTable() string {
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return "logs FINAL"
	}
	return "logs"
}

// logWhere accumulates a parameterised WHERE body. Text filters keep the LIKE
// form the upstream query builder produces, because a value containing % is a
// pattern rather than a literal.
type logWhere struct {
	conditions []string
	args       []any
}

func (w *logWhere) text(column string, value string) error {
	if value == "" {
		return nil
	}
	if strings.Contains(value, "%") {
		condition, pattern, err := buildLogLikeCondition(column, value)
		if err != nil {
			return err
		}
		w.conditions = append(w.conditions, condition)
		w.args = append(w.args, pattern)
		return nil
	}
	w.conditions = append(w.conditions, column+" = ?")
	w.args = append(w.args, value)
	return nil
}

func (w *logWhere) eq(column string, value any) {
	w.conditions = append(w.conditions, column+" = ?")
	w.args = append(w.args, value)
}

func (w *logWhere) raw(condition string, args ...any) {
	w.conditions = append(w.conditions, condition)
	w.args = append(w.args, args...)
}

func (w *logWhere) clause() string {
	if len(w.conditions) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(w.conditions, " AND ")
}

// with returns a copy carrying one more condition, leaving the receiver intact
// so the consumption window and the rpm window can share a base filter.
func (w *logWhere) with(condition string, args ...any) *logWhere {
	next := &logWhere{
		conditions: make([]string, 0, len(w.conditions)+1),
		args:       make([]any, 0, len(w.args)+1),
	}
	next.conditions = append(next.conditions, w.conditions...)
	next.args = append(next.args, w.args...)
	next.conditions = append(next.conditions, condition)
	next.args = append(next.args, args...)
	return next
}

// sumUsedQuotaFinal answers SumUsedQuota for ClickHouse. ok is false when this
// database does not need (and would reject) FINAL, in which case the caller
// runs the upstream query unchanged.
func sumUsedQuotaFinal(logType int, startTimestamp int64, endTimestamp int64, modelName string, username string, tokenName string, channel int, group string) (stat Stat, ok bool, err error) {
	if !common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return Stat{}, false, nil
	}

	where := &logWhere{}
	if err = where.text("username", username); err != nil {
		return stat, true, err
	}
	if tokenName != "" {
		where.eq("token_name", tokenName)
	}
	if startTimestamp != 0 {
		where.raw("created_at >= ?", startTimestamp)
	}
	if endTimestamp != 0 {
		where.raw("created_at <= ?", endTimestamp)
	}
	if err = where.text("model_name", modelName); err != nil {
		return stat, true, err
	}
	if channel != 0 {
		where.eq("channel_id", channel)
	}
	if group != "" {
		where.eq(logGroupCol, group)
	}
	where.eq("type", LogTypeConsume)

	table := logQueryTable()
	quotaSQL := "SELECT COALESCE(sum(quota), 0) AS quota FROM " + table + where.clause()
	if err = LOG_DB.Raw(quotaSQL, where.args...).Scan(&stat).Error; err != nil {
		common.SysError(common.LogText("failed to query log stat: %s", err.Error()))
		return stat, true, common.NewMessage("Failed to query statistics")
	}

	// Only the last 60 seconds count towards rpm/tpm.
	rate := where.with("created_at >= ?", time.Now().Add(-60*time.Second).Unix())
	var rateStat struct {
		Rpm int
		Tpm int
	}
	rateSQL := "SELECT count(*) AS rpm, COALESCE(sum(prompt_tokens), 0) + COALESCE(sum(completion_tokens), 0) AS tpm FROM " + table + rate.clause()
	if err = LOG_DB.Raw(rateSQL, rate.args...).Scan(&rateStat).Error; err != nil {
		common.SysError(common.LogText("failed to query rpm/tpm stat: %s", err.Error()))
		return stat, true, common.NewMessage("Failed to query statistics")
	}
	stat.Rpm = rateStat.Rpm
	stat.Tpm = rateStat.Tpm

	return stat, true, nil
}

// sumUsedTokenFinal answers SumUsedToken for ClickHouse.
func sumUsedTokenFinal(startTimestamp int64, endTimestamp int64, modelName string, username string, tokenName string) (token int, ok bool) {
	if !common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return 0, false
	}

	where := &logWhere{}
	if username != "" {
		where.eq("username", username)
	}
	if tokenName != "" {
		where.eq("token_name", tokenName)
	}
	if startTimestamp != 0 {
		where.raw("created_at >= ?", startTimestamp)
	}
	if endTimestamp != 0 {
		where.raw("created_at <= ?", endTimestamp)
	}
	if modelName != "" {
		where.eq("model_name", modelName)
	}
	sql := "SELECT COALESCE(sum(prompt_tokens), 0) + COALESCE(sum(completion_tokens), 0) AS total FROM " + logQueryTable() + where.clause()
	if err := LOG_DB.Raw(sql, where.args...).Scan(&token).Error; err != nil {
		common.SysError(common.LogText("failed to query log token: %s", err.Error()))
		return 0, true
	}
	return token, true
}

// upgradeClickHouseLogEngine swaps an existing MergeTree log table for
// ReplacingMergeTree.
//
// CREATE TABLE IF NOT EXISTS never changes an engine, so deployed tables need a
// one-time move. The replacement is built under a staging name and both tables
// are swapped with a single RENAME, so `logs` is never missing. The old table is
// renamed rather than dropped: an operator can verify the new one first.
//
// The DDL is derived from the upstream builder instead of being copied, so a
// column added upstream reaches this migration automatically.
func upgradeClickHouseLogEngine(ttlDays int) error {
	var current string
	if err := LOG_DB.Raw("SHOW CREATE TABLE logs").Scan(&current).Error; err != nil {
		return err
	}
	if strings.Contains(current, "ReplacingMergeTree") {
		return nil
	}

	const (
		staging = "logs_replacing_staging"
		legacy  = "logs_mergetree_backup"
	)
	stagingDDL, err := replacingLogTableDDL(staging, ttlDays)
	if err != nil {
		return err
	}

	for _, statement := range []string{
		"DROP TABLE IF EXISTS " + staging,
		stagingDDL,
		"INSERT INTO " + staging + " SELECT * FROM logs",
		"DROP TABLE IF EXISTS " + legacy,
		"RENAME TABLE logs TO " + legacy + ", " + staging + " TO logs",
	} {
		if err := LOG_DB.Exec(statement).Error; err != nil {
			return err
		}
	}
	common.SysLog(common.LogText("log table engine upgraded to ReplacingMergeTree; previous table kept as %s", legacy))
	return nil
}

// replacingLogTableDDL derives the replacement DDL from the upstream builder so
// a column added there reaches the migration automatically. It fails loudly
// rather than returning a MergeTree statement if the upstream engine line ever
// changes shape.
func replacingLogTableDDL(table string, ttlDays int) (string, error) {
	base := clickHouseLogCreateTableSQL(ttlDays)
	ddl := strings.Replace(base, "CREATE TABLE IF NOT EXISTS logs", "CREATE TABLE IF NOT EXISTS "+table, 1)
	ddl = strings.Replace(ddl, "ENGINE = MergeTree()", "ENGINE = ReplacingMergeTree()", 1)
	if !strings.Contains(ddl, "ReplacingMergeTree") {
		return "", fmt.Errorf("log table DDL does not declare ENGINE = MergeTree(); cannot derive the replacing engine")
	}
	return ddl, nil
}
