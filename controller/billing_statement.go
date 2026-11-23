package controller

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// 账单读取接口。
//
// 投影一律由服务端判定，不接受 `as=customer` 这类前端参数：一次误发就是把
// 内部计价结构发到客户手里。规则只有一条 —— 默认给客户投影，只有管理员显式
// 请求内部视图（`internal=1`）才多给 quota、分组与计价快照。

const (
	billingAccessTTL = time.Hour
)

// GetBillingStatement 返回一个账期的账单头与 (模型 × 分组) 汇总。
//
// 已出账返回冻结快照；当月实时聚合，`is_final=false`，页面要标注「预估」。
func GetBillingStatement(c *gin.Context) {
	period, ok := billingPeriodParam(c)
	if !ok {
		return
	}
	target, ok := billingTargetUser(c, period)
	if !ok {
		return
	}
	projection := billingProjectionFor(c)

	report, err := service.LoadBillingReport(c.Request.Context(), period, target.id, target.username,
		service.CurrentBillingSnapshot(), projection)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, newBillingOverviewView(report))
}

// ListBillingStatementLines 返回一页明细。
//
// 与页面排序相反：账单从月初读起。游标是 (created_at, id)，既不随行数变慢，
// 也不会因为期间有新写入而错行。
func ListBillingStatementLines(c *gin.Context) {
	period, ok := billingPeriodParam(c)
	if !ok {
		return
	}
	target, ok := billingTargetUser(c, period)
	if !ok {
		return
	}
	projection := billingProjectionFor(c)

	statement, found, err := model.FindBillingStatement(period, target.id)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !found || statement.Status == model.BillingStatusVoid {
		statement, err = service.NewDraftBillingStatement(period, target.id, target.username,
			service.CurrentBillingSnapshot())
		if err != nil {
			common.ApiError(c, err)
			return
		}
	}

	// 分页只需要账期与计价快照，不跑汇总：翻页不该每次都做一次 GROUP BY。
	report := &service.BillingReport{
		Statement:  statement,
		Projection: projection,
		IsDraft:    statement.Status == model.BillingStatusDraft,
	}
	limit, _ := strconv.Atoi(c.Query("page_size"))
	pageSize := service.NormalizeBillingLinePageSize(limit)
	filter := model.BillingLineFilter{
		ModelName: c.Query("model"),
		UseGroup:  c.Query("group"),
		RequestId: c.Query("request_id"),
	}
	if rawType, err := strconv.Atoi(c.Query("type")); err == nil {
		filter.Type = rawType
	}

	rows, next, err := service.LoadBillingLinesPage(c.Request.Context(), report, filter,
		service.ParseBillingLineCursor(c.Query("cursor")), pageSize)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	items := make([]billingLineView, 0, len(rows))
	for _, row := range rows {
		items = append(items, billingLineViewFromLine(row, projection))
	}
	common.ApiSuccess(c, gin.H{
		"items":       items,
		"next_cursor": service.FormatBillingLineCursor(next),
		"page_size":   pageSize,
		"is_final":    !report.IsDraft,
		"projection":  projection,
	})
}

