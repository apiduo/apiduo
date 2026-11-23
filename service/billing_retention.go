package service

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// 账单数据的保留期。
//
// 明细是最大的一块：503 万行一个月 ≈ 2.1 GB（1.16 GB 数据 + 0.93 GB 索引），
// 归档文件 500 MB/月。不清理就是无上限增长，而运营真正会回头看的是最近几个月。
// 默认留 6 个账期（含当月），0 或负数表示永久保留。
const billingRetentionMonthsDefault = 6

// BillingRetentionMonths 返回配置的保留月数。每次现读，改了不用重启。
func BillingRetentionMonths() int {
	months := common.GetEnvOrDefault("BILLING_RETENTION_MONTHS", billingRetentionMonthsDefault)
	if months < 0 {
		return 0
	}
	return months
}

// BillingPurgeResult 是一次清理的结果，供日志与测试断言。
type BillingPurgeResult struct {
	Cutoff     string // 早于这个账期的都会被删
	Statements int
	Lines      int64
	Exports    int
	Files      int
	Bytes      int64
}

// PurgeBillingBefore 删除 cutoff 之前的账单：明细 → 导出记录与文件 → 账单头。
//
// 顺序是有意的：先删最占空间的明细，再删记录，最后删头；中途失败时账单头还在，
// 下一次清理会接着删，不会留下「有头没明细」的半份账单被页面读到。
//
// 只删完全早于 cutoff 的账期：cutoff 自身以及更新的账期一律不动。
func PurgeBillingBefore(ctx context.Context, cutoff string) (BillingPurgeResult, error) {
	result := BillingPurgeResult{Cutoff: cutoff}

	var statements []*model.BillingStatement
	if err := model.DB.WithContext(ctx).
		Where("period < ?", cutoff).
		Order("period asc, user_id asc").
		Find(&statements).Error; err != nil {
		return result, err
	}
	if len(statements) == 0 {
		return result, nil
	}

	for _, statement := range statements {
		exports, err := model.ListBillingStatementExports(statement.Id)
		if err != nil {
			return result, err
		}
		for _, export := range exports {
			if path, pathErr := ExportFilePath(export.ObjectKey); pathErr == nil {
				if info, statErr := os.Stat(path); statErr == nil {
					if removeErr := os.Remove(path); removeErr != nil {
						common.SysError(common.LogText("billing purge: remove file failed: key=%s error=%s", export.ObjectKey, removeErr.Error()))
					} else {
						result.Files++
						result.Bytes += info.Size()
					}
				}
			}
		}
		if err := model.DB.WithContext(ctx).Where("statement_id = ?", statement.Id).
			Delete(&model.BillingStatementExport{}).Error; err != nil {
			return result, err
		}
		result.Exports += len(exports)

		deleted, err := model.DeleteBillingStatementLines(statement.Id)
		if err != nil {
			return result, err
		}
		result.Lines += deleted

		if err := model.DB.WithContext(ctx).Delete(statement).Error; err != nil {
			return result, err
		}
		result.Statements++
	}
	return result, nil
}

// PurgeBillingRetention 按保留期清理一次，cutoff 取「当前账期往前数 months-1 个月」。
//
// months <= 0 直接返回：显式关掉保留策略时不该有任何删除动作。
func PurgeBillingRetention(ctx context.Context, now time.Time) (BillingPurgeResult, error) {
	months := BillingRetentionMonths()
	if months <= 0 {
		return BillingPurgeResult{}, nil
	}
	cutoff := billingRetentionCutoff(now, months)
	return PurgeBillingBefore(ctx, cutoff)
}

// billingRetentionCutoff 返回最早的保留账期。
// 今天是 2026-11、留 6 个月 → 保留 2026-06..2026-11，cutoff = "2026-06"。
func billingRetentionCutoff(now time.Time, months int) string {
	if months < 1 {
		months = 1
	}
	local := now.In(time.FixedZone("CST", billingZoneOffset))
	return local.AddDate(0, -(months - 1), 0).Format("2006-01")
}

// ErrBillingIssueDisabled 表示当前节点不负责出账。
var ErrBillingIssueDisabled = errors.New("billing issue runs on the master node only")
