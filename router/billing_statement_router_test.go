package router

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// 账单自己的注册表项：与导出下载一样走模块清单，不往 api-router.go 里塞手工块。
func TestBillingStatementRoutesRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)

	registered := false
	for _, module := range apiduoModules {
		if module.name == "billing-statement" {
			registered = true
		}
	}
	assert.True(t, registered, "billing-statement must be listed in apiduoModules")

	engine := gin.New()
	registerApiduoBillingStatement(engine.Group(""))

	wanted := map[string]bool{
		"GET /billing/statements":       false,
		"GET /billing/statement":        false,
		"GET /billing/statement/lines":  false,
		"GET /billing/statement/export": false,
	}
	for _, route := range engine.Routes() {
		key := route.Method + " " + route.Path
		if _, ok := wanted[key]; ok {
			wanted[key] = true
		}
	}
	for key, found := range wanted {
		assert.True(t, found, "%s should be registered", key)
	}

	// 账单端点是只读的：出现写方法说明有人加了个会改数据的口子。
	for _, route := range engine.Routes() {
		if route.Method != http.MethodGet {
			t.Fatalf("billing routes must stay read-only, found %s %s", route.Method, route.Path)
		}
	}
}