// ExportBillingStatement 生成或取出归档文件，返回一个带签名的下载地址。
//
// 已出账的账期在出账任务里已经生成好文件，这里只签名，不查库、不流式；
// 当月没有终点，只能现场生成，并受行数上限约束。
func ExportBillingStatement(c *gin.Context) {
	period, ok := billingPeriodParam(c)
	if !ok {
		return
	}
	target, ok := billingTargetUser(c, period)
	if !ok {
		return
	}
	projection := billingProjectionFor(c)

	statement, found, err := model.FindBillingStatement(period, target.id)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	if found && statement.Status != model.BillingStatusVoid {
		if export, err := model.GetBillingStatementExport(statement.Id, string(projection)); err == nil {
			// 记录在库里不等于文件在盘上：导出目录被清理、换挂载卷、从旧机器搬过来，
			// 都会让这里签出一个必然 404 的地址，而客户只看到「下载失败」。
			// 文件没了就往下走重新生成 —— 已出账的数据来自 MySQL 快照，不碰 ClickHouse。
			if _, _, statErr := service.ExportFileStat(export.ObjectKey); statErr == nil {
				billingReplyDownload(c, export, period, false)
				return
			} else {
				common.SysLog(common.LogText("billing export file missing, regenerating: statement=%d projection=%s key=%s error=%v",
					statement.Id, projection, export.ObjectKey, statErr))
			}
		}
	}

	// 当月（或历史缺档）：现场生成。同一时刻只允许有限个导出在跑，
	// 与日志导出共用一个闸门，避免月末几十个人同时点导出把连接池占满。
	select {
	case logExportSlots <- struct{}{}:
		defer func() { <-logExportSlots }()
	default:
		common.ApiErrorMsg(c, "Too many exports are running. Try again in a minute.")
		return
	}

	if !found || statement.Status == model.BillingStatusVoid {
		// 先数再写：写到一半停下会给出一个看起来完整、实际截断的文件。
		bounds, err := service.NewDraftBillingStatement(period, target.id, target.username, service.CurrentBillingSnapshot())
		if err != nil {
			common.ApiError(c, err)
			return
		}
		total, err := model.CountUserLogsForBilling(c.Request.Context(), target.id, bounds.PeriodStart, bounds.PeriodEnd)
		if err != nil {
			common.ApiError(c, err)
			return
		}
		if total == 0 {
			// 空文件看起来像一份正式账单，客户只会拿来问「为什么是 0」。
			common.ApiError(c, common.NewMessage("No billable usage in this period."))
			return
		}
		if total > int64(billingDraftExportMaxRows()) {
			common.ApiErrorMsg(c, "This statement has too many rows to export before it is issued. The full file is available after the 1st of next month.")
			return
		}
	}

	report, err := service.LoadBillingReport(c.Request.Context(), period, target.id, target.username,
		service.CurrentBillingSnapshot(), projection)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	export, err := service.GenerateBillingExport(c.Request.Context(), report)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	billingReplyDownload(c, export, period, report.IsDraft)
}

// ListBillingStatements 是管理员的全站月度视图：一个账期下所有用户的账单。
//
// 当月没有出账，列表是空的；页面上要说清「1 号出账后可见」，而不是显示 0 元。
func ListBillingStatements(c *gin.Context) {
	if c.GetInt("role") < common.RoleAdminUser {
		// 空 body 会让前端只能显示通用错误；给一条可读的原因。
		common.ApiErrorStatus(c, http.StatusForbidden,
			common.NewMessage("Administrator role required for the all-users view."))
		return
	}
	period, ok := billingPeriodParam(c)
	if !ok {
		return
	}
	keyword := strings.TrimSpace(c.Query("keyword"))

	rows, isDraft, err := service.LoadBillingUserSummaries(c.Request.Context(), period, keyword)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	type item struct {
		UserId   int    `json:"user_id"`
		Username string `json:"username"`
		Status   string `json:"status"`
		Amount   string `json:"amount"`
		Quota    int64  `json:"quota"`
		Requests int    `json:"requests"`
		Errors   int    `json:"errors"`
		Refunds  int    `json:"refunds"`
		IssuedAt int64  `json:"issued_at"`
		DueAt    int64  `json:"due_at"`
		Currency string `json:"currency"`
		IsIssued bool   `json:"is_issued"`
	}
	items := make([]item, 0, len(rows))
	for _, row := range rows {
		items = append(items, item{
			UserId:   row.UserId,
			Username: row.Username,
			Status:   row.Status,
			Amount:   row.Amount,
			Quota:    row.Quota,
			Requests: row.Requests,
			Errors:   row.Errors,
			Refunds:  row.Refunds,
			IssuedAt: row.IssuedAt,
			DueAt:    row.DueAt,
			Currency: row.Currency,
			IsIssued: row.IsIssued,
		})
	}
	// pending 只在「真的一个人都没用」时为 true。带关键词搜不到是「无匹配」，
	// 说成「本期账单尚未出账」会误导运营去等 1 号。
	pending := len(items) == 0 && keyword == ""
	common.ApiSuccess(c, gin.H{
		"period":   period,
		"items":    items,
		"pending":  pending,
		"is_draft": isDraft,
	})
}

