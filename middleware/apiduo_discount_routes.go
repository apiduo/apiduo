package middleware

// 个人折扣接口的 access token 声明（APIDUO:discount）。
//
// 为什么单独放一个文件，而不是直接往上游的 access_token_routes.go 里加两行：
//  1. 那张表在上游文件里，每加一行都要计入上游合并面预算，而预算只允许下调；
//  2. 声明本身是纯追加，用 init 注册与直接写在表里等价，读路径不变。
//
// 背景：折扣接口 2026-11-08 上线时漏了这一步，生产实测 PAT 调
// GET /api/user/2/discounts 返回 403 ACCESS_TOKEN_ROUTE_UNDECLARED，
// 同时每次调用都会打一条 "access token route is not declared" 的 SysError。
// 回归由 middleware/access_token_discount_test.go 钉住。
func init() {
	accessTokenRouteRules["GET /api/user/:id/discounts"] = accessTokenScopeRule("user:read")
	accessTokenRouteRules["PUT /api/user/:id/discounts"] = accessTokenScopeRule("user:write")
}
