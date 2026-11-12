package mcp

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// Register 挂载 MCP 的全部入口：管理 API 与协议端点。
//
// 这是与 apiduo 唯一的接入点。router/main.go 里只需一行调用，
// 并且用 // APIDUO:mcp BEGIN/END 包裹，sync upstream 时按标记重放。
//
// 两个入口刻意分开鉴权：
//   - /api/mcp/*  管理 API，走 AdminAuth（session）
//   - /mcp        协议端点，走 TokenAuthReadOnly（用户 key）
//
// 后台界面只有前端「系统设置 → 扩展 → MCP」那一套。这里曾自带一份静态
// 面板（/mcp/admin），重构后它的协议选项与库里的值对不上，留着就是会写坏
// 数据的入口，已删除。
func Register(engine *gin.Engine) {
	// 建表失败不能让整个网关起不来：MCP 是增值能力，不是计费主链路。
	if err := Migrate(); err != nil {
		common.SysError("[mcp] 建表失败，管理 API 将不可用: " + err.Error())
	}

	api := engine.Group("/api/mcp")
	api.Use(middleware.AdminAuth())
	{
		api.GET("/overview", apiOverview)
		api.GET("/candidates", apiCandidates)

		api.GET("/profiles", apiListProfiles)
		api.POST("/profiles", apiCreateProfile)
		api.GET("/profiles/:id", apiGetProfile)
		api.PUT("/profiles/:id", apiUpdateProfile)
		// 按业务键读写：agent 纳管只认 model_id，不该先拉列表换自增 id。
		// 与上面的 :id 不冲突 —— gin 的静态段永远优先于参数段命中。
		api.GET("/profiles/by-model", apiGetProfileByModelId)
		api.PUT("/profiles/by-model", apiUpsertProfile)
		api.DELETE("/profiles/:id", apiDeleteProfile)
		api.POST("/profiles/:id/toggle", apiToggleProfile)
		api.GET("/profiles/:id/observations", apiObservations)
		api.GET("/profiles/:id/audit", apiAudit)

		api.GET("/error-rules", apiListErrorRules)
		api.PUT("/error-rules/:id", apiUpdateErrorRule)
	}

	// MCP 协议端点。TokenAuthReadOnly 只验令牌有效性，不在 MCP 层扣额度；
	// 真正的计费发生在工具调用打到 /v1 之后，由 relay 负责。
	//
	// 必须挂在中间件之后：空令牌在入口就被拒，不会出现「没带 key 却用了服务身份」
	// 的回落路径 —— 那是公网部署最容易出事的地方。
	proto := injectBearer(protocolHandler())
	engine.Any("/mcp", middleware.TokenAuthReadOnly(), captureCaller(), gin.WrapH(proto))

	// 旧版自带的静态面板已经删了，地址留一条跳转：外部书签和脚本直接打
	// 这个地址时，落在「系统设置 → 扩展 → MCP」而不是前端首页。
	engine.GET("/mcp/admin", redirectToSettings)
	engine.GET("/mcp/admin/", redirectToSettings)
}

// mcpSettingsPath 是前端里 MCP 后台的唯一入口。
const mcpSettingsPath = "/system-settings/site/mcp"

func redirectToSettings(c *gin.Context) {
	c.Redirect(http.StatusMovedPermanently, mcpSettingsPath)
}

// captureCaller 把令牌归属搬进 request context，供协议层写日志时归属到人。
//
// 必须挂在 TokenAuthReadOnly 之后：身份就来自它设置的 id / token_id。
// 协议端点是 gin.WrapH 包出来的 http.Handler，那之后 gin.Context 已经不可见，
// 所以这一搬是唯一时机。
func captureCaller() gin.HandlerFunc {
	return func(c *gin.Context) {
		caller := mcpCaller{UserId: c.GetInt("id"), TokenId: c.GetInt("token_id")}
		if caller.UserId > 0 {
			// fromDB=false：走用户缓存。为一条旁路日志开一次库查询不值。
			caller.Username, _ = model.GetUsernameById(caller.UserId, false)
		}
		c.Request = c.Request.WithContext(withCaller(c.Request.Context(), caller))
		c.Next()
	}
}
