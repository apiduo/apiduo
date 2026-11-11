package router

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"

	"github.com/gin-gonic/gin"
)

// APIDUO:ark-task-alias BEGIN
//
// 方舟形状的任务入口分两类：
//
//	/contents/generations/tasks          base URL 只填域名的客户端会打这条；
//	/v1/contents/generations/tasks       base URL 带 /v1 的客户端；
//	/api/v3/contents/generations/tasks   doubao 插件的原生入口。只有当请求模型不被任何
//	                                     任务插件声称时才转交任务中继，插件的既有行为不变。
//
// 前两条是静态路由：不注册的话会落到 SPA 兜底，返回 **200 的 HTML** ——
// 客户端把 200 当成功、再去解 JSON，报错信息与真实原因完全对不上。
const (
	apiduoArkAliasSubmitPath = "/api/v3/contents/generations/tasks"
	apiduoArkRelaySubmitPath = constant.ApiduoTaskRelayPath
	apiduoArkRelayRootPath   = "/v1/contents/generations/tasks"
	apiduoArkBareRootPath    = "/contents/generations/tasks"
	apiduoArkAliasReentryKey = "apiduo_ark_alias_reentry"
)

// SetApiduoArkTaskAliasRoutes registers the gateway-owned Ark-shaped task paths.
func SetApiduoArkTaskAliasRoutes(router *gin.Engine) {
	// 两条根路径打的是同一套处理器：客户端把 base URL 写成域名还是域名+/v1，
	// 都该拿到同样的任务号与同样的查询响应。
	for _, root := range []string{apiduoArkRelayRootPath, apiduoArkBareRootPath} {
		router.POST(root, apiduoArkSubmitHandlers()...)
		router.GET(root+"/:task_id",
			middleware.RouteTag("relay"),
			apiduoArkFetchPathRewrite(),
			middleware.TokenAuth(),
			middleware.Distribute(),
			// 方舟入口回方舟任务对象，不走网关的 {code,data} 包装。
			controller.ApiduoArkTaskFetch,
		)
	}
}

// apiduoArkSubmitHandlers mirrors the shared-endpoint chain of
// POST /v1/video/generations so a plugin-owned model keeps its native
// handling here as well.
func apiduoArkSubmitHandlers() []gin.HandlerFunc {
	return []gin.HandlerFunc{
		middleware.RouteTag("relay"),
		middleware.TokenAuth(),
		middleware.SystemPerformanceCheck(),
		// 必须在 PinTaskPluginEndpoint 之前：插件会把 body 原样带去上游。
		ApiduoArkCallbackGuard(),
		middleware.PinTaskPluginEndpoint(),
		middleware.TaskPluginEndpointOnly(middleware.ModelRequestRateLimit()),
		middleware.PrepareTaskPluginEndpoint(),
		middleware.Distribute(),
		func(c *gin.Context) {
			controller.RelayTaskPluginEndpoint(c, controller.RelayTask)
		},
	}
}

// apiduoArkFetchPathRewrite renames the alias query path to the relay one.
// Task distribution and the response builder key off that path, while the
// already-parsed :task_id parameter is unaffected.
func apiduoArkFetchPathRewrite() gin.HandlerFunc {
	return func(c *gin.Context) {
		taskID := c.Param("task_id")
		if taskID == "" {
			c.Next()
			return
		}
		target := apiduoArkRelaySubmitPath + "/" + taskID
		c.Request.URL.Path = target
		c.Request.URL.RawPath = ""
		// 与 apiduoArkReenter 保持一致：查询串留在 RequestURI 里。
		// 只改 Path 会让日志与 RequestURI 看到两个不同的地址，排查时很难认。
		if c.Request.URL.RawQuery != "" {
			c.Request.RequestURI = target + "?" + c.Request.URL.RawQuery
		} else {
			c.Request.RequestURI = target
		}
		c.Next()
	}
}

// ApiduoArkAliasDispatcher hands the plugin-native Ark path to the task relay
// when no task plugin claims it: by model on submit, by recorded task platform
// on query. Every other request keeps the original plugin dispatch, including
// anything this layer cannot identify.
func ApiduoArkAliasDispatcher(engine *gin.Engine, next gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetBool(apiduoArkAliasReentryKey) {
			next(c)
			return
		}
		switch c.Request.Method {
		case http.MethodPost:
			if c.Request.URL.Path != apiduoArkAliasSubmitPath {
				next(c)
				return
			}
			modelName, readable := apiduoArkRequestedModel(c)
			// A channel that declares this model outranks a plugin that only
			// claims the same name. Without this check the plugin wins every
			// overlap and the channel is unreachable.
			if readable && modelName != "" && model.ApiduoAdvancedAsyncServes(modelName) {
				if apiduoArkRejectCallbackURL(c, "") {
					return
				}
				apiduoArkReenter(engine, c, apiduoArkRelaySubmitPath)
				return
			}
			if !readable || modelName == "" || apiduoArkPluginClaimsModel(modelName) {
				next(c)
				return
			}
			apiduoArkReenter(engine, c, apiduoArkRelaySubmitPath)
		case http.MethodGet:
			taskID, ok := apiduoArkAliasTaskID(c.Request.URL.Path)
			if !ok || !apiduoArkTaskUsesRelay(taskID) {
				next(c)
				return
			}
			// 重入方舟形状的查询路由，不是通用的 /v1/video/generations：
			// 客户端打的是方舟路径，响应形状必须跟着路径走。
			apiduoArkReenter(engine, c, apiduoArkRelayRootPath+"/"+taskID)
		default:
			next(c)
		}
	}
}

