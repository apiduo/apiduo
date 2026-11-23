package service

import (
	"context"
	"slices"
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

// BillingUserSummaryRow 是管理员全站视图的一行：一个用户一个账期。
type BillingUserSummaryRow struct {
	UserId   int
	Username string
	Status   string
	Amount   string
	Quota    int64
	Requests int
	Errors   int
	Refunds  int
	IssuedAt int64
	DueAt    int64
	Currency string
	IsIssued bool
}

// LoadBillingUserSummaries 返回一个账期下所有用户的账单行。
//
// 已出账读快照；未出账（当月，或历史漏跑）按用户实时聚合，行上带 IsIssued=false。
// 管理员的全站视图因此不会因为「还没到 1 号」而空着 —— 那正是它最该能回答
// 「这个月谁用得多」的时候。
func LoadBillingUserSummaries(ctx context.Context, period, keyword string) ([]BillingUserSummaryRow, bool, error) {
	statements, err := model.ListBillingStatementsByPeriod(period)
	if err != nil {
		return nil, false, err
	}
	if len(statements) > 0 {
		rows := make([]BillingUserSummaryRow, 0, len(statements))
		for _, statement := range statements {
			rows = append(rows, BillingUserSummaryRow{
				UserId:   statement.UserId,
				Username: statement.Username,
				Status:   statement.Status,
				Amount:   statement.TotalAmount,
				Quota:    statement.TotalQuota,
				Requests: statement.Requests,
				Errors:   statement.Errors,
				Refunds:  statement.Refunds,
				IssuedAt: statement.IssuedAt,
				DueAt:    statement.DueAt,
				Currency: statement.Currency,
				IsIssued: true,
			})
		}
		return filterBillingUserRows(rows, keyword), false, nil
	}

	start, end, err := BillingPeriodBounds(period)
	if err != nil {
		return nil, false, err
	}
	totals, err := model.SummarizeUserLogsByUser(ctx, start, end)
	if err != nil {
		return nil, false, err
	}
	userIds := make([]int, 0, len(totals))
	for _, total := range totals {
		userIds = append(userIds, total.UserId)
	}
	names, err := model.BillingUsernames(userIds)
	if err != nil {
		// 用户名只影响展示：users 表查不到（迁移中、只读副本）就回落 user#id，
		// 不能让管理员的全站视图因此 500。
		names = map[int]string{}
	}

	snapshot := CurrentBillingSnapshot()
	rows := make([]BillingUserSummaryRow, 0, len(totals))
	for _, total := range totals {
		// 只有错误行的用户不出账：与 BuildBillingStatement 的候选集合一致。
		// 放在 Go 里过滤而不是 HAVING —— ClickHouse 对聚合表达式出现在
		// HAVING/ORDER BY 里很挑，几十行数据不值得为它冒一次方言风险。
		if total.Requests+total.Refunds == 0 {
			continue
		}
		username := names[total.UserId]
		if username == "" {
			username = "user#" + strconv.Itoa(total.UserId)
		}
		// 请求数含失败行，与账单头的口径一致：合计等于各行相加。
		rows = append(rows, BillingUserSummaryRow{
			UserId:   total.UserId,
			Username: username,
			Status:   model.BillingStatusDraft,
			Amount:   snapshot.AmountOf(total.Quota),
			Quota:    total.Quota,
			Requests: int(total.Requests + total.Errors),
			Errors:   int(total.Errors),
			Refunds:  int(total.Refunds),
			Currency: snapshot.Currency,
			IsIssued: false,
		})
	}
	sortBillingUserRows(rows)
	return filterBillingUserRows(rows, keyword), true, nil
}

// sortBillingUserRows 按金额倒序、用户 id 正序：运营先看谁贡献最多。
func sortBillingUserRows(rows []BillingUserSummaryRow) {
	slices.SortStableFunc(rows, func(a, b BillingUserSummaryRow) int {
		if a.Quota != b.Quota {
			if a.Quota > b.Quota {
				return -1
			}
			return 1
		}
		return a.UserId - b.UserId
	})
}

// filterBillingUserRows 支持用户名片段与完整用户 id。
// 两类关键词是运营最常用的两种找法：记得名字，或者手里只有 id。
func filterBillingUserRows(rows []BillingUserSummaryRow, keyword string) []BillingUserSummaryRow {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return rows
	}
	lower := strings.ToLower(keyword)
	matched := make([]BillingUserSummaryRow, 0, len(rows))
	for _, row := range rows {
		if strings.Contains(strings.ToLower(row.Username), lower) ||
			strconv.Itoa(row.UserId) == keyword {
			matched = append(matched, row)
		}
	}
	return matched
}
