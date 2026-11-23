package service

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/QuantumNous/new-api/model"
)

// 一份账单的完整内容，与数据来源无关。
//
// 已出账的账期读 billing_statement_lines（快照，金额永不再变），当月读 logs
// （实时，金额随用量变化）。两者在页面、在导出文件里长得完全一样，区别只有
// Status 与 IsDraft —— 前端不需要为「当月」写第二套表格。
type BillingReport struct {
	Statement  *model.BillingStatement
	Projection BillingProjection
	Summary    []model.BillingSummaryRow
	// Lines 顺序回调整月明细。调用方一次只持有一批，文件多大都不攒在内存里。
	Lines       func(push func(*model.BillingStatementLine) error) error
	GeneratedAt int64
	IsDraft     bool
}

// BillingProjection 决定字段集与文件内容。
//
// 不是前端参数：由服务端按「操作者角色 + 是否代客」判定。客户投影少 quota、
// 分组、耗时这类内部字段，是为了一次误发不泄漏计费结构。
type BillingProjection string

const (
	BillingProjectionAdmin    BillingProjection = "admin"
	BillingProjectionCustomer BillingProjection = "customer"
)

// ParseBillingProjection 只接受两个已知值，避免拼错后静默降级到 admin。
func ParseBillingProjection(value string) (BillingProjection, bool) {
	switch BillingProjection(value) {
	case BillingProjectionAdmin:
		return BillingProjectionAdmin, true
	case BillingProjectionCustomer:
		return BillingProjectionCustomer, true
	}
	return BillingProjectionCustomer, false
}

// LoadIssuedBillingReport 读一份已出账账单。
func LoadIssuedBillingReport(ctx context.Context, statement *model.BillingStatement, projection BillingProjection) (*BillingReport, error) {
	summary, err := model.SummarizeBillingStatementLines(statement.Id)
	if err != nil {
		return nil, err
	}
	return &BillingReport{
		Statement:   statement,
		Projection:  projection,
		Summary:     summary,
		GeneratedAt: time.Now().Unix(),
		Lines: func(push func(*model.BillingStatementLine) error) error {
			return model.StreamBillingStatementLines(ctx, statement.Id, 2000, func(rows []*model.BillingStatementLine) error {
				for _, row := range rows {
					if err := push(row); err != nil {
						return err
					}
				}
				return nil
			})
		},
	}, nil
}

// NewDraftBillingStatement 造一个不落库的账单头。
//
// 明细分页只需要账期边界与计价快照，不需要汇总；把它单独拆出来，页面翻页
// 就不会每次都跑一遍 GROUP BY。
func NewDraftBillingStatement(period string, userId int, username string, snapshot BillingSnapshot) (*model.BillingStatement, error) {
	start, end, err := BillingPeriodBounds(period)
	if err != nil {
		return nil, err
	}
	return &model.BillingStatement{
		Period:       period,
		UserId:       userId,
		Username:     username,
		Status:       model.BillingStatusDraft,
		PeriodStart:  start,
		PeriodEnd:    end,
		Currency:     snapshot.Currency,
		QuotaPerUnit: snapshot.QuotaPerUnit,
		SettleRate:   snapshot.SettleRate,
	}, nil
}

// LoadDraftBillingReport 实时聚合一个尚未出账的账期。
//
// 不落库：当月每一分钟都在变，落一份明细既贵又立刻过期。汇总下推到数据库，
// 明细按页读，只有导出时才整月顺序扫一遍。
func LoadDraftBillingReport(ctx context.Context, period string, userId int, username string, snapshot BillingSnapshot, projection BillingProjection) (*BillingReport, error) {
	totals, err := NewDraftBillingStatement(period, userId, username, snapshot)
	if err != nil {
		return nil, err
	}
	summary, err := model.SummarizeUserLogsForBilling(ctx, userId, totals.PeriodStart, totals.PeriodEnd)
	if err != nil {
		return nil, err
	}
	for _, row := range summary {
		totals.Requests += int(row.Requests + row.Errors)
		totals.Errors += int(row.Errors)
		totals.Refunds += int(row.Refunds)
		totals.TotalQuota += row.Quota
	}
	totals.TotalAmount = snapshot.AmountOf(totals.TotalQuota)

	return &BillingReport{
		Statement:   totals,
		Projection:  projection,
		Summary:     summary,
		GeneratedAt: time.Now().Unix(),
		IsDraft:     true,
		Lines: func(push func(*model.BillingStatementLine) error) error {
			return model.StreamUserLogsForBilling(ctx, userId, totals.PeriodStart, totals.PeriodEnd, model.BillingLineFilter{}, func(log *model.Log) error {
				return push(billingLineFromLog(log, snapshot))
			})
		},
	}, nil
}

