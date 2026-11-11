package constant

// APIDUO:image-async BEGIN
//
// 高级异步渠道有两套内置客户端形状（advanced_async.protocol）：
//
//	images  POST <incoming> 拿任务号，GET <incoming>/{id} 拿标准 Images 响应；
//	ark     火山方舟任务形状，客户端轮询任务对象本身。
//
// 入口路径是每条渠道自己声明的（incoming_paths），下面的常量只是**默认值**
// 与网关内部落地点的唯一来源：分发器、控制器、路由和前端预设都引用它。

// ApiduoClientPathKey carries the path the client actually called across the
// dispatcher's rewrite onto the internal relay path.
//
// It lives in Request.Context, not in gin's Keys: the alias entries re-enter
// through engine.HandleContext, whose reset() clears c.Keys — a value stored
// with c.Set would be gone before the handler that writes the log runs.
type ApiduoClientPathKey struct{}

const (
	// ApiduoImageAsyncIncomingPath is the default client path of the built-in
	// Images async protocol.
	//
	// 它特意不是 /v1/images/generations：那条路径是**同步**的 OpenAI Images
	// 入口，一旦被异步渠道接管，每个同步图像请求都要先过一遍异步配置。
	ApiduoImageAsyncIncomingPath = "/v1/images/generations/async"

	// ApiduoImageAsyncFetchPath is the internal landing point of the poll
	// half. Only GET is registered on it, so the synchronous POST that shares
	// the same path keeps its own handler untouched.
	ApiduoImageAsyncFetchPath = "/v1/images/generations"

	// ApiduoTaskRelayPath is the gateway-internal task submit/query path. Every
	// advanced-async channel serves it: the dispatcher rewrites a client path
	// onto it before distribution.
	ApiduoTaskRelayPath = "/v1/video/generations"

	// Protocol names persisted in advanced_async.protocol.
	ApiduoAsyncProtocolImages = "images"
	ApiduoAsyncProtocolArk    = "ark"
	ApiduoAsyncProtocolCustom = "custom"
)

// APIDUO:image-async END
