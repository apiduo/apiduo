package router

// 自有路由的注册表。
//
// `api-router.go` 是上游文件，每插一个标记块就多一处未来要解冲突的位置。
// 这里把「加一组新接口」的成本从「往上游文件里插一块」降到「往切片里加一行」：
// `api-router.go` 里只剩一个固定挂接点，与功能数量无关。
//
// 存量的三块（`/option` 批量、`/discount`、审计令牌选项）没有搬进来 —— 它们挂在
// 深层 group 内，依赖父 group 的中间件，搬出来等于手工重建一遍继承链，风险大于收益。
// 那三块留在原处，但不再是增量模式。
//
// 新功能一律往 `apiduoModules` 里加，不要在 `api-router.go` 里新开块。

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"

	"github.com/gin-gonic/gin"
)

// apiduoModule 是一组自有关路由。
type apiduoModule struct {
	// name 与标记块里的 feature 名一致，只用于阅读与排错，不参与路由。
	name     string
	register func(*gin.RouterGroup)
}

// apiduoModules 的顺序就是注册顺序。
//
// 用显式切片而不是 init() 注册：init 的执行顺序由 import 顺序决定，等于把
// 「谁先注册」交给编译器；而这个顺序要能在代码里一眼读到。
var apiduoModules = []apiduoModule{
	{"agent-key", registerApiduoAgentKeys},
	{"appearance", registerApiduoAppearance},
	{"dynamic-nav", registerApiduoDynamicNav},
	{"sk-analytics", registerApiduoSkAnalytics},
	{"capacity-forecast", registerApiduoCapacityForecast},
	{"growth", registerApiduoGrowth},
	{"ops", registerApiduoOps},
	{"trace", registerApiduoTrace},
}

// registerApiduoRoutes 是 `api-router.go` 里的唯一挂接点。
//
// 必须在 apiRouter 的 Use(...) 之后调用：gzip、访问令牌审计、请求体清理、
// 全局限流四个中间件要先挂上，否则这里注册的路由会绕过它们。
func registerApiduoRoutes(g *gin.RouterGroup) {
	for _, module := range apiduoModules {
		module.register(g)
	}
}

func registerApiduoAgentKeys(g *gin.RouterGroup) {
	// Anonymous by design: these only mint a random nonce and verify a
	// signature, so there is nothing to protect with a token. Both are
	// rate-limited because they are reachable without credentials.
	//
	// 请求体上限每次请求现读配置（middleware 内部如此），所以这里再取一次
	// 与 api-router.go 里那个变量语义相同，不是快照。
	bodyLimit := middleware.AnonymousRequestBodyLimit()
	agentRoute := g.Group("/agent")
	{
		agentRoute.POST("/challenge", middleware.CriticalRateLimit(), middleware.DisableCache(), bodyLimit, controller.RequestAgentChallenge)
		agentRoute.POST("/exchange", middleware.CriticalRateLimit(), middleware.DisableCache(), bodyLimit, controller.ExchangeAgentSignature)

		// Managing keys is a browser action, so the same prefix carries a
		// session guard. Keeping it here rather than under /api/user means the
		// whole feature lives behind one path an operator can remember.
		agentKeyRoute := agentRoute.Group("/keys")
		agentKeyRoute.Use(middleware.DisableCache(), middleware.UserAuth())
		{
			agentKeyRoute.GET("", controller.ListAgentKeys)
			agentKeyRoute.POST("", middleware.CriticalRateLimit(), middleware.UserCriticalRateLimit("access-token"), controller.CreateAgentKey)
			agentKeyRoute.DELETE("/:id", middleware.CriticalRateLimit(), middleware.UserCriticalRateLimit("access-token"), controller.RevokeAgentKey)
		}
	}
}

func registerApiduoAppearance(g *gin.RouterGroup) {
	// Public by design: the effect is drawn on apiduo's own surfaces, some of
	// which render before sign-in. The reply carries a colour and three
	// numbers, so there is nothing here to keep from an anonymous visitor.
	g.GET("/appearance", controller.GetApiduoAppearance)
}

func registerApiduoDynamicNav(g *gin.RouterGroup) {
	// Stored in the existing option table (CustomNavApps), so no schema
	// change is involved. Reads are authenticated: entries may point at
	// internal hosts that must not be exposed to anonymous visitors.
	g.GET("/nav_apps", middleware.UserAuth(), controller.GetNavApps)
	// Editing uses RootAuth to match the option endpoints: these entries are
	// global appearance configuration, the same class as SidebarModulesAdmin.
	g.GET("/nav_apps/all", middleware.RootAuth(), controller.GetAllNavApps)
	g.PUT("/nav_apps", middleware.RootAuth(), controller.SaveNavApps)
}

