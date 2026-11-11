package router

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"
	// APIDUO:mcp:BEGIN
	mcpserver "github.com/QuantumNous/new-api/mcp"
	// APIDUO:mcp:END
	"github.com/QuantumNous/new-api/middleware"

	"github.com/gin-gonic/gin"
)

func SetRouter(router *gin.Engine, assets WebAssets) {
	SetApiRouter(router)
	SetDashboardRouter(router)
	SetRelayRouter(router)
	SetTaskPluginProtocolRouter(router)
	SetVideoRouter(router)
	// APIDUO:mcp:BEGIN
	mcpserver.Register(router)
	// APIDUO:mcp:END
	// APIDUO:ark-task-alias:BEGIN
	SetApiduoArkTaskAliasRoutes(router)
	// APIDUO:ark-task-alias:END
	SetTaskRouter(router)
	pluginDispatcher := SetPluginRouter(router)
	// APIDUO:ark-task-alias:BEGIN
	pluginDispatcher = ApiduoArkAliasDispatcher(router, pluginDispatcher)
	// APIDUO:ark-task-alias:END
	// APIDUO:image-async-alias:BEGIN
	// 渠道自己声明的入站端点没有静态路由，最外层接住它们；方舟原生路径由
	// 上一条 dispatcher 处理，两者按路径互斥。
	pluginDispatcher = SetApiduoImageAsyncRoutes(router, pluginDispatcher)
	// APIDUO:image-async-alias:END
	frontendBaseUrl := os.Getenv("FRONTEND_BASE_URL")
	if common.IsMasterNode && frontendBaseUrl != "" {
		frontendBaseUrl = ""
		common.SysLog(common.LogText("FRONTEND_BASE_URL is ignored on master node"))
	}
	if frontendBaseUrl == "" {
		SetWebRouter(router, assets, pluginDispatcher)
	} else {
		frontendBaseUrl = strings.TrimSuffix(frontendBaseUrl, "/")
		router.NoRoute(
			pluginDispatcher,
			middleware.RouteTag("web"),
			middleware.AccessTokenAudit(),
			func(c *gin.Context) {
				c.Redirect(http.StatusMovedPermanently, fmt.Sprintf("%s%s", frontendBaseUrl, c.Request.RequestURI))
			},
		)
	}
}
