package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// 汇总排序在 Go 里做，不在 SQL 里。这个用例锁住行为，也提醒后来者别把
// ORDER BY SUM(...) 加回去：ClickHouse 会报 aggregate inside aggregate。
func TestSortBillingSummary(t *testing.T) {
	rows := []BillingSummaryRow{
		{ModelName: "beta", UseGroup: "vip", Quota: 100},
		{ModelName: "alpha", UseGroup: "vip", Quota: 100},
		{ModelName: "gamma", UseGroup: "default", Quota: 900},
		{ModelName: "refunded", UseGroup: "vip", Quota: -50},
	}

	sortBillingSummary(rows)

	assert.Equal(t, "gamma", rows[0].ModelName, "金额最大的在最前")
	assert.Equal(t, "alpha", rows[1].ModelName, "同金额按模型名正序")
	assert.Equal(t, "beta", rows[2].ModelName)
	assert.Equal(t, "refunded", rows[3].ModelName, "只有退款的组排最后")

	// 空切片与单行都不能 panic。
	sortBillingSummary(nil)
	sortBillingSummary([]BillingSummaryRow{{ModelName: "only"}})
}