// billingDraftExportMaxRows 是当月账单导出的行数上限。
//
// 与日志导出分开配置：日志导出随时可能被点，闸门要收紧；账单是月度主动操作，
// 而且写入是流式、内存与行数无关。按日志的 50 万会被真实客户（单月 60 万+）
// 天天撞上，等于当月导出不可用。
func billingDraftExportMaxRows() int {
	return common.GetEnvOrDefault("BILLING_DRAFT_EXPORT_MAX_ROWS", 2_000_000)
}

// billingUser 是本次请求的账单归属。
type billingUser struct {
	id       int
	username string
}

// billingTargetUser 解析账单归属。普通用户传 user_id 时忽略并只看自己 ——
// 报错会变成一个探测别人 id 是否存在的接口。
//
// period 用来界定「有没有账单」的范围：已出账看快照表，当月看该月日志用量。
func billingTargetUser(c *gin.Context, period string) (billingUser, bool) {
	viewerId := c.GetInt("id")
	targetId := viewerId
	if raw := c.Query("user_id"); raw != "" && c.GetInt("role") >= common.RoleAdminUser {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			common.ApiErrorMsg(c, "Invalid user_id.")
			return billingUser{}, false
		}
		targetId = parsed
	}
	username, err := model.GetUsernameById(targetId, false)
	// GetUsernameById 对不存在的行返回 ("", nil)，只判 err 会把「查无此人」
	// 变成一份 username 为空的空账单，运营会误读成「该用户当月没消费」。
	// 但用户被删除时名字同样为空，而他的账单可能还在 —— 那种情况要继续。
	if targetId != viewerId && (err != nil || username == "") {
		start, end, boundsErr := service.BillingPeriodBounds(period)
		if boundsErr != nil {
			start, end = 0, 0
		}
		exists, lookupErr := model.BillingStatementExistsForUser(c.Request.Context(), targetId, start, end)
		if lookupErr != nil {
			// 查不动 ≠ 查无此人。ClickHouse 内存被打满时这里会在十几毫秒内
			// 返回 Code: 241，把一次瞬时故障当成结论，运营就会看到「老客户的
			// 账单不存在」。放行一份可能是空的账单，损失远小于拒绝一份真实账单。
			// 权限面不受影响：只有管理员能传 user_id，全站列表本来就返回这些 id。
			common.SysLog(common.LogText("billing statement existence probe failed, allowing the request: user_id=%d period=%s error=%v",
				targetId, period, lookupErr))
			exists = true
		}
		if !exists {
			common.ApiError(c, common.NewMessage("User not found."))
			return billingUser{}, false
		}
		username = "user#" + strconv.Itoa(targetId)
	}
	return billingUser{id: targetId, username: username}, true
}

// billingPeriodParam 校验账期格式。缺省不补当月：页面必须显式带上 period，
// 这样服务端日志与审计能看出查的是哪个月。
func billingPeriodParam(c *gin.Context) (string, bool) {
	period := c.Query("period")
	if period == "" {
		common.ApiErrorMsg(c, "period is required, format YYYY-MM.")
		return "", false
	}
	if _, _, err := service.BillingPeriodBounds(period); err != nil {
		common.ApiErrorMsg(c, "Invalid period, want YYYY-MM.")
		return "", false
	}
	return period, true
}

// billingProjectionFor 是服务端唯一的投影判定。
//
// 默认客户投影：即使管理员看自己的账单，少给几列也不会出错，多给几列却可能
// 被转发出去。管理员要内部数据必须显式 internal=1，多一步换掉一次误发。
func billingProjectionFor(c *gin.Context) service.BillingProjection {
	if c.GetInt("role") >= common.RoleAdminUser && c.Query("internal") == "1" {
		return service.BillingProjectionAdmin
	}
	return service.BillingProjectionCustomer
}

// billingReplyDownload 签发下载地址。文件本体走已有的导出下载端点，
// 那里已经处理了 Range、ETag 与过期校验。
func billingReplyDownload(c *gin.Context, export *model.BillingStatementExport, period string, isDraft bool) {
	access, err := service.IssueExportFileAccess(export.ObjectKey, billingAccessTTL)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{
		"download_url": "/api/export/files/" + export.ObjectKey + "?access=" + access,
		"filename":     service.BillingExportFileName(period, isDraft),
		"rows":         export.Rows,
		"bytes":        export.Bytes,
		"sha256":       export.Sha256,
		"is_draft":     isDraft,
		"expires_at":   time.Now().Add(billingAccessTTL).Unix(),
	})
}

