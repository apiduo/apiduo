package middleware

// 运维台接口的 access token 声明（APIDUO:ops:routes）。
//
// 这 16 条路由从上线起就没有声明过，router 的
// TestAccessTokenRouteRulesCoverEveryDashboardRoute 一直是红的 ——
// 与 MCP 控制面 2026-11-10 那次是同一类缺陷：浏览器会话点得通，
// PAT 与 agent token 一律 403 ACCESS_TOKEN_ROUTE_UNDECLARED。
//
// 读全部跟随读权限；写动作（变更记录、班次、成本、事件处置）跟随写权限。
// ops scope 在 accessTokenStaticResources 里已经存在，不需要新增资源。
func init() {
	for _, path := range []string{
		"/api/ops/series",
		"/api/ops/breakdown",
		"/api/ops/clusters",
		"/api/ops/changes",
		"/api/ops/ratelimits",
		"/api/ops/margins",
		"/api/ops/costs",
		"/api/ops/shift",
		"/api/ops/incidents",
	} {
		accessTokenRouteRules["GET "+path] = accessTokenScopeRule("ops:read")
	}
	for _, path := range []string{
		"/api/ops/changes",
		"/api/ops/shift",
		"/api/ops/incidents",
		"/api/ops/incidents/:id/claim",
		"/api/ops/incidents/:id/close",
		"/api/ops/incidents/:id/assign",
	} {
		accessTokenRouteRules["POST "+path] = accessTokenScopeRule("ops:write")
	}
	accessTokenRouteRules["PUT /api/ops/costs"] = accessTokenScopeRule("ops:write")
}
