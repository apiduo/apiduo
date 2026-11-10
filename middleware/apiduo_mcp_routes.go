package middleware

// MCP 控制面接口的 access token 声明（APIDUO:mcp:routes）。
//
// 为什么不直接往上游 access_token_routes.go 里加行：那张表在上游文件里，
// 每加一行都计入上游合并面预算（只允许下调），而声明是纯追加，用 init
// 注册与写在表里读路径完全等价。
//
// 背景：MCP 控制面 2026-11-10 上线时漏了这一步，生产实测 root PAT 调
// GET /api/mcp/overview 返回 403 ACCESS_TOKEN_ROUTE_UNDECLARED，且每次
// 调用都打一条 "access token route is not declared" 的 SysError。
// 与 2026-11-08 折扣接口是同一类缺陷，回归由 apiduo_mcp_routes_test.go 钉住。
//
// scope 取 model:read / model:write：MCP 管的是模型能力声明与错误规则，
// 与渠道/模型配置同属一个权限域。观察与审计读的是本模型的历史样本，跟随读权限。
func init() {
	accessTokenRouteRules["GET /api/mcp/overview"] = accessTokenScopeRule("model:read")
	accessTokenRouteRules["GET /api/mcp/candidates"] = accessTokenScopeRule("model:read")

	accessTokenRouteRules["GET /api/mcp/profiles"] = accessTokenScopeRule("model:read")
	accessTokenRouteRules["POST /api/mcp/profiles"] = accessTokenScopeRule("model:write")
	accessTokenRouteRules["GET /api/mcp/profiles/:id"] = accessTokenScopeRule("model:read")
	accessTokenRouteRules["PUT /api/mcp/profiles/:id"] = accessTokenScopeRule("model:write")
	accessTokenRouteRules["DELETE /api/mcp/profiles/:id"] = accessTokenScopeRule("model:write")
	accessTokenRouteRules["POST /api/mcp/profiles/:id/toggle"] = accessTokenScopeRule("model:write")
	accessTokenRouteRules["GET /api/mcp/profiles/:id/observations"] = accessTokenScopeRule("model:read")
	accessTokenRouteRules["GET /api/mcp/profiles/:id/audit"] = accessTokenScopeRule("model:read")

	accessTokenRouteRules["GET /api/mcp/error-rules"] = accessTokenScopeRule("model:read")
	accessTokenRouteRules["PUT /api/mcp/error-rules/:id"] = accessTokenScopeRule("model:write")
}
