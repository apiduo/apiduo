package service

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

// 出账：把一个用户一个自然月的日志折成一份账单。
//
// 关键约束：金额只能在出账这一刻算一次，并连同 quota_per_unit 与结算汇率一起落库。
// 之后无论汇率怎么变，账单金额都不再变化 —— 这是账单区别于「用量报表」的地方。

const (
	billingZoneOffset = 8 * 3600 // Asia/Shanghai，无夏令时
	billingLineBatch  = 1000     // 每批写库行数
	billingDueDays    = 30       // 默认账期，后续可由合同覆盖
)

// BillingPeriodBounds 返回账期 [start, end) 的 Unix 秒。按东八区自然月切，
// 不能用 UTC，否则每月 1 日凌晨的账会错 8 小时。
func BillingPeriodBounds(period string) (int64, int64, error) {
	start, err := time.ParseInLocation("2006-01", period, time.FixedZone("CST", billingZoneOffset))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid billing period %q, want YYYY-MM", period)
	}
	return start.Unix(), start.AddDate(0, 1, 0).Unix(), nil
}

// CurrentBillingPeriod 返回当前时刻所在的账期。
func CurrentBillingPeriod(now time.Time) string {
	return now.In(time.FixedZone("CST", billingZoneOffset)).Format("2006-01")
}

// BillingSnapshot 是一次出账用的计价口径。
type BillingSnapshot struct {
	QuotaPerUnit float64
	SettleRate   float64
	Currency     string
}

// CurrentBillingSnapshot 读取当前配置。调用方应在出账开始时取一次并贯穿整个过程，
// 避免中途被管理员改动导致同一份账单里两种汇率。
func CurrentBillingSnapshot() BillingSnapshot {
	return BillingSnapshot{
		QuotaPerUnit: common.QuotaPerUnit,
		SettleRate:   operation_setting.USDExchangeRate,
		Currency:     "CNY",
	}
}

// amountOf 把带符号的 quota 折成金额，保留 6 位小数。用字符串落库，避免浮点误差累积。
func (s BillingSnapshot) amountOf(quota int64) string {
	if s.QuotaPerUnit == 0 {
		return "0.000000"
	}
	amount := float64(quota) / s.QuotaPerUnit * s.SettleRate
	return strconv.FormatFloat(amount, 'f', 6, 64)
}

// 日志里 other 的公开字段，账单只取缓存与任务两类。
type billingLogOther struct {
	CacheTokens      int    `json:"cache_tokens"`
	CacheWriteTokens int    `json:"cache_write_tokens"`
	TaskId           string `json:"task_id"`
}

type billingTotals struct {
	requests   int
	errors     int
	refunds    int
	quota      int64
	hasConsume bool
}

// BuildBillingStatement 为 userId 生成 period 的账单。
//
// 幂等：同一 (period, user_id) 只允许一份；已存在则原样返回，不重算。
// 未出账的当月不落库 —— 当月是实时聚合，没有快照可言。
func BuildBillingStatement(ctx context.Context, period string, userId int, username string, snapshot BillingSnapshot) (*model.BillingStatement, bool, error) {
	start, end, err := BillingPeriodBounds(period)
	if err != nil {
		return nil, false, err
	}

	if existing, found, err := model.FindBillingStatement(period, userId); err != nil {
		return nil, false, err
	} else if found {
		return existing, false, nil
	}

	now := time.Now().Unix()
	statement := &model.BillingStatement{
		Period:       period,
		UserId:       userId,
		Username:     username,
		Status:       model.BillingStatusIssued,
		PeriodStart:  start,
		PeriodEnd:    end,
		IssuedAt:     now,
		DueAt:        end + billingDueDays*86400,
		Currency:     snapshot.Currency,
		QuotaPerUnit: snapshot.QuotaPerUnit,
		SettleRate:   snapshot.SettleRate,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := model.DB.Create(statement).Error; err != nil {
		return nil, false, err
	}

	// 出账中途失败要把半份账单清掉，否则下次重跑会撞唯一键，用户也看不到完整数据。
	committed := false
	defer func() {
		if committed {
			return
		}
		model.DB.Where("statement_id = ?", statement.Id).Delete(&model.BillingStatementLine{})
		model.DB.Delete(statement)
	}()

	batch := make([]*model.BillingStatementLine, 0, billingLineBatch)
	totals := billingTotals{}

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := model.InsertBillingStatementLines(batch); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}

	err = model.StreamUserLogsForBilling(ctx, userId, start, end, func(log *model.Log) error {
		var other billingLogOther
		if log.Other != "" {
			_ = common.UnmarshalJsonStr(log.Other, &other)
		}

		// 账单里统一用带符号的数：退款是负数。日志里退款存正数，
		// 下游不必再判断 type，加总即可。
		signedQuota := int64(log.Quota)
		if log.Type == model.LogTypeRefund {
			signedQuota = -signedQuota
		}

		inputTokens := log.PromptTokens - other.CacheTokens - other.CacheWriteTokens
		if inputTokens < 0 {
			inputTokens = 0
		}

		batch = append(batch, &model.BillingStatementLine{
			StatementId:      statement.Id,
			UserId:           userId,
			RequestId:        log.RequestId,
			CreatedAt:        log.CreatedAt,
			Type:             log.Type,
			TokenName:        log.TokenName,
			ModelName:        log.ModelName,
			UseGroup:         log.Group,
			InputTokens:      inputTokens,
			CacheReadTokens:  other.CacheTokens,
			CacheWriteTokens: other.CacheWriteTokens,
			OutputTokens:     log.CompletionTokens,
			Quota:            signedQuota,
			Amount:           snapshot.amountOf(signedQuota),
			TaskId:           other.TaskId,
		})

		switch log.Type {
		case model.LogTypeConsume:
			totals.requests++
			totals.quota += signedQuota
			totals.hasConsume = true
		case model.LogTypeError:
			totals.requests++
			totals.errors++
		case model.LogTypeRefund:
			totals.refunds++
			totals.quota += signedQuota
		}

		if len(batch) >= billingLineBatch {
			return flush()
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if err := flush(); err != nil {
		return nil, false, err
	}

	statement.Requests = totals.requests
	statement.Errors = totals.errors
	statement.Refunds = totals.refunds
	statement.TotalQuota = totals.quota
	statement.TotalAmount = snapshot.amountOf(totals.quota)
	if err := model.DB.Model(statement).
		Select("requests", "errors", "refunds", "total_quota", "total_amount", "updated_at").
		Updates(statement).Error; err != nil {
		return nil, false, err
	}

	committed = true
	return statement, true, nil
}