// billingOverviewView 是概览响应。客户投影里 quota 与 pricing 一律不出现，
// 不是置 0 —— omitempty 让字段本身消失，前端想误用也没有东西可用。
type billingOverviewView struct {
	Period      string `json:"period"`
	Status      string `json:"status"`
	IsFinal     bool   `json:"is_final"`
	Projection  string `json:"projection"`
	UserId      int    `json:"user_id"`
	Username    string `json:"username"`
	PeriodStart int64  `json:"period_start"`
	PeriodEnd   int64  `json:"period_end"`
	IssuedAt    int64  `json:"issued_at,omitempty"`
	DueAt       int64  `json:"due_at,omitempty"`
	Currency    string `json:"currency"`
	Amount      string `json:"amount"`
	Quota       int64  `json:"quota,omitempty"`
	Requests    int    `json:"requests"`
	Errors      int    `json:"errors"`
	Refunds     int    `json:"refunds"`

	InputTokens      int64 `json:"input_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	OutputTokens     int64 `json:"output_tokens"`

	Items   []billingSummaryView `json:"items"`
	Pricing *billingPricingView  `json:"pricing,omitempty"`
}

type billingSummaryView struct {
	ModelName        string `json:"model"`
	UseGroup         string `json:"group,omitempty"`
	Requests         int64  `json:"requests"`
	Errors           int64  `json:"errors"`
	Refunds          int64  `json:"refunds"`
	InputTokens      int64  `json:"input_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	Quota            int64  `json:"quota,omitempty"`
	Amount           string `json:"amount"`
}

type billingPricingView struct {
	QuotaPerUnit float64 `json:"quota_per_unit"`
	SettleRate   float64 `json:"settle_rate"`
	Currency     string  `json:"currency"`
}

func newBillingOverviewView(report *service.BillingReport) billingOverviewView {
	statement := report.Statement
	isAdmin := report.Projection == service.BillingProjectionAdmin
	snapshot := service.BillingSnapshot{
		QuotaPerUnit: statement.QuotaPerUnit,
		SettleRate:   statement.SettleRate,
		Currency:     statement.Currency,
	}

	var input, cacheRead, cacheWrite, output int64
	items := make([]billingSummaryView, 0, len(report.Summary))
	for _, row := range report.Summary {
		input += row.InputTokens
		cacheRead += row.CacheReadTokens
		cacheWrite += row.CacheWriteTokens
		output += row.OutputTokens

		view := billingSummaryView{
			ModelName:        row.ModelName,
			Requests:         row.Requests + row.Errors,
			Errors:           row.Errors,
			Refunds:          row.Refunds,
			InputTokens:      row.InputTokens,
			CacheReadTokens:  row.CacheReadTokens,
			CacheWriteTokens: row.CacheWriteTokens,
			OutputTokens:     row.OutputTokens,
			Amount:           snapshot.AmountOf(row.Quota),
		}
		if isAdmin {
			view.UseGroup = row.UseGroup
			view.Quota = row.Quota
		}
		items = append(items, view)
	}

	response := billingOverviewView{
		Period:           statement.Period,
		Status:           statement.Status,
		IsFinal:          !report.IsDraft,
		Projection:       string(report.Projection),
		UserId:           statement.UserId,
		Username:         statement.Username,
		PeriodStart:      statement.PeriodStart,
		PeriodEnd:        statement.PeriodEnd,
		IssuedAt:         statement.IssuedAt,
		DueAt:            statement.DueAt,
		Currency:         statement.Currency,
		Amount:           statement.TotalAmount,
		Requests:         statement.Requests,
		Errors:           statement.Errors,
		Refunds:          statement.Refunds,
		InputTokens:      input,
		CacheReadTokens:  cacheRead,
		CacheWriteTokens: cacheWrite,
		OutputTokens:     output,
		Items:            items,
	}
	if isAdmin {
		response.Quota = statement.TotalQuota
		response.Pricing = &billingPricingView{
			QuotaPerUnit: statement.QuotaPerUnit,
			SettleRate:   statement.SettleRate,
			Currency:     statement.Currency,
		}
	}
	return response
}

