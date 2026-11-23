package model

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

// 账单：账期头、明细行、导出文件。
//
// 三张表的关系是「一份月账单 + 它的逐行明细 + 它的归档文件」。汇总不单独落表：
// 明细每行的 amount 在出账时冻结，按 模型 × 分组 加总就是汇总，不会随配置变化。

// 账单状态。issued 之后金额冻结；void 表示作废重开，不原地改数。
const (
	// BillingStatusDraft 只出现在实时视图里，不落库：当月账单没有快照可言，
	// 落一份会立刻过期的明细既贵又没有凭证价值。
	BillingStatusDraft  = "draft"
	BillingStatusIssued = "issued"
	BillingStatusPaid   = "paid"
	BillingStatusVoid   = "void"
)

// BillingStatement 是一个用户一个自然月的账单头。
type BillingStatement struct {
	Id       int    `json:"id" gorm:"primaryKey"`
	Period   string `json:"period" gorm:"type:varchar(7);not null;uniqueIndex:idx_billing_statement_period_user,priority:1"`
	UserId   int    `json:"user_id" gorm:"not null;uniqueIndex:idx_billing_statement_period_user,priority:2"`
	Username string `json:"username" gorm:"type:varchar(64);default:'';index"`

	Status      string `json:"status" gorm:"type:varchar(16);index;default:'issued'"`
	PeriodStart int64  `json:"period_start" gorm:"bigint;not null"`
	PeriodEnd   int64  `json:"period_end" gorm:"bigint;not null"`
	IssuedAt    int64  `json:"issued_at" gorm:"bigint;index"`
	DueAt       int64  `json:"due_at" gorm:"bigint;default:0"`

	Currency    string `json:"currency" gorm:"type:varchar(8);default:'CNY'"`
	TotalAmount string `json:"total_amount" gorm:"type:varchar(32);default:'0'"`
	TotalQuota  int64  `json:"total_quota" gorm:"bigint;default:0"`
	Requests    int    `json:"requests" gorm:"default:0"`
	Errors      int    `json:"errors" gorm:"default:0"`
	Refunds     int    `json:"refunds" gorm:"default:0"`

	// 出账那一刻的计价口径。只存 quota 而不存这两个数，改一次汇率历史账单就变形。
	// 不写 type:double：PostgreSQL 没有 double 这个类型名，交给 GORM 按 float64 映射
	// （MySQL double / PG double precision / SQLite real）。
	QuotaPerUnit float64 `json:"quota_per_unit" gorm:"default:0"`
	SettleRate   float64 `json:"settle_rate" gorm:"default:0"`

	CreatedAt int64 `json:"created_at" gorm:"bigint;index"`
	UpdatedAt int64 `json:"updated_at" gorm:"bigint;default:0"`
}

func (BillingStatement) TableName() string { return "billing_statements" }

// BillingStatementLine 是一条逐请求明细，金额在出账时写定。
type BillingStatementLine struct {
	Id          int    `json:"id" gorm:"primaryKey"`
	StatementId int    `json:"statement_id" gorm:"index:idx_billing_line_statement,priority:1;not null"`
	UserId      int    `json:"user_id" gorm:"index"`
	RequestId   string `json:"request_id" gorm:"type:varchar(64);index;default:''"`
	CreatedAt   int64  `json:"created_at" gorm:"index:idx_billing_line_statement,priority:2"`
	Type        int    `json:"type" gorm:"index"`

	TokenName string `json:"token_name" gorm:"type:varchar(128);default:''"`
	ModelName string `json:"model_name" gorm:"type:varchar(128);index;default:''"`
	UseGroup  string `json:"use_group" gorm:"type:varchar(64);index;default:''"`
	// 端点必须放得下 Gemini 的流式路径：
	// /v1beta/models/<model>:streamGenerateContent 实测最长 71 字符，varchar(64)
	// 会让该用户的整份账单在插入时回滚（Error 1406），出账任务每 30 分钟重试一次、
	// 每次都失败。全站历史最大 71，这里给到 191 留余量。
	Endpoint string `json:"endpoint" gorm:"type:varchar(191);default:''"`

	InputTokens      int `json:"input_tokens" gorm:"default:0"`
	CacheReadTokens  int `json:"cache_read_tokens" gorm:"default:0"`
	CacheWriteTokens int `json:"cache_write_tokens" gorm:"default:0"`
	OutputTokens     int `json:"output_tokens" gorm:"default:0"`

	Quota  int64  `json:"quota" gorm:"bigint;default:0"`
	Amount string `json:"amount" gorm:"type:varchar(32);default:'0'"`
	TaskId string `json:"task_id" gorm:"type:varchar(64);default:''"`
	// 单请求耗时。客户核对「这一行为什么慢」只需要它，不涉及渠道与上游。
	UseTimeMs int `json:"use_time_ms" gorm:"default:0"`
}

