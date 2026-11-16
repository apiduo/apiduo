package middleware

// 公告管理接口的 access token 声明（APIDUO:announcement:routes）。
//
// 与 apiduo_mcp_routes.go 同一模式：用 init 追加，不动上游那张表。
// 漏声明的后果是浏览器会话点得通、PAT 与 agent token 一律 403
// ACCESS_TOKEN_ROUTE_UNDECLARED —— 这正是 MCP 控制面 2026-11-10 上线时踩过的坑。
//
// 路径必须与 gin 的 FullPath 完全一致：Group("/announcements") 上注册 ""，
// 得到的是 /api/announcements，没有尾斜杠。
func init() {
	accessTokenRouteRules["GET /api/announcements"] = accessTokenScopeRule("announcement:read")
	accessTokenRouteRules["POST /api/announcements"] = accessTokenScopeRule("announcement:write")
	accessTokenRouteRules["PATCH /api/announcements/:id"] = accessTokenScopeRule("announcement:write")
	accessTokenRouteRules["DELETE /api/announcements/:id"] = accessTokenScopeRule("announcement:write")
}
