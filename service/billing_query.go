package service

import (
	"context"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/model"
)

// 账单的读取入口：一个账期一件事，已出账走快照、未出账走实时。
//
// 页面与导出共用这里，所以「当月看到的数」与「下月出账后的数」算法完全一致，
// 差别只在时间范围。判断是否存在出账快照只有这一处，避免两边各写一遍
// 「issued 才读明细」的条件后逐渐漂移。

// LoadBillingReport 返回某账期的账单视图。已有快照读快照，否则实时聚合。
func LoadBillingReport(ctx context.Context, period string, userId int, username string, snapshot BillingSnapshot, projection BillingProjection) (*BillingReport, error) {
	statement, found, err := model.FindBillingStatement(period, userId)
	if err != nil {
		return nil, err
	}
	if found && statement.Status != model.BillingStatusVoid {
		return LoadIssuedBillingReport(ctx, statement, projection)
	}
	return LoadDraftBillingReport(ctx, period, userId, username, snapshot, projection)
}

// LoadBillingLinesPage 取一页明细。
//
// 多取一行只为了判断有没有下一页：这样前端拿到 next_cursor 为空就是真的到底，
// 不必再多发一次必然为空的请求。
func LoadBillingLinesPage(ctx context.Context, report *BillingReport, filter model.BillingLineFilter, cursor model.BillingLineCursor, limit int) ([]*model.BillingStatementLine, model.BillingLineCursor, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var rows []*model.BillingStatementLine
	if report.IsDraft {
		statement := report.Statement
		start, end := statement.PeriodStart, statement.PeriodEnd
		logs, queryErr := model.ListUserLogsForBilling(ctx, statement.UserId, start, end, filter, cursor, limit+1)
		if queryErr != nil {
			return nil, model.BillingLineCursor{}, queryErr
		}
		snapshot := BillingSnapshot{
			QuotaPerUnit: statement.QuotaPerUnit,
			SettleRate:   statement.SettleRate,
			Currency:     statement.Currency,
		}
		rows = make([]*model.BillingStatementLine, 0, len(logs))
		for _, log := range logs {
			line := billingLineFromLog(log, snapshot)
			// 当月视图没有行 id，用 (created_at, 日志 id) 继续翻页。
			line.Id = log.Id
			rows = append(rows, line)
		}
	} else {
		var err error
		rows, err = model.ListBillingStatementLinesAfter(report.Statement.Id, filter, cursor, limit+1)
		if err != nil {
			return nil, model.BillingLineCursor{}, err
		}
	}

	next := model.BillingLineCursor{}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next = model.BillingLineCursor{Valid: true, CreatedAt: last.CreatedAt, Id: last.Id}
	}
	return rows, next, nil
}

// ParseBillingLineCursor 解析前端回传的游标。格式不合法时回落到第一页，
// 而不是把它当成 0 时刻去扫全表。
func ParseBillingLineCursor(value string) model.BillingLineCursor {
	createdAtPart, idPart, ok := strings.Cut(value, "_")
	if !ok {
		return model.BillingLineCursor{}
	}
	createdAt, err := strconv.ParseInt(createdAtPart, 10, 64)
	if err != nil || createdAt <= 0 {
		return model.BillingLineCursor{}
	}
	id, err := strconv.Atoi(idPart)
	if err != nil || id <= 0 {
		return model.BillingLineCursor{}
	}
	return model.BillingLineCursor{Valid: true, CreatedAt: createdAt, Id: id}
}

// FormatBillingLineCursor 是 ParseBillingLineCursor 的逆操作。
func FormatBillingLineCursor(cursor model.BillingLineCursor) string {
	if !cursor.Valid {
		return ""
	}
	return strconv.FormatInt(cursor.CreatedAt, 10) + "_" + strconv.Itoa(cursor.Id)
}
