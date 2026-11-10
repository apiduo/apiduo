package middleware

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// 声明表是 fail-closed 的：没登记的路由，任何 scoped token 都会被
// 403 ACCESS_TOKEN_ROUTE_UNDECLARED 挡掉，而且只有运行时才暴露 ——
// 轨迹详情 2026-11-11 上线时就漏了这一步，生产 root PAT 调
// /api/apiduo/trace/detail/:trace_id 直接 403。
//
// 两个入口都必须是 log:read：抽屉展示的是日志行那条请求的 prompt/answer，
// 与 /api/log/ 同权限域；降级成 accessTokenAnyRule 等于让任何 token 都能
// 读别人的正文，这里钉住不许漂移。
func TestTraceRoutesDeclareScopes(t *testing.T) {
	cases := map[string]string{
		"GET /api/apiduo/trace/config":           "log:read",
		"GET /api/apiduo/trace/detail/:trace_id": "log:read",
	}

	for key, want := range cases {
		rule, declared := AccessTokenRouteRule(key)
		assert.True(t, declared, "%s must declare an access token rule", key)
		assert.Equal(t, want, rule.Scope(), key)
	}
}
