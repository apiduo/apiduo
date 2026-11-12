package middleware

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// 声明表是 fail-closed 的：没登记的路由，任何 scoped token 都会被
// 403 ACCESS_TOKEN_ROUTE_UNDECLARED 挡掉，而且只有运行时才暴露。
// MCP 控制面 2026-11-10 上线时漏了这一步（生产实测 root PAT 调
// /api/mcp/overview 报 ACCESS_TOKEN_ROUTE_UNDECLARED），在这里钉死。
//
// 这份清单必须与 mcp/router.go 的 Register 一一对应：新增接口忘了登记，
// 这个测试不会红，但生产上 PAT 会 403 —— 所以改动 mcp/router.go 时，
// 这里是第二处要同步的地方。
func TestMCPRoutesDeclareScopes(t *testing.T) {
	cases := map[string]string{
		"GET /api/mcp/overview":                  "model:read",
		"GET /api/mcp/candidates":                "model:read",
		"GET /api/mcp/profiles":                  "model:read",
		"POST /api/mcp/profiles":                 "model:write",
		"GET /api/mcp/profiles/:id":              "model:read",
		"PUT /api/mcp/profiles/:id":              "model:write",
		"GET /api/mcp/profiles/by-model":         "model:read",
		"PUT /api/mcp/profiles/by-model":         "model:write",
		"DELETE /api/mcp/profiles/:id":           "model:write",
		"POST /api/mcp/profiles/:id/toggle":      "model:write",
		"GET /api/mcp/profiles/:id/observations": "model:read",
		"GET /api/mcp/profiles/:id/audit":        "model:read",
		"GET /api/mcp/error-rules":               "model:read",
		"PUT /api/mcp/error-rules/:id":           "model:write",
	}

	for key, want := range cases {
		rule, declared := AccessTokenRouteRule(key)
		assert.True(t, declared, "%s must declare an access token rule", key)
		assert.Equal(t, want, rule.Scope(), key)
	}
}
