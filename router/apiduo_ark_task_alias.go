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
// 方舟形状的任务入口有两个来源：
//
//	/v1/contents/generations/tasks       网关自有别名，始终走任务中继，
//	                                     于是 type=64（高级异步）渠道也能服务方舟形状的上游；
//	/api/v3/contents/generations/tasks   doubao 插件的原生入口。只有当请求模型不被任何
//	                                     任务插件声称时才转交任务中继，插件的既有行为不变。
const (
	apiduoArkAliasSubmitPath = "/api/v3/contents/generations/tasks"
	apiduoArkRelaySubmitPath = "/v1/video/generations"
	apiduoArkRelayRootPath   = "/v1/contents/generations/tasks"
	apiduoArkAliasReentryKey = "apiduo_ark_alias_reentry"
)

// SetApiduoArkTaskAliasRoutes registers the gateway-owned Ark-shaped task path.
func SetApiduoArkTaskAliasRoutes(router *gin.Engine) {
	router.POST(apiduoArkRelayRootPath, apiduoArkSubmitHandlers()...)
	router.GET(apiduoArkRelayRootPath+"/:task_id",
		middleware.RouteTag("relay"),
		apiduoArkFetchPathRewrite(),
		middleware.TokenAuth(),
		middleware.Distribute(),
		controller.RelayTaskFetch,
	)
}

// apiduoArkSubmitHandlers mirrors the shared-endpoint chain of
// POST /v1/video/generations so a plugin-owned model keeps its native
// handling here as well.
func apiduoArkSubmitHandlers() []gin.HandlerFunc {
	return []gin.HandlerFunc{
		middleware.RouteTag("relay"),
		middleware.TokenAuth(),
		middleware.SystemPerformanceCheck(),
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
		c.Request.RequestURI = target
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
			apiduoArkReenter(engine, c, apiduoArkRelaySubmitPath+"/"+taskID)
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

func apiduoArkRequestedModel(c *gin.Context) (string, bool) {
	seeker, err := common.GetRequestBody(c)
	if err != nil {
		return "", false
	}
	reader, ok := seeker.(io.Reader)
	if !ok {
		return "", false
	}
	raw, readErr := io.ReadAll(reader)
	if _, seekErr := seeker.Seek(0, io.SeekStart); seekErr != nil {
		return "", false
	}
	if readErr != nil {
		return "", false
	}
	// Peeking consumed and closed the wire body; restore it so the rerouted
	// request reaches the task relay with its payload intact.
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))

	var payload struct {
		Model string `json:"model"`
	}
	if err := common.Unmarshal(raw, &payload); err != nil {
		return "", false
	}
	return strings.TrimSpace(payload.Model), true
}

// APIDUO:ark-task-alias END
