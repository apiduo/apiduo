package mcp

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/middleware"
)

// 管理面每条路由都必须在 accessTokenRouteRules 里有 scope 声明。
//
// 漏一条既不会编译失败，也不影响浏览器使用 —— 只有 PAT / 脚本会拿到
// ACCESS_TOKEN_ROUTE_UNDECLARED，而且是在线上才发现。adopt 上线时就是这样：
// 面板点得通，PAT 一调 403，回头补的声明。
//
// 这个断言的价值不在今天，而在下一次有人加路由：忘了加 scope，
// `go test ./mcp/` 当场红。
func TestAdminRoutesDeclareAccessTokenScope(t *testing.T) {
	routes := adminRoutes()
	if len(routes) == 0 {
		t.Fatal("路由表是空的，测试没有意义")
	}
	for _, rt := range routes {
		key := rt.Method + " /api/mcp" + rt.Path
		if _, declared := middleware.AccessTokenRouteRule(key); !declared {
			t.Errorf("路由缺少 scope 声明，PAT 调它会 403：%s", key)
		}
	}
}

// 反过来也看一眼：scope 表里别留已经删掉的 MCP 路由。
//
// 只记日志不失败 —— 表里还有大量非 MCP 路由，这一侧做不了强断言，
// 但能让人在输出里看到「声明的接口已经不存在了」。
func TestAccessTokenScopeHasNoStaleMCPRoutes(t *testing.T) {
	known := make(map[string]bool, len(adminRoutes()))
	for _, rt := range adminRoutes() {
		known[rt.Method+" /api/mcp"+rt.Path] = true
	}
	for _, key := range middleware.AccessTokenRouteRuleKeys() {
		if strings.Contains(key, " /api/mcp/") && !known[key] {
			t.Logf("scope 表里的路由已不在 adminRoutes：%s", key)
		}
	}
}
