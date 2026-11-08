package middleware

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// 路由声明表是 fail-closed 的：没登记的路由，任何 scoped token 调用都会被
// 403 ACCESS_TOKEN_ROUTE_UNDECLARED 挡掉，而且只在运行时才暴露。折扣接口
// 上线时就漏了这一步（2026-11-08 生产实测：PAT 调 /api/user/2/discounts 报
// ACCESS_TOKEN_ROUTE_UNDECLARED），所以在这里钉死。
func TestDiscountRoutesDeclareScopes(t *testing.T) {
	cases := map[string]string{
		"GET /api/user/:id/discounts": "user:read",
		"PUT /api/user/:id/discounts": "user:write",
	}

	for key, want := range cases {
		rule, declared := AccessTokenRouteRule(key)
		assert.True(t, declared, "%s must declare an access token rule", key)
		assert.Equal(t, want, rule.Scope(), key)
	}
}