// BillingExportKey 是归档文件在 data/exports 下的相对路径。
//
// 固定三段：`billing/<账期>/<文件名>`。导出的下载端点是按段白名单校验的，
// 多一段就会被拒；账期与账单编号都进文件名，客户下载到的名字可直接归档。
func BillingExportKey(period string, statementId, userId int, projection BillingProjection) string {
	name := fmt.Sprintf("BILL-%s-u%d-%s.xlsx", period, userId, projection)
	if statementId <= 0 {
		// 当月没有账单编号，-draft 后缀让它与出账后的正式文件并存，
		// 客户可以拿两份对比。
		name = fmt.Sprintf("BILL-%s-u%d-draft-%s.xlsx", period, userId, projection)
	}
	return "billing/" + period + "/" + name
}

// BillingExportFileName 是给用户下载时看到的名字。
func BillingExportFileName(period string, isDraft bool) string {
	if isDraft {
		return "billing-statement-" + period + "-draft.xlsx"
	}
	return "billing-statement-" + period + ".xlsx"
}

// 四类 token 的合计，账单信息表与汇总合计行共用。
type billingTokenTotals struct {
	input      int64
	cacheRead  int64
	cacheWrite int64
	output     int64
}

func (r *BillingReport) tokenTotals() billingTokenTotals {
	var totals billingTokenTotals
	for _, row := range r.Summary {
		totals.input += row.InputTokens
		totals.cacheRead += row.CacheReadTokens
		totals.cacheWrite += row.CacheWriteTokens
		totals.output += row.OutputTokens
	}
	return totals
}

// WriteBillingReportXLSX 写一份三工作表的账单，返回实际写出的明细行数。
//
// 明细默认不写失败行：金额为 0 的行会让客户问「这行为什么不收钱」，
// 而排障在页面按类型筛得到。行数按实际写出计，与文件内容一致。
func WriteBillingReportXLSX(w io.Writer, report *BillingReport) (int64, error) {
	var detailRows int64
	err := WriteXLSX(w,
		billingInfoSheet(report),
		billingSummarySheet(report),
		billingDetailSheet(report, &detailRows),
	)
	return detailRows, err
}

