package mcp

import (
	"net/http"
	"strings"
	"sync"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// protocolVersion 只用于 initialize 的 serverInfo，不参与协议协商。
const protocolVersion = "0.1.0"

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
			Version: protocolVersion,
		}, nil)
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
