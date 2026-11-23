package model

import (
	"context"
	"strconv"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

// 当月账单的实时视图。
//
// 已出账的账期读 billing_statement_lines，当月没有终点、也不能为它落一份
// 会立刻过期的明细，所以这里直接读 logs。两条路径共用同一个 BillingLineFilter
// 与同一个输出结构，页面与导出不必知道数据来自哪边。
//
// 聚合下推到数据库：一个月几十万行，搬进 Go 再 GROUP BY 会同时占满连接与内存，
// 而这些请求还要和转发抢同一个连接池。

// ClickHouse 聚合查询的内存策略。
//
// 线上账单页首屏连续三次全挂，报 (total) memory limit exceeded（current RSS
// 4.00 GiB / maximum 4.00 GiB），连 internal=0 也一样，前端只剩骨架。根因是
// 服务端两个默认值叠在一起：max_bytes_before_external_group_by=0 让分组聚合
// 只走内存、永不落盘，max_threads=32 又按线程数放大中间缓冲，而一个 60 万行
// 账期的汇总要把 other 大列（实测 185 MiB）逐行解四次 JSON。
//
// 手工加一组 settings 后同一查询秒级返回，所以这里固化成「到 200MB 就落盘 +
// 限 2 线程 + 限 1G 预算」。只作用于这两个聚合查询，不影响转发链路。
const (
	billingAggregateMemoryUsage = 1_000_000_000 // 1 GiB，比服务端默认 4G 更保守
	billingAggregateSpillBytes  = 200_000_000   // 200 MB，超过就溢写到磁盘
	billingAggregateThreads     = 2             // 默认 32，是中间缓冲的主要放大项
)

// billingAggregateContext 给聚合查询挂上上面那组内存策略。
//
// 只能走查询级 settings（clickhouse.Context 随 statement.Context 下发到驱动），
// 不能拼进 SQL：GORM 把 Table() 的表达式放在 FROM 位置，写
// `Table("(?) AS agg SETTINGS ...")` 会生成
// `FROM (...) SETTINGS ... WHERE ... GROUP BY ...`，SETTINGS 落在 WHERE 之前，
// ClickHouse 直接语法错误（DryRun 验过）。其他方言没有这些参数，原样返回。
func billingAggregateContext(ctx context.Context) context.Context {
	if !common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return ctx
	}
	return clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"max_memory_usage":                   billingAggregateMemoryUsage,
		"max_bytes_before_external_group_by": billingAggregateSpillBytes,
		"max_threads":                        billingAggregateThreads,
	}))
}

// billingJSONInt 返回从 logs.other 里取一个整数的方言表达式。
//
// key 只接受本文件里的两个常量，不来自请求。日志库四种形态都要能跑：
// 生产是 ClickHouse，开发与小型部署常见 SQLite / MySQL / PostgreSQL。
// 空 other 必须返回 0 而不是报错——历史行里确实有空串，一条坏数据不该
// 让整页 500；非法 JSON 属于写入侧 bug，让它报出来更好排查。
func billingJSONInt(key string) string {
	switch common.LogDatabaseType() {
	case common.DatabaseTypeClickHouse:
		return "JSONExtractInt(other, '" + key + "')"
	case common.DatabaseTypePostgreSQL:
		return "CASE WHEN other IS NULL OR other = '' THEN 0 ELSE COALESCE((other::json->>'" + key + "')::bigint, 0) END"
	case common.DatabaseTypeMySQL:
		return "CASE WHEN other IS NULL OR other = '' THEN 0 ELSE COALESCE(CAST(JSON_UNQUOTE(JSON_EXTRACT(other, '$." + key + "')) AS SIGNED), 0) END"
	default:
		return "CASE WHEN other IS NULL OR other = '' THEN 0 ELSE COALESCE(json_extract(other, '$." + key + "'), 0) END"
	}
}

// billingGroupColumn 返回引用 logs.group 的写法。
// group 在 ClickHouse、MySQL、PostgreSQL 里都是保留字，不转义直接语法错误；
// SQLite 同时接受反引号与双引号。
func billingGroupColumn() string {
	if common.UsingLogDatabase(common.DatabaseTypePostgreSQL) {
		return `"group"`
	}
	return "`group`"
}

// CountUserLogsForBilling 统计一个账期内要进账单的行数，用于导出前拒绝超限，
// 而不是写到一半停下。错误行计入统计，导出时再按规则排除。
func CountUserLogsForBilling(userId int, start, end int64) (int64, error) {
	var total int64
	err := LOG_DB.Model(&Log{}).
		Where("user_id = ? AND created_at >= ? AND created_at < ? AND type IN ?",
			userId, start, end, billingConsumeTypes()).
		Count(&total).Error
	return total, err
}

