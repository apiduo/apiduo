package controller

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// 投影是安全边界：默认必须给客户投影，只有管理员显式 internal=1 才多给字段。
func TestBillingProjectionFor(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name   string
		role   int
		query  string
		expect service.BillingProjection
	}{
		{"普通用户默认", common.RoleCommonUser, "", service.BillingProjectionCustomer},
		{"普通用户带 internal 也不行", common.RoleCommonUser, "?internal=1", service.BillingProjectionCustomer},
		{"管理员默认客户投影", common.RoleAdminUser, "", service.BillingProjectionCustomer},
		{"管理员显式内部视图", common.RoleAdminUser, "?internal=1", service.BillingProjectionAdmin},
		{"根用户显式内部视图", common.RoleRootUser, "?internal=1", service.BillingProjectionAdmin},
		{"internal 只认 1", common.RoleAdminUser, "?internal=true", service.BillingProjectionCustomer},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest("GET", "/api/billing/statement"+item.query, nil)
			c.Set("role", item.role)
			assert.Equal(t, item.expect, billingProjectionFor(c))
		})
	}
}

func TestBillingPeriodParamRequiresValidMonth(t *testing.T) {
	gin.SetMode(gin.TestMode)

	valid := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(valid)
	c.Request = httptest.NewRequest("GET", "/?period=2026-10", nil)
	period, ok := billingPeriodParam(c)
	assert.True(t, ok)
	assert.Equal(t, "2026-10", period)

	for _, bad := range []string{"", "2026-13", "2026/10", "202610", "2026-10-01"} {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Request = httptest.NewRequest("GET", "/?period="+bad, nil)
		_, ok := billingPeriodParam(context)
		assert.False(t, ok, "period %q must be rejected", bad)
		assert.Equal(t, 200, recorder.Code, "业务错误保持 HTTP 200 + success:false")
	}
}