func (BillingStatementLine) TableName() string { return "billing_statement_lines" }

// BillingStatementExport 是出账时生成好的文件。同一账期两个投影各一份：
// 管理员看得到 quota 与倍率，客户端只能看结算金额。
type BillingStatementExport struct {
	Id          int    `json:"id" gorm:"primaryKey"`
	StatementId int    `json:"statement_id" gorm:"not null;uniqueIndex:idx_billing_export_unique,priority:1"`
	Projection  string `json:"projection" gorm:"type:varchar(16);not null;uniqueIndex:idx_billing_export_unique,priority:2"`
	ObjectKey   string `json:"object_key" gorm:"type:varchar(255);not null"`
	Bytes       int64  `json:"bytes" gorm:"bigint;default:0"`
	Rows        int64  `json:"rows" gorm:"bigint;default:0"`
	Sha256      string `json:"sha256" gorm:"type:varchar(64);default:''"`
	CreatedAt   int64  `json:"created_at" gorm:"bigint"`
}

func (BillingStatementExport) TableName() string { return "billing_statement_exports" }

// GetBillingStatement 按账期与用户取一份账单。
func GetBillingStatement(period string, userId int) (*BillingStatement, error) {
	var statement BillingStatement
	err := DB.Where("period = ? AND user_id = ?", period, userId).First(&statement).Error
	if err != nil {
		return nil, err
	}
	return &statement, nil
}

// FindBillingStatement 与 GetBillingStatement 相同，但没找到时返回 nil 而不是错误。
func FindBillingStatement(period string, userId int) (*BillingStatement, bool, error) {
	statement, err := GetBillingStatement(period, userId)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return statement, true, nil
}