// billingConsumeTypes 是账单明细的来源类型：消费、错误、退款。
// 充值与管理日志与用量无关，不进来。
func billingConsumeTypes() []int {
	return []int{LogTypeConsume, LogTypeError, LogTypeRefund}
}

// applyBillingLineFilter 把明细筛选条件落到 logs 查询上。
// 空值表示不限；type = 0 同样表示不限，与 BillingLineFilter 的约定一致。
func applyBillingLineFilter(tx *gorm.DB, filter BillingLineFilter) *gorm.DB {
	if filter.ModelName != "" {
		tx = tx.Where("model_name = ?", filter.ModelName)
	}
	if filter.UseGroup != "" {
		tx = tx.Where(billingGroupColumn()+" = ?", filter.UseGroup)
	}
	if filter.Type != 0 {
		tx = tx.Where("type = ?", filter.Type)
	}
	if filter.RequestId != "" {
		tx = tx.Where("request_id = ?", filter.RequestId)
	}
	return tx
}

// ListUserLogsForBilling 取当月明细的一页，按时间正序。
//
// 正序与页面相反：页面最近在前，账单从月初读起。tie-break 有两种：
// 普通日志库用 (created_at, id)，ClickHouse 用 (created_at, request_id)，
// 因为那边 logs.id 恒为 0，拿它做游标会让「下一页」永远停在第一页。
// 模式由游标自己带着走（RequestId 非空即 request_id 模式），翻页中途换库也不会错行。
func ListUserLogsForBilling(ctx context.Context, userId int, start, end int64, filter BillingLineFilter, cursor BillingLineCursor, limit int) ([]*Log, error) {
	if limit <= 0 || limit > BillingLinePageSizeMax+1 {
		limit = BillingLinePageSizeMax + 1
	}
	tx := LOG_DB.WithContext(ctx).Model(&Log{}).
		Where("user_id = ? AND created_at >= ? AND created_at < ? AND type IN ?",
			userId, start, end, billingConsumeTypes())
	tx = applyBillingLineFilter(tx, filter)
	byRequestId := cursor.RequestId != "" ||
		(!cursor.Valid && common.UsingLogDatabase(common.DatabaseTypeClickHouse))
	if cursor.Valid {
		if byRequestId {
			tx = tx.Where("(created_at > ? OR (created_at = ? AND request_id > ?))",
				cursor.CreatedAt, cursor.CreatedAt, cursor.RequestId)
		} else {
			tx = tx.Where("(created_at > ? OR (created_at = ? AND id > ?))",
				cursor.CreatedAt, cursor.CreatedAt, cursor.Id)
		}
	}
	order := "created_at ASC, id ASC"
	if byRequestId {
		order = "created_at ASC, request_id ASC"
	}
	rows := make([]*Log, 0, limit)
	// 与 streamLogs 同理：Omit 会触发逐列枚举，只读的 channel_name 必须一起排除，
	// 否则 ClickHouse 直接报列不存在。
	err := tx.Omit("content", "channel_name").Order(order).Limit(limit).Find(&rows).Error
	return rows, err
}

// StreamUserLogsForBilling 按时间正序把整月日志交给 fn，供当月导出使用。
func StreamUserLogsForBilling(ctx context.Context, userId int, start, end int64, filter BillingLineFilter, fn func(*Log) error) error {
	tx := LOG_DB.WithContext(ctx).Model(&Log{}).
		Where("user_id = ? AND created_at >= ? AND created_at < ? AND type IN ?",
			userId, start, end, billingConsumeTypes())
	tx = applyBillingLineFilter(tx, filter)
	// 与 StreamUserLogs 不同，这里不改写 id、也不接受「最新在前」的改写：
	// 账单从月初读起，游标就是真实 (created_at, request_id)。
	return streamBillingLogs(tx, "logs.created_at ASC, logs.request_id ASC", fn)
}

// streamBillingLogs 严格按调用方给的 order 逐行读日志，不做「最新在前」的改写。
//
// 不能复用 model/log.go 的 streamLogs：它在 ClickHouse 下无条件把 order 换成
// 「最新在前」，而账单明细要的是月初到月末，倒序会让明细表和汇总表读起来是两个
// 方向（生产 0.0.214 就是这样）。把遍历放在这里，上游那个函数一行都不用动。
func streamBillingLogs(tx *gorm.DB, order string, fn func(*Log) error) error {
	// 与 streamLogs 同理：Omit 会让 GORM 从「SELECT *」切换成逐列枚举，只读字段
	// channel_name 也会进列清单，而 ClickHouse 的 logs 表没有这一列。
	rows, err := tx.Omit("content", "channel_name").Model(&Log{}).Order(order).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var log Log
		if err := LOG_DB.ScanRows(rows, &log); err != nil {
			return err
		}
		if err := fn(&log); err != nil {
			return err
		}
	}
	return rows.Err()
}

