package mcp

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverVersion 只用于 serverInfo，不参与协议协商。
//
// 一定要跟随构建版本：客户端上报的 serverInfo 是排障时唯一能确定「连的是哪个
// 构建」的字段。写死 0.1.0 时，网关升到 v0.0.141 还报 0.1.0，等于没有信息。
func serverVersion() string {
	if v := strings.TrimSpace(common.Version); v != "" {
		return "mcp/" + v
	}
	return "mcp/dev"
}

// serverInstructions 随 initialize 下发给客户端，被注入模型上下文。
//
// 这是 MCP 自带的使用引导：接入页要人去看，工具描述只在 agent 决定调用时
// 才读得到，而 instructions 在连上那一刻就已经在上下文里了。所以「先查
// models.list」「每次生成都计费」「视频要轮询」这几件事写在这里，比写在
// 任何文档里都更早生效。
//
// 用英文：客户端与模型实现以英文为主，指令遵循更稳；模型回应用户时仍用
// 用户的语言。保持简短 —— 它会占用每一轮对话的上下文。
const serverInstructions = `Generate images and videos from a prompt.

Workflow
1. Call models.list before the first generation unless the model is already known.
   It carries each model's summary, price and accepted parameters. Model ids must
   come from that list — never invent or guess one.
2. Call image.generate or video.generate. Pass the model's declared parameters at
   the top level, not inside extra. reference_images turns the call into
   image-to-image or image-to-video; omit it for text-to-image.
3. Video is usually asynchronous: the call returns task_id, then poll task.get
   using poll_after_ms.

Rules
- model may be omitted; the default is used and named in the response.
- Every generation is billed. Say which model you picked and roughly what it costs.
- If the request is vague, ask one clarifying question before spending money.
- On error read code and retry_action: none = stop, call_models_list = pick another
  model, wait = retry after retry_after_ms.
- Give the user the output URL; never report success without one.

Preferences
- If you have a memory mechanism, use it: when the user states a lasting preference
  (a default model, a usual aspect ratio, a recurring style, a budget cap), record it
  and follow it in later sessions instead of asking again.
- Record only what sounds durable. A one-off "use flux this time" is not a preference,
  and remembering it will make later calls wrong.
- A remembered preference never overrides the model list: confirm the model still
  exists and the price before spending money.`

var (
	protoOnce    sync.Once
	protoHandler http.Handler
)

// protocolHandler 装配 MCP 协议端点。
//
// Stateless + JSONResponse：
//   - Stateless 不读写 Mcp-Session-Id，任何副本都能处理任何请求，
//     不用为会话做粘性负载均衡。
//   - JSONResponse 让响应是普通 JSON 而不是 SSE，反代与日志都好处理。
//
// 代价：不支持服务端主动推送（sampling / notifications）。本项目只用 tools，
// 不需要那些能力。
func protocolHandler() http.Handler {
	protoOnce.Do(func() {
		server := mcpsdk.NewServer(&mcpsdk.Implementation{
			Name:    "apiduo-mcp",
			Version: serverVersion(),
		}, &mcpsdk.ServerOptions{
			Instructions: serverInstructions,
			// 这个服务端所有「可缓存的结果」都是静态的：工具清单固定 4 个、
			// 没有 resources、能力声明也不随库变化。给 5 分钟让客户端别每个会话
			// 都重拉一次；能力清单（models）是工具返回值，不受这里影响。
			SetCacheable: func(_ context.Context, _ mcpsdk.Request, c *mcpsdk.Cacheable) {
				c.TTLMs = 5 * 60 * 1000
				c.CacheScope = "public"
			},
			// 显式声明能力，不用 SDK 默认值：默认会带 logging（2026-07-28 已废弃），
			// 而这个服务端在 JSONResponse 下根本发不出 notifications/message。
			// 声明一个永远兑现不了的能力，只会让客户端在排障时多走一条死路。
			Capabilities: &mcpsdk.ServerCapabilities{
				Tools: &mcpsdk.ToolCapabilities{ListChanged: true},
			},
		})
		registerTools(server, &toolDeps{up: newUpClient()})
		protoHandler = mcpsdk.NewStreamableHTTPHandler(
			func(*http.Request) *mcpsdk.Server { return server },
			&mcpsdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
		)
	})
	return protoHandler
}

// injectBearer 把 Authorization 里的令牌放进 context，供工具层调用上游时使用。
//
// 这里不再做「没带令牌就回落服务身份」的兜底：apiduo 的 TokenAuthReadOnly
// 已经在更外层拒绝了空令牌，MCP 层不该有第二条身份路径。
func injectBearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if key, ok := bearerToken(r); ok {
			r = r.WithContext(withAPIKey(r.Context(), key))
		}
		next.ServeHTTP(w, r)
	})
}

// bearerToken 解析 Authorization 头。
//
// 保留 `Bearer ` 前缀判断的大小写不敏感：不同客户端写法不一致，
// 但「带了头但不是 Bearer」与「没带头」必须区分开。
func bearerToken(r *http.Request) (string, bool) {
	raw := strings.TrimSpace(r.Header.Get("Authorization"))
	if raw == "" {
		return "", false
	}
	if len(raw) > 7 && strings.EqualFold(raw[:7], "bearer ") {
		raw = strings.TrimSpace(raw[7:])
	}
	if raw == "" {
		return "", false
	}
	return raw, true
}