// ListBillingStatements 返回一个用户最近若干账期，按账期倒序。
func ListBillingStatements(userId int, limit int) ([]*BillingStatement, error) {
	if limit <= 0 || limit > 60 {
		limit = 24
	}
	rows := make([]*BillingStatement, 0, limit)
	err := DB.Where("user_id = ?", userId).Order("period DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

// DeleteBillingStatementLines 在重跑出账前清掉旧明细，返回删掉的行数。
func DeleteBillingStatementLines(statementId int) (int64, error) {
	tx := DB.Where("statement_id = ?", statementId).Delete(&BillingStatementLine{})
	return tx.RowsAffected, tx.Error
}

// ListBillingStatementExports 列出一份账单的所有归档文件（两个投影各一条）。
// 清理时要顺着它把磁盘上的文件一起删掉，只删记录会留下永远没人取的孤儿文件。
func ListBillingStatementExports(statementId int) ([]*BillingStatementExport, error) {
	var exports []*BillingStatementExport
	err := DB.Where("statement_id = ?", statementId).Find(&exports).Error
	return exports, err
}

// InsertBillingStatementLines 批量写入明细。单批超过 500 行时由调用方切分。
func InsertBillingStatementLines(lines []*BillingStatementLine) error {
	if len(lines) == 0 {
		return nil
	}
	return DB.CreateInBatches(lines, 500).Error
}

// CountBillingStatementLines 返回明细行数。
func CountBillingStatementLines(statementId int) (int64, error) {
	var total int64
	err := DB.Model(&BillingStatementLine{}).Where("statement_id = ?", statementId).Count(&total).Error
	return total, err
}

// BillingLineFilter 是明细页的筛选条件。零值表示不限。
type BillingLineFilter struct {
	ModelName string
	UseGroup  string
	Type      int
	RequestId string
}

// ListBillingStatementLines 按时间正序取一页明细。offset 分页对本场景足够：
// 单账期行数有界，且翻页不频繁。
func ListBillingStatementLines(statementId int, filter BillingLineFilter, offset, limit int) ([]*BillingStatementLine, error) {
	if limit <= 0 || limit > BillingLinePageSizeMax {
		limit = BillingLinePageSizeMax
	}
	if offset < 0 {
		offset = 0
	}
	query := DB.Where("statement_id = ?", statementId)
	if filter.ModelName != "" {
		query = query.Where("model_name = ?", filter.ModelName)
	}
	if filter.UseGroup != "" {
		query = query.Where("use_group = ?", filter.UseGroup)
	}
	if filter.Type != 0 {
		query = query.Where("type = ?", filter.Type)
	}
	if filter.RequestId != "" {
		query = query.Where("request_id = ?", filter.RequestId)
	}
	rows := make([]*BillingStatementLine, 0, limit)
	err := query.Order("created_at ASC, id ASC").Offset(offset).Limit(limit).Find(&rows).Error
	return rows, err
}

// ListBillingStatementLinesAfter 按 (created_at, id) 取一页明细，与导出用同一套
// keyset 语义。页面翻页因此不会随行数变慢，也不会因为期间有新写入而错行。
func ListBillingStatementLinesAfter(statementId int, filter BillingLineFilter, cursor BillingLineCursor, limit int) ([]*BillingStatementLine, error) {
	// 与当月明细同一个上界：调用方会多取一行判断有没有下一页，
	// 所以这里必须允许 BillingLinePageSizeMax+1，否则 page_size=200 会被打回 50。
	if limit <= 0 || limit > BillingLinePageSizeMax+1 {
		limit = BillingLinePageSizeMax + 1
	}
	query := DB.Where("statement_id = ?", statementId)
	if filter.ModelName != "" {
		query = query.Where("model_name = ?", filter.ModelName)
	}
	if filter.UseGroup != "" {
		query = query.Where("use_group = ?", filter.UseGroup)
	}
	if filter.Type != 0 {
		query = query.Where("type = ?", filter.Type)
	}
	if filter.RequestId != "" {
		query = query.Where("request_id = ?", filter.RequestId)
	}
	if cursor.Valid {
		query = query.Where("(created_at > ? OR (created_at = ? AND id > ?))", cursor.CreatedAt, cursor.CreatedAt, cursor.Id)
	}
	rows := make([]*BillingStatementLine, 0, limit)
	err := query.Order("created_at ASC, id ASC").Limit(limit).Find(&rows).Error
	return rows, err
}

// ListBillingStatementsByPeriod 返回一个账期下所有用户的账单，供管理员全站视图。
// 按金额倒序：运营先看谁贡献最多。
func ListBillingStatementsByPeriod(period string) ([]*BillingStatement, error) {
	rows := make([]*BillingStatement, 0, 32)
	err := DB.Where("period = ? AND status <> ?", period, BillingStatusVoid).
		Order("total_quota DESC, user_id ASC").
		Find(&rows).Error
	return rows, err
}

// GetBillingStatementExport 取某个投影的归档文件记录。
func GetBillingStatementExport(statementId int, projection string) (*BillingStatementExport, error) {
	var export BillingStatementExport
	err := DB.Where("statement_id = ? AND projection = ?", statementId, projection).First(&export).Error
	if err != nil {
		return nil, err
	}
	return &export, nil
}

// BillingSummaryRow 是 (model, group) 的聚合，一次 GROUP BY 得出。
//
// 金额不在这里算：它由 quota 合计按账单快照折算，与 statements.total_amount
// 同源。逐行 decimal 相加会引入 1e-6 级累计差，验收要求「Σ明细 == 总额」时
// 反而说不清。
type BillingSummaryRow struct {
	ModelName        string
	UseGroup         string
	Requests         int64
	Errors           int64
	Refunds          int64
	InputTokens      int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	OutputTokens     int64
	Quota            int64
}

// sortBillingSummary 按金额倒序、模型名正序排。金额为负的组（只有退款的）
// 排在最后，与运营的直觉一致：先看谁贡献最多。
func sortBillingSummary(rows []BillingSummaryRow) {
	slices.SortStableFunc(rows, func(a, b BillingSummaryRow) int {
		if a.Quota != b.Quota {
			if a.Quota > b.Quota {
				return -1
			}
			return 1
		}
		return strings.Compare(a.ModelName, b.ModelName)
	})
}

// SummarizeBillingStatementLines 按 (model, group) 汇总一份账单的明细。
func SummarizeBillingStatementLines(statementId int) ([]BillingSummaryRow, error) {
	return summarizeBillingLines(DB.Model(&BillingStatementLine{}).
		Where("statement_id = ?", statementId))
}

// summarizeBillingLines 是已出账账单与当月实时视图共用的聚合语句。
// 入参是已经带好 where 的查询，避免两条路径各写一份 CASE WHEN。
func summarizeBillingLines(query *gorm.DB) ([]BillingSummaryRow, error) {
	consume, errorType, refund := strconv.Itoa(LogTypeConsume), strconv.Itoa(LogTypeError), strconv.Itoa(LogTypeRefund)
	rows := make([]BillingSummaryRow, 0, 16)
	err := query.
		Select(`model_name,
			use_group,
			SUM(CASE WHEN type = ` + consume + ` THEN 1 ELSE 0 END) AS requests,
			SUM(CASE WHEN type = ` + errorType + ` THEN 1 ELSE 0 END) AS errors,
			SUM(CASE WHEN type = ` + refund + ` THEN 1 ELSE 0 END) AS refunds,
			SUM(input_tokens) AS input_tokens,
			SUM(cache_read_tokens) AS cache_read_tokens,
			SUM(cache_write_tokens) AS cache_write_tokens,
			SUM(output_tokens) AS output_tokens,
			SUM(quota) AS quota`).
		Group("model_name, use_group").
		Scan(&rows).Error
	sortBillingSummary(rows)
	return rows, err
}

// BillingLineCursor 是明细的 keyset 游标，(created_at, id) 唯一确定一行的位置。
// Valid 为 false 表示从第一页开始；解析失败时调用方也应回落到第一页，
// 而不是把游标当成 0 时刻去扫全表。
type BillingLineCursor struct {
	Valid     bool
	CreatedAt int64
	Id        int
	// RequestId 是 ClickHouse 日志表上的 tie-break。那边没有可用的自增 id
	// （logs.id 恒为 0），同一秒的多行只能靠 request_id 定序。
	RequestId string
}

// BillingLinePageSizeMax 是当月明细一页的上限，内部多取的那一行不算在内。
const BillingLinePageSizeMax = 200

// BillingLineCursorFromLog 按日志库形态造游标：ClickHouse 落在 request_id 上，
// 其他库落在自增 id 上。查询条件与排序必须跟着同一个选择走，否则翻页会错行。
func BillingLineCursorFromLog(log *Log) BillingLineCursor {
	if log == nil {
		return BillingLineCursor{}
	}
	cursor := BillingLineCursor{Valid: true, CreatedAt: log.CreatedAt}
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		// 没有 request_id 的行在这套游标下无法定位「下一行」。宁可让翻页停在
		// 这里，也不要给出一个解析不了、会把读者送回第一页再循环的游标。
		// 实测 10,456,643 行里空 request_id 为 0，走到这里说明写入侧异常。
		if log.RequestId == "" {
			return BillingLineCursor{}
		}
		cursor.RequestId = log.RequestId
	} else {
		cursor.Id = log.Id
	}
	return cursor
}

// MigrateBillingStatementEndpointWidth 把 endpoint 从旧版的 varchar(64) 加宽到 191。
//
// 只靠 AutoMigrate 不够：类型同为 varchar，GORM 不会去比对长度，旧库就一直停在 64。
// 而 64 装不下 Gemini 的 `/v1beta/models/<model>:streamGenerateContent`（实测 71），
// 该用户的整份账单会在插入明细时以 Error 1406 回滚，出账任务每 30 分钟重试、每次必失败。
// 只在 MySQL 上做：其余方言由 AutoMigrate 直接建成长度够用的列。
func MigrateBillingStatementEndpointWidth() error {
	if !common.UsingMainDatabase(common.DatabaseTypeMySQL) {
		return nil
	}
	if !DB.Migrator().HasTable(&BillingStatementLine{}) {
		return nil
	}
	var length int64
	table := BillingStatementLine{}.TableName()
	if err := DB.Raw(`SELECT CHARACTER_MAXIMUM_LENGTH FROM information_schema.columns
		WHERE table_schema = DATABASE() AND table_name = ? AND column_name = 'endpoint'`, table).
		Scan(&length).Error; err != nil {
		return err
	}
	if length == 0 || length >= 191 {
		return nil
	}
	return DB.Exec("ALTER TABLE " + table + " MODIFY COLUMN endpoint varchar(191) NOT NULL DEFAULT ''").Error
}

// BillingStatementExistsForUser 回答「这个 user_id 有没有账单」。
//
// 代客查看时用它兜底：用户被删除后 users 表查不到名字，但已出账的账单还在，
// 不能因为查不到人就 404 —— 那会让运营没法核对历史账单。
//
// 必须同时看草稿月：当月还没出账，billing_statements 里根本没有行，只查那张表
// 会把「这个月有 60 万次请求、但用户已被删除」的老客户判成不存在（线上实测
// user_id=64/65 全部用户列表显示 user#64，点进详情却返回 User not found.）。
// start/end 是该账期的边界，传 0 表示不查用量维度。
//
// 用量维度只做存在性探测，不数总数：调用方要的是「有没有」，而 count 在服务端
// 内存吃紧时会被直接拒绝，把一次瞬时故障变成一个「查无此人」的结论。
func BillingStatementExistsForUser(ctx context.Context, userId int, start, end int64) (bool, error) {
	var count int64
	if err := DB.Model(&BillingStatement{}).Where("user_id = ?", userId).Count(&count).Error; err != nil {
		return false, err
	}
	if count > 0 {
		return true, nil
	}
	if start >= end {
		return false, nil
	}
	return BillingUserHasUsage(ctx, userId, start, end)
}

// StreamBillingStatementLines 按时间正序把全部明细分批交给 fn，供导出使用。
//
// 用 (created_at, id) keyset 而不是 offset：单账期几十万行时，offset 越翻
// 越慢，而 keyset 每页都走 idx_billing_line_statement 的前缀。
func StreamBillingStatementLines(ctx context.Context, statementId int, batch int, fn func([]*BillingStatementLine) error) error {
	if batch <= 0 || batch > 5000 {
		batch = 2000
	}
	var lastCreatedAt int64
	lastId := 0
	started := false
	for {
		rows := make([]*BillingStatementLine, 0, batch)
		tx := DB.WithContext(ctx).Where("statement_id = ?", statementId)
		if started {
			tx = tx.Where("(created_at > ? OR (created_at = ? AND id > ?))", lastCreatedAt, lastCreatedAt, lastId)
		}
		if err := tx.Order("created_at ASC, id ASC").Limit(batch).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		if err := fn(rows); err != nil {
			return err
		}
		last := rows[len(rows)-1]
		lastCreatedAt, lastId = last.CreatedAt, last.Id
		started = true
		if len(rows) < batch {
			return nil
		}
	}
}

// UpsertBillingStatementExport 登记归档文件。重跑出账会覆盖同名文件，
// 因此这里也是覆盖而不是报唯一键冲突。
func UpsertBillingStatementExport(row *BillingStatementExport) error {
	values := map[string]any{
		"object_key": row.ObjectKey,
		"bytes":      row.Bytes,
		"rows":       row.Rows,
		"sha256":     row.Sha256,
		"created_at": row.CreatedAt,
	}
	result := DB.Model(&BillingStatementExport{}).
		Where("statement_id = ? AND projection = ?", row.StatementId, row.Projection).
		Updates(values)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		return nil
	}
	return DB.Create(row).Error
}

// ListBillingCandidateUsers 返回账期内产生过消费或退款的用户，供出账任务遍历。
// 只有错误行的用户不出账：没有金额的账单没有对账价值。
func ListBillingCandidateUsers(ctx context.Context, start, end int64) ([]int, error) {
	rows := make([]int, 0, 64)
	err := LOG_DB.WithContext(ctx).Model(&Log{}).
		Where("created_at >= ? AND created_at < ? AND type IN ?", start, end, []int{LogTypeConsume, LogTypeRefund}).
		Distinct().
		Pluck("user_id", &rows).Error
	return rows, err
}