// SummarizeUserLogsForBilling 按 (model, group) 汇总当月用量。
//
// 输入 token 逐行 clamp 到 0 再求和，与明细的展示口径一致；先 SUM 再 clamp
// 会让个别异常行抵消其他行的用量，对不上账。
func SummarizeUserLogsForBilling(ctx context.Context, userId int, start, end int64) ([]BillingSummaryRow, error) {
	read := billingJSONInt("cache_tokens")
	write := billingJSONInt("cache_write_tokens")
	group := billingGroupColumn()
	consume, errorType, refund := strconv.Itoa(LogTypeConsume), strconv.Itoa(LogTypeError), strconv.Itoa(LogTypeRefund)

	inner := LOG_DB.WithContext(ctx).Table("logs").
		Select(`model_name AS model_name,
			`+group+` AS use_group,
			type AS type,
			quota AS quota,
			completion_tokens AS output_tokens,
			`+read+` AS cache_read_tokens,
			`+write+` AS cache_write_tokens,
			CASE WHEN prompt_tokens - `+read+` - `+write+` < 0 THEN 0
				ELSE prompt_tokens - `+read+` - `+write+` END AS input_tokens`).
		Where("user_id = ? AND created_at >= ? AND created_at < ? AND type IN ?",
			userId, start, end, billingConsumeTypes())

	rows := make([]BillingSummaryRow, 0, 16)
	err := LOG_DB.WithContext(billingAggregateContext(ctx)).Table("(?) AS billing_draft", inner).
		Select(`model_name,
			use_group,
			SUM(CASE WHEN type = ` + consume + ` THEN 1 ELSE 0 END) AS requests,
			SUM(CASE WHEN type = ` + errorType + ` THEN 1 ELSE 0 END) AS errors,
			SUM(CASE WHEN type = ` + refund + ` THEN 1 ELSE 0 END) AS refunds,
			SUM(input_tokens) AS input_tokens,
			SUM(cache_read_tokens) AS cache_read_tokens,
			SUM(cache_write_tokens) AS cache_write_tokens,
			SUM(output_tokens) AS output_tokens,
			SUM(CASE WHEN type = ` + refund + ` THEN -quota ELSE quota END) AS quota`).
		Group("model_name, use_group").
		Scan(&rows).Error
	// 排序放在 Go 里：ClickHouse 不接受 ORDER BY 里再写一遍 SUM(CASE ...)
	// —— 它把 SELECT 的聚合别名当成中间结果，报「aggregate inside aggregate」。
	// 汇总行数是模型 × 分组，几十行，Go 排序比为了方言一致再拼一段 SQL 便宜。
	sortBillingSummary(rows)
	return rows, err
}

// BillingUserTotal 是一个用户在一个账期内的实时合计，供管理员的全站视图使用。
type BillingUserTotal struct {
	UserId   int
	Requests int64
	Errors   int64
	Refunds  int64
	Quota    int64
}

// SummarizeUserLogsByUser 按用户汇总一个账期。当月没有出账快照，管理员要看到
// 「谁用了多少」只能实时聚合；一次 GROUP BY 回几十到几百行，比逐用户查便宜得多。
// 同样是 ClickHouse 上会撞内存的一组，聚合前先套上同一份内存策略。
func SummarizeUserLogsByUser(ctx context.Context, start, end int64) ([]BillingUserTotal, error) {
	consume, errorType, refund := strconv.Itoa(LogTypeConsume), strconv.Itoa(LogTypeError), strconv.Itoa(LogTypeRefund)
	rows := make([]BillingUserTotal, 0, 64)
	err := LOG_DB.WithContext(billingAggregateContext(ctx)).Table("logs").
		Select(`user_id AS user_id,
			SUM(CASE WHEN type = `+consume+` THEN 1 ELSE 0 END) AS requests,
			SUM(CASE WHEN type = `+errorType+` THEN 1 ELSE 0 END) AS errors,
			SUM(CASE WHEN type = `+refund+` THEN 1 ELSE 0 END) AS refunds,
			SUM(CASE WHEN type = `+refund+` THEN -quota ELSE quota END) AS quota`).
		Where("created_at >= ? AND created_at < ? AND type IN ?", start, end, billingConsumeTypes()).
		Group("user_id").
		Scan(&rows).Error
	return rows, err
}

// BillingUsernames 批量取用户名。删除过的用户查不到，调用方回落成 user#id。
func BillingUsernames(ids []int) (map[int]string, error) {
	names := make(map[int]string, len(ids))
	if len(ids) == 0 {
		return names, nil
	}
	type row struct {
		Id       int
		Username string
	}
	rows := make([]row, 0, len(ids))
	if err := DB.Model(&User{}).Select("id, username").Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, item := range rows {
		names[item.Id] = item.Username
	}
	return names, nil
}