func apiduoArkAliasTaskID(path string) (string, bool) {
	remainder, found := strings.CutPrefix(path, apiduoArkAliasSubmitPath+"/")
	if !found {
		return "", false
	}
	remainder = strings.Trim(remainder, "/")
	if remainder == "" || strings.Contains(remainder, "/") {
		return "", false
	}
	return remainder, true
}

// apiduoArkTaskUsesRelay reports whether the task was served by the
// advanced-async channel type, which no task plugin can query.
func apiduoArkTaskUsesRelay(taskID string) bool {
	platform, ok := model.ApiduoTaskPlatform(taskID)
	return ok && platform == strconv.Itoa(constant.ChannelTypeAdvancedAsync)
}

func apiduoArkReenter(engine *gin.Engine, c *gin.Context, target string) {
	c.Set(apiduoArkAliasReentryKey, true)
	c.Request.URL.Path = target
	c.Request.URL.RawPath = ""
	if c.Request.URL.RawQuery != "" {
		c.Request.RequestURI = target + "?" + c.Request.URL.RawQuery
	} else {
		c.Request.RequestURI = target
	}
	// 走 NoRoute 进来时，状态码在处理器执行前就被预置成 404 了。重入的处理器
	// 只写 body 不碰状态码，于是客户端拿到「正常的 JSON + 404」—— SDK 会把
	// 一次成功的查询当成失败。先清回 200，让重入的链路自己决定最终状态。
	c.Status(http.StatusOK)
	engine.HandleContext(c)
	c.Abort()
}

// apiduoArkPluginClaimsModel reports whether a task plugin owns the model name.
// An empty or unreadable model never claims it here: the plugin dispatcher owns
// that validation, and rerouting an unidentifiable request would change which
// error the client sees.
func apiduoArkPluginClaimsModel(modelName string) bool {
	generation := jsplugin.DefaultRegistry.Generation()
	if generation == nil {
		return false
	}
	if strings.TrimSpace(modelName) == "" {
		return true
	}
	if _, claimed := generation.GetByModel(modelName); claimed {
		return true
	}
	return false
}

func apiduoArkPeekSubmitBody(c *gin.Context) ([]byte, bool) {
	seeker, err := common.GetRequestBody(c)
	if err != nil {
		return nil, false
	}
	reader, ok := seeker.(io.Reader)
	if !ok {
		return nil, false
	}
	raw, readErr := io.ReadAll(reader)
	if _, seekErr := seeker.Seek(0, io.SeekStart); seekErr != nil {
		return nil, false
	}
	if readErr != nil {
		return nil, false
	}
	// Peeking consumed and closed the wire body; restore it so the rerouted
	// request reaches the task relay with its payload intact.
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	return raw, true
}

func apiduoArkRequestedModel(c *gin.Context) (string, bool) {
	raw, ok := apiduoArkPeekSubmitBody(c)
	if !ok {
		return "", false
	}
	var payload struct {
		Model string `json:"model"`
	}
	if err := common.Unmarshal(raw, &payload); err != nil {
		return "", false
	}
	return strings.TrimSpace(payload.Model), true
}

// apiduoArkRejectCallbackURL writes the Ark-shaped rejection for callback_url
// and reports whether it already answered.
//
// 网关只发放自己的 task_xxxx，而方舟的 callback_url 会带**上游**任务号回调
// 客户端，客户端拿它查不到任何任务。与 doubao 插件一致直接拒绝。错误信封
// 必须是方舟形状 {error:{code,message}}：这里在 controller 之前写响应，
// 就是为了不落到网关的 {code,message} TaskError。
//
// protocol 为空时按模型查渠道；dispatcher 已知协议就直接传，避免同一模型
// 被多条不同协议的渠道声明时误判。
func apiduoArkRejectCallbackURL(c *gin.Context, protocol string) bool {
	raw, ok := apiduoArkPeekSubmitBody(c)
	if !ok {
		return false
	}
	var payload struct {
		Model       string  `json:"model"`
		CallbackURL *string `json:"callback_url"`
	}
	if err := common.Unmarshal(raw, &payload); err != nil || payload.CallbackURL == nil {
		return false
	}
	if protocol == "" {
		claimedProtocol, claimed := model.ApiduoAsyncModelProtocol(payload.Model)
		if !claimed {
			return false
		}
		protocol = claimedProtocol
	}
	if protocol != constant.ApiduoAsyncProtocolArk {
		return false
	}
	c.AbortWithStatusJSON(http.StatusBadRequest, map[string]any{
		"error": map[string]any{
			"code":    "InvalidParameter",
			"message": "callback_url is not supported; poll the task instead",
		},
	})
	return true
}

// ApiduoArkCallbackGuard is the middleware form for the static submit chains.
func ApiduoArkCallbackGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPost {
			c.Next()
			return
		}
		if apiduoArkRejectCallbackURL(c, "") {
			return
		}
		c.Next()
	}
}

// APIDUO:ark-task-alias END