func billingInfoSheet(report *BillingReport) XLSXSheet {
	statement := report.Statement
	totals := report.tokenTotals()
	isAdmin := report.Projection == BillingProjectionAdmin

	return XLSXSheet{
		Name:    "账单信息",
		Columns: []XLSXColumn{{Title: "项目", Width: 20}, {Title: "值", Width: 34}},
		Rows: func(row *XLSXRowWriter) error {
			pairs := [][2]string{
				{"账期", statement.Period},
				{"账期范围", formatBillingTime(statement.PeriodStart) + " ~ " + formatBillingTime(statement.PeriodEnd-1)},
				{"状态", billingStatusLabel(statement.Status, report.IsDraft)},
				{"用户", statement.Username},
				{"币种", statement.Currency},
				{"净费用", statement.TotalAmount},
				{"请求数", fmt.Sprintf("%d", statement.Requests)},
				{"失败数", fmt.Sprintf("%d", statement.Errors)},
				{"退款笔数", fmt.Sprintf("%d", statement.Refunds)},
				{"输入 token（未缓存）", fmt.Sprintf("%d", totals.input)},
				{"缓存读 token", fmt.Sprintf("%d", totals.cacheRead)},
				{"缓存写 token", fmt.Sprintf("%d", totals.cacheWrite)},
				{"输出 token", fmt.Sprintf("%d", totals.output)},
			}
			if isAdmin {
				pairs = append(pairs,
					[2]string{"内部 quota 合计", fmt.Sprintf("%d", statement.TotalQuota)},
					[2]string{"quota 折算率", fmt.Sprintf("%.0f / USD", statement.QuotaPerUnit)},
					[2]string{"结算汇率", fmt.Sprintf("%.4f", statement.SettleRate)},
				)
			}
			if statement.IssuedAt > 0 {
				pairs = append(pairs, [2]string{"出账时间", formatBillingTime(statement.IssuedAt)})
			}
			if statement.DueAt > 0 {
				pairs = append(pairs, [2]string{"到期时间", formatBillingTime(statement.DueAt)})
			}
			pairs = append(pairs, [2]string{"文件生成时间", formatBillingTime(report.GeneratedAt)})
			if report.IsDraft {
				pairs = append(pairs, [2]string{"说明", "当月账单为预估值，随用量变化；次月 1 日出账后金额冻结。"})
			}
			for _, pair := range pairs {
				if err := row.Row(TextCell(pair[0]), TextCell(pair[1])); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func billingSummarySheet(report *BillingReport) XLSXSheet {
	statement := report.Statement
	// 金额按账单自己的快照折算，不读当前汇率：出账后管理员改汇率，历史文件不变。
	snapshot := BillingSnapshot{
		QuotaPerUnit: statement.QuotaPerUnit,
		SettleRate:   statement.SettleRate,
		Currency:     statement.Currency,
	}
	isAdmin := report.Projection == BillingProjectionAdmin

	columns := []XLSXColumn{
		{Title: "模型", Width: 30},
	}
	if isAdmin {
		columns = append(columns, XLSXColumn{Title: "分组", Width: 14})
	}
	columns = append(columns,
		XLSXColumn{Title: "请求数", Width: 12},
		XLSXColumn{Title: "失败数", Width: 10},
		XLSXColumn{Title: "退款笔数", Width: 10},
		XLSXColumn{Title: "输入 token", Width: 14},
		XLSXColumn{Title: "缓存读", Width: 12},
		XLSXColumn{Title: "缓存写", Width: 12},
		XLSXColumn{Title: "输出 token", Width: 14},
	)
	if isAdmin {
		columns = append(columns, XLSXColumn{Title: "Quota", Width: 14})
	}
	columns = append(columns, XLSXColumn{Title: "金额", Width: 14})

	return XLSXSheet{
		Name:    "汇总",
		Columns: columns,
		Rows: func(row *XLSXRowWriter) error {
			for _, item := range report.Summary {
				cells := []XLSXCell{TextCell(item.ModelName)}
				if isAdmin {
					cells = append(cells, TextCell(item.UseGroup))
				}
				// 请求数含失败行，否则「合计」不等于各行相加：
				// 账单头的 Requests 本来就是消费 + 失败。
				cells = append(cells,
					IntCell(item.Requests+item.Errors),
					IntCell(item.Errors),
					IntCell(item.Refunds),
					IntCell(item.InputTokens),
					IntCell(item.CacheReadTokens),
					IntCell(item.CacheWriteTokens),
					IntCell(item.OutputTokens),
				)
				if isAdmin {
					cells = append(cells, IntCell(item.Quota))
				}
				cells = append(cells, DecimalCell(snapshot.AmountOf(item.Quota)))
				if err := row.Row(cells...); err != nil {
					return err
				}
			}
			// 合计行与账单头同源，客户能在一页内核对。
			cells := []XLSXCell{TextCell("合计")}
			if isAdmin {
				cells = append(cells, TextCell(""))
			}
			cells = append(cells,
				IntCell(int64(statement.Requests)),
				IntCell(int64(statement.Errors)),
				IntCell(int64(statement.Refunds)),
			)
			totals := report.tokenTotals()
			cells = append(cells,
				IntCell(totals.input),
				IntCell(totals.cacheRead),
				IntCell(totals.cacheWrite),
				IntCell(totals.output),
			)
			if isAdmin {
				cells = append(cells, IntCell(statement.TotalQuota))
			}
			cells = append(cells, DecimalCell(statement.TotalAmount))
			return row.Row(cells...)
		},
	}
}

func billingDetailSheet(report *BillingReport, written *int64) XLSXSheet {
	isAdmin := report.Projection == BillingProjectionAdmin

	columns := []XLSXColumn{
		{Title: "时间", Width: 20},
		{Title: "请求 ID", Width: 38},
		{Title: "类型", Width: 8},
		{Title: "令牌", Width: 18},
		{Title: "模型", Width: 30},
	}
	if isAdmin {
		columns = append(columns, XLSXColumn{Title: "分组", Width: 14}, XLSXColumn{Title: "请求类型", Width: 22})
	}
	columns = append(columns,
		XLSXColumn{Title: "输入 token", Width: 14},
		XLSXColumn{Title: "缓存读", Width: 12},
		XLSXColumn{Title: "缓存写", Width: 12},
		XLSXColumn{Title: "输出 token", Width: 14},
		XLSXColumn{Title: "总 token", Width: 14},
	)
	if isAdmin {
		columns = append(columns, XLSXColumn{Title: "Quota", Width: 14})
	}
	columns = append(columns,
		XLSXColumn{Title: "金额", Width: 14},
		XLSXColumn{Title: "任务 ID", Width: 28},
		XLSXColumn{Title: "耗时(ms)", Width: 12},
	)

	return XLSXSheet{
		Name:    "明细",
		Columns: columns,
		Rows: func(row *XLSXRowWriter) error {
			return report.Lines(func(line *model.BillingStatementLine) error {
				if line.Type == model.LogTypeError {
					return nil
				}
				totalTokens := int64(line.InputTokens + line.CacheReadTokens + line.CacheWriteTokens + line.OutputTokens)
				cells := []XLSXCell{
					TextCell(formatBillingTime(line.CreatedAt)),
					TextCell(line.RequestId),
					TextCell(billingTypeLabel(line.Type)),
					TextCell(line.TokenName),
					TextCell(line.ModelName),
				}
				if isAdmin {
					cells = append(cells, TextCell(line.UseGroup), TextCell(line.Endpoint))
				}
				cells = append(cells,
					IntCell(int64(line.InputTokens)),
					IntCell(int64(line.CacheReadTokens)),
					IntCell(int64(line.CacheWriteTokens)),
					IntCell(int64(line.OutputTokens)),
					IntCell(totalTokens),
				)
				if isAdmin {
					cells = append(cells, IntCell(line.Quota))
				}
				cells = append(cells,
					DecimalCell(line.Amount),
					TextCell(line.TaskId),
					IntCell(int64(line.UseTimeMs)),
				)
				if err := row.Row(cells...); err != nil {
					return err
				}
				*written++
				return nil
			})
		},
	}
}

// GenerateBillingExport 把账单写进 data/exports 并返回登记信息。
//
// 已出账的账期由出账任务调用一次，之后下载只是取静态文件；当月由导出请求调用，
// 每次都重新生成 —— 当月的数字本来每分钟都在变，缓存一份旧账反而容易被当成凭证。
func GenerateBillingExport(ctx context.Context, report *BillingReport) (*model.BillingStatementExport, error) {
	statement := report.Statement
	key := BillingExportKey(statement.Period, statement.Id, statement.UserId, report.Projection)
	var rows int64
	size, digest, err := WriteExportFile(key, func(w io.Writer) error {
		written, err := WriteBillingReportXLSX(w, report)
		rows = written
		return err
	})
	if err != nil {
		return nil, err
	}

	export := &model.BillingStatementExport{
		StatementId: statement.Id,
		Projection:  string(report.Projection),
		ObjectKey:   key,
		Bytes:       size,
		Rows:        rows,
		Sha256:      digest,
		CreatedAt:   time.Now().Unix(),
	}
	if statement.Id > 0 {
		if err := model.UpsertBillingStatementExport(export); err != nil {
			return nil, err
		}
	}
	return export, nil
}

// BillingExportETag 供下载端判断是否同一份文件。
func BillingExportETag(export *model.BillingStatementExport) string {
	return fmt.Sprintf(`"%s"`, export.Sha256)
}

func billingStatusLabel(status string, isDraft bool) string {
	if isDraft {
		return "进行中（预估）"
	}
	switch status {
	case model.BillingStatusIssued:
		return "已出账"
	case model.BillingStatusPaid:
		return "已结清"
	case model.BillingStatusVoid:
		return "已作废"
	}
	return status
}

func billingTypeLabel(logType int) string {
	switch logType {
	case model.LogTypeConsume:
		return "消费"
	case model.LogTypeError:
		return "失败"
	case model.LogTypeRefund:
		return "退款"
	}
	return "其他"
}

// formatBillingTime 固定东八区、精确到秒。离开年份的时间在归档文件里没法用，
// 所以这里不用页面上的「10-31 22:59」那种短格式。
func formatBillingTime(timestamp int64) string {
	if timestamp <= 0 {
		return ""
	}
	return time.Unix(timestamp, 0).In(time.FixedZone("CST", billingZoneOffset)).Format("2006-01-02 15:04:05")
}
