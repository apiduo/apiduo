package controller

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
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

// 探测失败不等于「查无此人」。
//
// 线上 ClickHouse 服务端内存被聚合查询打满时，存在性探测会在十几毫秒内被拒
// （Code: 241），而旧代码把 err 和 !exists 一起当成 404，运营就会看到「这个
// 老客户的账单不存在」——一次瞬时故障变成一条业务结论。现在失败放行，让
// 后面真正读数据的步骤去决定成败。
func TestBillingTargetUserAllowsWhenProbeFails(t *testing.T) {
	gin.SetMode(gin.TestMode)

	primary, err := gorm.Open(sqlite.Open("file:billing-target-user?mode=memory&cache=shared"),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, primary.AutoMigrate(&model.BillingStatement{}, &model.Log{}))

	// 一个立刻报错的日志库：模拟 ClickHouse 拒绝查询。
	broken, err := gorm.Open(sqlite.Open("file:billing-broken?mode=memory&cache=shared"),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	brokenSQL, err := broken.DB()
	require.NoError(t, err)
	require.NoError(t, brokenSQL.Close())

	previousDB, previousLogDB := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = primary, broken
	previousRedis := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.RedisEnabled = previousRedis
	})

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("GET", "/api/billing/statement?period=2026-11&user_id=64", nil)
	c.Set("id", 1)
	c.Set("role", common.RoleAdminUser)

	target, ok := billingTargetUser(c, "2026-11")
	assert.True(t, ok, "探测失败必须放行")
	assert.Equal(t, 64, target.id)
	assert.Equal(t, "user#64", target.username)

	// 反过来：探测成功且确实没有账单时仍然要拒绝，否则「放行」就退化成
	// 「谁来都行」，管理员能凭空看任意 id 的空账单。
	model.LOG_DB = primary
	recorder = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("GET", "/api/billing/statement?period=2026-11&user_id=64", nil)
	c.Set("id", 1)
	c.Set("role", common.RoleAdminUser)
	_, ok = billingTargetUser(c, "2026-11")
	assert.False(t, ok, "没有任何账单的 id 仍然必须拒绝")
}
