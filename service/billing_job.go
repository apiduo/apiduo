package service

import (
	"context"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// 每月出账。
//
// 出账是「把一个自然月折成一份不再变化的凭证」：明细落库、金额冻结、两个投影
// 的 xlsx 一并生成。它是离线任务，不在转发路径上，跑得慢一点无所谓；要紧的是
// 幂等与可重跑 —— 1 日凌晨重启、上游报错、某个用户数据异常，都不该让一份账单
// 停在半成品状态。

const (
	// billingIssueMinuteOfDay 是次月 1 日的出账时刻：00:10。
	// 留 10 分钟给迟到日志与异步任务结算，否则月末最后几笔会掉到下一期。
	billingIssueMinuteOfDay = 10
	// billingIssueWindowEndMinute 是出账窗口的结束时刻 06:00。
	// 只在夜间出账：全量扫一个月日志会长时间占用连接池，而转发与它共用同一个池；
	// 白天补跑意味着用户正在用的时刻去抢 IO。错过 1 日凌晨就等当晚，账单晚半天
	// 生成不会有人受损，白天卡一下会。
	billingIssueWindowEndMinute = 6 * 60
	// billingIssueRetryInterval 让进程重启或前一次失败后能自动补跑。
	// 任务本身幂等，重复跑只是多做几次「已存在」查询。
	billingIssueRetryInterval = 30 * time.Minute
)

// BillingIssueResult 是一次出账的结果，供日志与测试断言。
type BillingIssueResult struct {
	Period  string
	Issued  int
	Skipped int
	Failed  int
	Exports int
}

// StartBillingTaskLoop 启动每月出账。只有主节点跑：多实例各跑一套会 N 倍
// 抢占连接池，而转发与它共用同一个池。
func StartBillingTaskLoop() {
	if !common.IsMasterNode {
		common.SysLog(common.LogText("billing task skipped on non-master node"))
		return
	}
	go func() {
		// 启动时先补一次：进程正好在出账窗口重启时，内存里的「今天跑过了」
		// 已经丢了，靠这一次接上。没到 00:10 就不标记，让后续 tick 继续尝试。
		lastRunDay := ""
		if billingIssueIfDue() {
			lastRunDay = billingIssueDayMark()
		}
		ticker := time.NewTicker(billingIssueRetryInterval)
		defer ticker.Stop()
		for range ticker.C {
			if billingIssueDayMark() == lastRunDay {
				continue
			}
			if billingIssueIfDue() {
				lastRunDay = billingIssueDayMark()
			}
		}
	}()
}

// billingIssueIfDue 在每天 00:10–06:00 的窗口内处理上一个自然月，返回是否真的跑过。
//
// 不限定「只在 1 日跑」：1 日整天宕机、或某个用户反复失败，第二天仍要能补。
// 已出账的用户会在 BuildBillingStatement 里直接返回，代价只有一次主键查询。
// 窗口外的调用直接返回，是为了不让一次白天的重启把全站重算排在转发高峰上。
func billingIssueIfDue() bool {
	now := time.Now().In(time.FixedZone("CST", billingZoneOffset))
	if !billingIssueDueAt(now) {
		return false
	}
	period := previousBillingPeriod(now)
	result, err := RunBillingIssueForPeriod(context.Background(), period)
	if err != nil {
		common.SysLog(common.LogText("billing issue failed: period=%s error=%s", period, err.Error()))
		return false
	}
	if result.Issued > 0 || result.Failed > 0 {
		common.SysLog(common.LogText(
			"billing issued: period=%s issued=%d skipped=%d failed=%d exports=%d",
			result.Period, result.Issued, result.Skipped, result.Failed, result.Exports))
	}
	return true
}

// billingIssueDueAt 判断当前时刻是否在出账窗口内：00:10 到 06:00（含边界）。
// 单独成函数是为了能测边界 —— 差一分钟就是白天抢资源，或者整个月不出账。
func billingIssueDueAt(now time.Time) bool {
	local := now.In(time.FixedZone("CST", billingZoneOffset))
	minuteOfDay := local.Hour()*60 + local.Minute()
	return minuteOfDay >= billingIssueMinuteOfDay && minuteOfDay <= billingIssueWindowEndMinute
}

// billingIssueDayMark 是「今天」的标记，用于避免同一天反复出账。
func billingIssueDayMark() string {
	return time.Now().In(time.FixedZone("CST", billingZoneOffset)).Format("2006-01-02")
}

// previousBillingPeriod 返回 now 所在月的上一个自然月。
func previousBillingPeriod(now time.Time) string {
	local := now.In(time.FixedZone("CST", billingZoneOffset))
	return local.AddDate(0, 0, -local.Day()).Format("2006-01")
}

// RunBillingIssueForPeriod 为一个账期出账：逐用户冻结明细、生成两份归档文件。
//
// 幂等：已 issued 的用户直接跳过；中途失败的用户会在下一次调用时重建
// （BuildBillingStatement 会把半份账单清掉再写）。串行处理是刻意的 ——
// 出账与转发共用连接池，并行度越高，月末最后一天的真实请求越容易被拖慢。
func RunBillingIssueForPeriod(ctx context.Context, period string) (BillingIssueResult, error) {
	result := BillingIssueResult{Period: period}
	start, end, err := BillingPeriodBounds(period)
	if err != nil {
		return result, err
	}

	userIds, err := model.ListBillingCandidateUsers(ctx, start, end)
	if err != nil {
		return result, err
	}

	snapshot := CurrentBillingSnapshot()
	for _, userId := range userIds {
		username, err := model.GetUsernameById(userId, false)
		if err != nil || username == "" {
			// 用户已被删除但日志还在：账单仍要出，用 id 标识，不阻断整批。
			username = "user#" + strconv.Itoa(userId)
		}

		statement, created, err := BuildBillingStatement(ctx, period, userId, username, snapshot)
		if err != nil {
			result.Failed++
			common.SysLog(common.LogText("billing build failed: period=%s user=%d error=%s", period, userId, err.Error()))
			continue
		}
		if !created {
			result.Skipped++
			continue
		}
		result.Issued++

		// 汇总与明细只算一次，两个投影共用同一份读数：
		// 客户版少几列，金额与合计完全一致。
		report, err := LoadIssuedBillingReport(ctx, statement, BillingProjectionAdmin)
		if err != nil {
			result.Failed++
			common.SysLog(common.LogText("billing report failed: period=%s user=%d error=%s", period, userId, err.Error()))
			continue
		}
		if _, err := GenerateBillingExport(ctx, report); err != nil {
			result.Failed++
			common.SysLog(common.LogText("billing export failed: period=%s user=%d projection=admin error=%s", period, userId, err.Error()))
			continue
		}
		result.Exports++

		customer := *report
		customer.Projection = BillingProjectionCustomer
		if _, err := GenerateBillingExport(ctx, &customer); err != nil {
			result.Failed++
			common.SysLog(common.LogText("billing export failed: period=%s user=%d projection=customer error=%s", period, userId, err.Error()))
			continue
		}
		result.Exports++
	}
	return result, nil
}
