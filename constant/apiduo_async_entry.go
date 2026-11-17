package constant

import "github.com/QuantumNous/new-api/pkg/endpoint"

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
	// ApiduoImageAsyncIncomingPath is the client path of the built-in Images
	// async protocol, and also its submit landing route.
	//
	// 它特意不是 /v1/images/generations：那条路径是**同步**的 OpenAI Images
	// 入口，一旦被异步渠道接管，每个同步图像请求都要先过一遍异步配置。
	//
	// The name lives in pkg/endpoint because the endpoint table is what decides
	// a request is asynchronous; this is the alias the router registers.
	ApiduoImageAsyncIncomingPath = endpoint.AsyncImageSubmitPath

	// ApiduoImageAsyncFetchPath is the internal landing point of the poll
	// half. Only GET is registered on it, so the synchronous POST that shares
	// the same path keeps its own handler untouched.
	ApiduoImageAsyncFetchPath = "/v1/images/generations"

	// ApiduoTaskRelayPath is the gateway-internal submit/query path for the
	// non-Images advanced-async protocols (ark and custom). The dispatcher
	// rewrites those client paths onto it before distribution.
	//
	// Images does not use it: an image task that re-entered this video path
	// would be logged, presented and counted as video work. Its landing route
	// is endpoint.AsyncImageSubmitPath.
	ApiduoTaskRelayPath = "/v1/video/generations"

	// Protocol names persisted in advanced_async.protocol.
	//
	// Images is the built-in endpoint's shape as well, so its name has one
	// definition in pkg/endpoint and this is the alias the channel
	// configuration reads.
	ApiduoAsyncProtocolImages = endpoint.AsyncProtocolImages
	ApiduoAsyncProtocolArk    = "ark"
	ApiduoAsyncProtocolCustom = "custom"
)

// APIDUO:image-async END
