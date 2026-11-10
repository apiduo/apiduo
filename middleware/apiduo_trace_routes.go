package middleware

// 轨迹抽屉接口的 access token 声明（APIDUO:trace:routes）。
//
// 为什么不直接写进上游 access_token_routes.go 的 APIDUO:trace 标记块：
// 那张表在上游文件里，每加一行都计入上游合并面预算（只允许下调），而声明
// 本身是纯追加 —— 用 init 注册与写在表里读路径完全等价。同 2026-11-08 的
// 折扣接口、2026-11-10 的 MCP 控制面，两处都是这么处理的。
//
// 背景：轨迹列 2026-11-11 上线时，config 那一行随列一起登记了，detail 漏了
// —— 于是任何 scoped token（含 root 的 nap_ PAT）调
// GET /api/apiduo/trace/detail/:trace_id 都拿 403 ACCESS_TOKEN_ROUTE_UNDECLARED，
// 每次调用还会打一条 "access token route is not declared" 的 SysError。
// 这直接把「脚本自检抽屉」堵死：验收只能靠人工点浏览器。
//
// scope 取 log:read：抽屉渲染的就是日志行里那条请求的 prompt/answer，与
// GET /api/log/、GET /api/apiduo/trace/config 同权限域，不额外放大授权面。
// 回归由 apiduo_trace_routes_test.go 钉住。
func init() {
	accessTokenRouteRules["GET /api/apiduo/trace/detail/:trace_id"] = accessTokenScopeRule("log:read")
}
