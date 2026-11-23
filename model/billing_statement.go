package model

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// 账单：账期头、明细行、导出文件。
//
// 三张表的关系是「一份月账单 + 它的逐行明细 + 它的归档文件」。汇总不单独落表：
// 明细每行的 amount 在出账时冻结，按 模型 × 分组 加总就是汇总，不会随配置变化。

// 账单状态。issued 之后金额冻结；void 表示作废重开，不原地改数。
const (
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
	Endpoint  string `json:"endpoint" gorm:"type:varchar(64);default:''"`

	InputTokens      int `json:"input_tokens" gorm:"default:0"`
	CacheReadTokens  int `json:"cache_read_tokens" gorm:"default:0"`
	CacheWriteTokens int `json:"cache_write_tokens" gorm:"default:0"`
	OutputTokens     int `json:"output_tokens" gorm:"default:0"`

	Quota  int64  `json:"quota" gorm:"bigint;default:0"`
	Amount string `json:"amount" gorm:"type:varchar(32);default:'0'"`
	TaskId string `json:"task_id" gorm:"type:varchar(64);default:''"`
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

// DeleteBillingStatementLines 在重跑出账前清掉旧明细。
func DeleteBillingStatementLines(statementId int) error {
	return DB.Where("statement_id = ?", statementId).Delete(&BillingStatementLine{}).Error
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
	if limit <= 0 || limit > 200 {
		limit = 50
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

// GetBillingStatementExport 取某个投影的归档文件记录。
func GetBillingStatementExport(statementId int, projection string) (*BillingStatementExport, error) {
	var export BillingStatementExport
	err := DB.Where("statement_id = ? AND projection = ?", statementId, projection).First(&export).Error
	if err != nil {
		return nil, err
	}
	return &export, nil
}

// StreamUserLogsForBilling 按时间正序把一段账期内的消费、错误、退款日志交给 fn。
// 与导出的流式读取分开：这里要的是原始 other（含缓存 token），不做可见性裁剪。
func StreamUserLogsForBilling(ctx context.Context, userId int, start, end int64, fn func(*Log) error) error {
	tx := LOG_DB.WithContext(ctx).
		Where("user_id = ? AND created_at >= ? AND created_at < ? AND type IN ?",
			userId, start, end, []int{LogTypeConsume, LogTypeError, LogTypeRefund})
	return streamLogs(tx, "logs.created_at ASC, logs.id ASC", fn)
}
