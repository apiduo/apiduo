package service

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
)

// quotaTopUp 构造额度不足时的出路提示。
//
// 为什么只给 URL 不给「还能用几天」：后者要读聚合表或日志，
// 而这里在请求路径上。客户此刻最需要的是「去哪充」，
// 剩余天数可以在控制台里看，不值得为它在这条路径上加一次查询。
//
// balanceQuota <= 0 时不填余额：0 既可能是「真的用光了」，
// 也可能是「这里拿不到」，混淆两者会误导客户。
func quotaTopUp(balanceQuota int) types.NewAPIErrorOptions {
	hint := types.TopUpHint{
		URL:  "/wallet",
		Hint: "余额不足，充值后可继续调用",
	}
	if balanceQuota > 0 && common.QuotaPerUnit > 0 {
		usd := float64(balanceQuota) / common.QuotaPerUnit
		hint.Balance = &usd
	}
	return types.ErrOptionWithTopUp(hint)
}