func registerApiduoSkAnalytics(g *gin.RouterGroup) {
	// SK x model x user cross-section over quota_data. Admin-scoped to match
	// /api/data: that endpoint already exposes every user's usage to admins,
	// so this adds no new reach. The token key is masked server-side.
	g.GET("/data/sk", middleware.AdminAuth(), controller.GetSkQuotaDates)
	// Self-scoped variant for resellers: the owner boundary comes from the
	// session, so it is safe for every logged-in user.
	g.GET("/data/sk/self", middleware.UserAuth(), controller.GetSelfSkQuotaDates)
}

func registerApiduoCapacityForecast(g *gin.RouterGroup) {
	// Daily series feeding the capacity planner and the balance runway. Same
	// reach as /api/data/sk; /self is scoped to the caller's own rows.
	g.GET("/data/series", middleware.AdminAuth(), controller.GetUsageSeries)
	g.GET("/data/series/self", middleware.UserAuth(), controller.GetSelfUsageSeries)
	// Per-user balance + daily series for the retention board.
	g.GET("/data/user-balances", middleware.AdminAuth(), controller.GetUserBalanceSeries)
}

// registerApiduoOps 注册服务质量接口。
//
// 与 /growth 分开而不是塞进它的子路径：两者服务的时间尺度差两个数量级
// （分钟级排障 vs 天级运营），路径上也应该能看出来。
func registerApiduoOps(g *gin.RouterGroup) {
	opsRoute := g.Group("/ops")
	opsRoute.Use(middleware.AdminAuth())
	{
		opsRoute.GET("/series", controller.GetOpsSeries)
		opsRoute.GET("/breakdown", controller.GetOpsBreakdown)
		opsRoute.GET("/clusters", controller.GetOpsClusters)
		opsRoute.GET("/changes", controller.GetOpsChanges)
		opsRoute.POST("/changes", controller.CreateOpsChange)
		opsRoute.GET("/ratelimits", controller.GetOpsRateLimits)
		opsRoute.GET("/margins", controller.GetOpsMargins)
		opsRoute.GET("/costs", controller.GetOpsChannelCosts)
		opsRoute.PUT("/costs", controller.UpsertOpsChannelCost)
		opsRoute.GET("/shift", controller.GetOpsShift)
		opsRoute.POST("/shift", controller.StartOpsShift)
		opsRoute.GET("/incidents", controller.GetOpsIncidents)
		opsRoute.POST("/incidents", controller.CreateOpsIncident)
		opsRoute.POST("/incidents/:id/claim", controller.ClaimOpsIncident)
		opsRoute.POST("/incidents/:id/close", controller.CloseOpsIncident)
		opsRoute.POST("/incidents/:id/assign", controller.ReassignOpsIncident)
	}
}

func registerApiduoGrowth(g *gin.RouterGroup) {
	// 增长运营：只读派生视图 + 人工动作记录。全部走 AdminAuth，
	// 不进入转发路径，也不读写 users.group。
	growthRoute := g.Group("/growth")
	growthRoute.Use(middleware.AdminAuth())
	{
		growthRoute.GET("/overview", controller.GetGrowthOverview)
		growthRoute.GET("/users", controller.GetGrowthUsers)
		growthRoute.GET("/users/:id", controller.GetGrowthProfile)
		growthRoute.PUT("/users/:id/tier", controller.UpdateGrowthUserTier)
		growthRoute.POST("/users/:id/touches", controller.CreateGrowthTouch)
		growthRoute.GET("/alerts", controller.GetGrowthAlerts)
		growthRoute.POST("/alerts/:userId/:kind/status", controller.UpdateGrowthAlertStatus)
		growthRoute.GET("/batches", controller.GetGrowthBatches)
		growthRoute.POST("/batches", controller.CreateGrowthBatch)
		growthRoute.POST("/batches/:id/stop", controller.StopGrowthBatch)
		growthRoute.GET("/models", controller.GetGrowthModels)
		growthRoute.GET("/settings", controller.GetGrowthSettings)
		growthRoute.PUT("/settings", controller.UpdateGrowthSettings)
		growthRoute.POST("/recompute", controller.RecomputeGrowth)
	}
}

func registerApiduoTrace(g *gin.RouterGroup) {
	// Trace jump-link config for the log page. AdminAuth because
	// /api/status is public-by-design and must not leak the internal
	// Langfuse host.
	g.GET("/apiduo/trace/config", middleware.AdminAuth(), controller.GetApiduoTraceConfig)
	// detail/ is a literal segment on purpose: Gin panics when a static and a
	// wildcard segment share a level, so /apiduo/trace/:id cannot coexist with
	// /apiduo/trace/config.
	g.GET("/apiduo/trace/detail/:trace_id", middleware.AdminAuth(), controller.GetApiduoTraceDetail)
	// 图片外置后的唯一公开入口：URL 写在 span 正文里，Langfuse 是从另一个
	// 源、没有 apiduo 会话去加载它的。id 由 Langfuse 按内容生成、不可枚举。
	g.GET("/apiduo/trace/media/:media_id", controller.GetApiduoTraceMedia)
}
