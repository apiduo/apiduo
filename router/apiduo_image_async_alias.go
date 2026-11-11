package router

import (
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// APIDUO:image-async-alias BEGIN
//
// 高级异步渠道的客户端入口是**每条渠道自己声明**的（advanced_async
// .incoming_paths），因此没有静态路由表，统一落在 NoRoute 上：
//
//	POST <incoming>            提交，重入任务中继，立刻拿到任务号；
//	GET  <incoming>/{task_id}  轮询：images 协议回标准 Images 响应，
//	                           其它协议回任务对象。
//
// 图像异步的默认入口是 /v1/images/generations/async，**不是**同步的
// /v1/images/generations：后者属于图像中继，被异步渠道接管会让每一个
// 同步图像请求都取决于该渠道的异步配置。

// SetApiduoImageAsyncRoutes registers the poll half of the Images protocol and
// returns the inbound dispatcher wrapped around the plugin dispatcher.
//
// 客户端调的是 <incoming>/{task_id}，由 dispatcher 改写到这个内部落地点；
// 这里只注册 GET，同路径上的同步 POST 仍然归图像中继。
func SetApiduoImageAsyncRoutes(router *gin.Engine, pluginDispatcher gin.HandlerFunc) gin.HandlerFunc {
	router.GET(constant.ApiduoImageAsyncFetchPath+"/:task_id",
		middleware.RouteTag("relay"),
		middleware.TokenAuth(),
		controller.ApiduoImageAsyncTaskFetch,
	)
	return ApiduoAsyncIncomingDispatcher(router, pluginDispatcher)
}

// apiduoTaskActionHeader carries the task action across the re-entry that
// rewrites an image submission onto the task relay.
const apiduoTaskActionHeader = "X-Apiduo-Task-Action"

// ApiduoTaskActionHeaderBridge moves that header into the context key the task
// controller reads. It runs inside the relay chain because anything set before
// HandleContext is wiped by its context reset.
func ApiduoTaskActionHeaderBridge() gin.HandlerFunc {
	return func(c *gin.Context) {
		if action := strings.TrimSpace(c.Request.Header.Get(apiduoTaskActionHeader)); action != "" {
			c.Set("task_action", action)
			c.Request.Header.Del(apiduoTaskActionHeader)
		}
		c.Next()
	}
}

// ApiduoAsyncIncomingDispatcher hands a request on an operator-declared client
// path to the task relay, using the protocol that channel declares.
//
// 它只接管**有渠道认领**的路径：路径在渠道的 incoming_paths 里、且渠道声明
// 了请求的模型。任何读不出来或没人认领的请求都原样交给下一条链路 —— 越权
// 接管只会把客户端原本能拿到的错误换成另一种错误。
func ApiduoAsyncIncomingDispatcher(engine *gin.Engine, next gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetBool(apiduoArkAliasReentryKey) {
			next(c)
			return
		}
		switch c.Request.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch:
			modelName, readable := apiduoArkRequestedModel(c)
			if !readable || modelName == "" {
				next(c)
				return
			}
			protocol, claimed := model.ApiduoAsyncIncomingServes(c.Request.URL.Path, modelName)
			if !claimed {
				next(c)
				return
			}
			// 方舟协议在重入前就把 callback_url 拒掉：重入之后错误会落到
			// 网关的 TaskError 信封，那是 {code,message}，方舟客户端读不懂。
			if apiduoArkRejectCallbackURL(c, protocol) {
				return
			}
			if protocol == constant.ApiduoAsyncProtocolImages {
				// 动作在入口这一层就定下来。重入之后路径已经变成任务中继的，
				// 靠 body 形状猜会把「没有 content 数组的图像体」和「缺字段的
				// 视频体」混成一类。
				//
				// 走请求头而不是 context：HandleContext 内部的 c.reset() 会
				// 清空 c.Keys，重入前塞进 context 的值活不过那次重置。
				c.Request.Header.Set(apiduoTaskActionHeader, constant.TaskActionImageGeneration)
			}
			apiduoArkReenter(engine, c, constant.ApiduoTaskRelayPath)
		case http.MethodGet:
			basePath, taskID, ok := apiduoAsyncPollPath(c.Request.URL.Path)
			if !ok {
				next(c)
				return
			}
			protocol, claimed := model.ApiduoAsyncIncomingProtocol(basePath)
			if !claimed {
				next(c)
				return
			}
			// 任务号必须属于高级异步渠道：别的平台的同号任务不能借这条自定义
			// 路径被读到。images 由自己的查询控制器校验归属，先放行。
			if protocol != constant.ApiduoAsyncProtocolImages && !apiduoArkTaskUsesRelay(taskID) {
				next(c)
				return
			}
			if protocol == constant.ApiduoAsyncProtocolImages {
				apiduoArkReenter(engine, c, constant.ApiduoImageAsyncFetchPath+"/"+taskID)
				return
			}
			// 方舟协议即使是从渠道自定义路径进来的，也必须回方舟形状：
			// 通用任务查询回的是网关的 {code,data}，方舟 SDK 读不到 status。
			if protocol == constant.ApiduoAsyncProtocolArk {
				apiduoArkReenter(engine, c, apiduoArkRelayRootPath+"/"+taskID)
				return
			}
			apiduoArkReenter(engine, c, constant.ApiduoTaskRelayPath+"/"+taskID)
		default:
			next(c)
		}
	}
}

// apiduoAsyncPollPath splits "<incoming>/{task_id}" at the last slash. Task ids
// never contain one, and an incoming path may contain several.
func apiduoAsyncPollPath(path string) (string, string, bool) {
	index := strings.LastIndex(path, "/")
	if index <= 0 || index == len(path)-1 {
		return "", "", false
	}
	return path[:index], strings.TrimSpace(path[index+1:]), true
}

// APIDUO:image-async-alias END