// billingLineView 是明细行。客户投影不出现 quota 与分组；endpoint 是公开路由名，
// 客户排障需要它，保留。
type billingLineView struct {
	RequestId        string `json:"request_id"`
	CreatedAt        int64  `json:"created_at"`
	Type             int    `json:"type"`
	TokenName        string `json:"token_name"`
	ModelName        string `json:"model"`
	UseGroup         string `json:"group,omitempty"`
	Endpoint         string `json:"endpoint,omitempty"`
	InputTokens      int    `json:"input_tokens"`
	CacheReadTokens  int    `json:"cache_read_tokens"`
	CacheWriteTokens int    `json:"cache_write_tokens"`
	OutputTokens     int    `json:"output_tokens"`
	TotalTokens      int    `json:"total_tokens"`
	Quota            int64  `json:"quota,omitempty"`
	Amount           string `json:"amount"`
	TaskId           string `json:"task_id,omitempty"`
	UseTimeMs        int    `json:"use_time_ms,omitempty"`
}

func billingLineViewFromLine(line *model.BillingStatementLine, projection service.BillingProjection) billingLineView {
	view := billingLineView{
		RequestId:        line.RequestId,
		CreatedAt:        line.CreatedAt,
		Type:             line.Type,
		TokenName:        line.TokenName,
		ModelName:        line.ModelName,
		Endpoint:         line.Endpoint,
		InputTokens:      line.InputTokens,
		CacheReadTokens:  line.CacheReadTokens,
		CacheWriteTokens: line.CacheWriteTokens,
		OutputTokens:     line.OutputTokens,
		TotalTokens:      line.InputTokens + line.CacheReadTokens + line.CacheWriteTokens + line.OutputTokens,
		Amount:           line.Amount,
		TaskId:           line.TaskId,
		UseTimeMs:        line.UseTimeMs,
	}
	if projection == service.BillingProjectionAdmin {
		view.UseGroup = line.UseGroup
		view.Quota = line.Quota
	}
	return view
}

// IssueBillingStatement 手动补跑一次出账（管理员）。
//
// 自动出账只在 00:10–06:00 窗口内跑，窗口一过当天就没有第二次机会；某个用户
// 因数据异常失败、或者修完 bug 要立刻重算，都要靠这个入口。任务本身幂等：
// 已出账的用户直接跳过，重复点不会产生第二份账单。
func IssueBillingStatement(c *gin.Context) {
	if c.GetInt("role") < common.RoleAdminUser {
		common.ApiErrorStatus(c, http.StatusForbidden, errors.New("Administrator role required."))
		return
	}
	if !common.IsMasterNode {
		common.ApiErrorMsg(c, "Billing issue runs on the master node only.")
		return
	}
	period := strings.TrimSpace(c.Query("period"))
	if _, _, err := service.BillingPeriodBounds(period); err != nil {
		common.ApiErrorMsg(c, "Invalid period, want YYYY-MM.")
		return
	}
	if period > service.CurrentBillingPeriod(time.Now()) {
		common.ApiErrorMsg(c, "Cannot issue a future period.")
		return
	}
	if !service.TryStartBillingIssue(period) {
		// 与增长页的手动重算同一个口径：已经在跑就不是失败，返回 200。
		common.ApiSuccess(c, gin.H{"started": false, "already_running": true})
		return
	}
	go func() {
		result, err := service.RunBillingIssueForPeriod(context.Background(), period)
		service.FinishBillingIssue(period, result, err)
		if err != nil {
			common.SysError(common.LogText("billing issue (manual) failed: period=%s error=%s", period, err.Error()))
			return
		}
		common.SysLog(common.LogText("billing issue (manual) done: period=%s issued=%d skipped=%d failed=%d exports=%d",
			result.Period, result.Issued, result.Skipped, result.Failed, result.Exports))
	}()
	common.ApiSuccess(c, gin.H{"started": true, "period": period})
}

// GetBillingIssueStatus 返回最近一次出账的运行状态，供补跑后轮询。
func GetBillingIssueStatus(c *gin.Context) {
	if c.GetInt("role") < common.RoleAdminUser {
		common.ApiErrorStatus(c, http.StatusForbidden, errors.New("Administrator role required."))
		return
	}
	common.ApiSuccess(c, service.BillingIssueStatusSnapshot())
}
