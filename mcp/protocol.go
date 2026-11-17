package mcp

// 协议族（transport）是「生成端点 + 查询端点」的绑定，三者必须同源。
//
// 两个端点都是 apiduo 自己的路由，上游统一是方舟：
//
//	图片  /v1/images/generations            方舟的图片接口本身就兼容这个形状
//	视频  /v1/contents/generations/tasks    方舟任务形状，见 router/apiduo_ark_task_alias.go
//
// 视频为什么不用 /v1/video/generations：那是 OpenAI 形状的别名，和方舟形状共用
// 同一套插件链路，但响应会把任务对象包进 {code,data}。协议既然对外叫 ark，
// 就用 ark 的形状 —— 少一层解包，任务号与状态直接可读。
type transport struct {
	Name string
	// Generate 是同步提交端点（相对 base_url）。
	Generate string
	// AsyncGenerate 是异步提交端点；空表示该协议没有异步形态。
	//
	// 图片的两半收同一个 body，只有提交路径与响应形状不同；视频两者同值，
	// 因为视频本来就是异步提交。这样选路只有「按形态换 Generate」一条分支，
	// 不必给视频加例外。
	//
	// 路径以 "^" 开头表示相对站点根（upClient.url 会剥掉它）：异步图片的
	// 客户端入口是 /async/v1/...，在 /v1 之外，用 baseURL 拼不出来。
	AsyncGenerate string
	Query         string // {id} 为占位；空表示该协议没有任务查询
}

// transports 按产物类型索引：端点由 kind 决定，与上游厂商无关 —— 图片和视频
// 都走方舟，只是方舟自己给这两种产物开了两套接口。
//
// 🔴 图片只有一个**请求**形状：`/v1/images/generations`，文生图与图生图共用
// （图生图多一个 `image` 字段）。响应有两种，由能力声明的 execution 决定：
// sync 直接回 data，async 回 task_id 并用同一个 kind 的 Query 轮询。
//
// 不要为图生图加 edits 分支，也不要因为上游是 gpt-image 就在协议层另开一条路
// —— gpt-image 已适配主端点，渠道内部是否转成 edits 是渠道的事。
// 也不要让客户端用参数在 sync / async 之间切换：那等于把协议选择交给调用方。
var transports = map[string]transport{
	"image": {
		Name:     "image",
		Generate: "/images/generations",
		// 异步半边用对外的客户端入口 /async/v1/images/generations，而不是
		// /v1/images/generations/async —— 后者是静态的兼容入口，网关注释里
		// 已把它称作 older entry。两者收到的响应一致，但 MCP 没有理由依赖
		// 一条准备下架的路由。
		//
		// 与同步半边收同一个 body，只是先回 202 任务号；查询端点也只属于
		// 异步形态 —— 同步请求从来不会有任务可查。
		AsyncGenerate: "^/async/v1/images/generations",
		Query:         "^/async/v1/images/generations/{id}",
	},
	"video": {
		Name:     "video",
		Generate: "/contents/generations/tasks",
		// 视频本来就是异步提交，两半同值只为让选路不出现第二个分支。
		AsyncGenerate: "/contents/generations/tasks",
		Query:         "/contents/generations/tasks/{id}",
	},
}

// ErrUnknownTransport 表示模型声明里写了不认识的协议。
type ErrUnknownTransport struct{ Name string }

func (e *ErrUnknownTransport) Error() string {
	// 区分「类型写错」与「类型合法但端点还没接」：能力注册允许先声明
	// files / search / audio，真调用时才需要说清是后者。
	if knownKinds[e.Name] {
		return e.Name + " 的生成端点尚未接入（当前可用: image, video）"
	}
	return "未知产物类型: " + e.Name + "（可用: " + kindList() + "）"
}

// withExecution 返回按执行形态解析后的端点。
//
// 同步与异步收同一个 body，差异只有提交路径与响应形状 —— 所以只换 Generate，
// Query 与 Name 都不动。async 为真但该协议没有异步半边时保持原样：那是声明
// 写错了，让上游按原路径回错误，比在这里静默改路更容易排查。
func (t transport) withExecution(async bool) transport {
	if async && t.AsyncGenerate != "" {
		t.Generate = t.AsyncGenerate
	}
	return t
}

func getTransport(name string) (transport, error) {
	if t, ok := transports[name]; ok {
		return t, nil
	}
	return transport{}, &ErrUnknownTransport{Name: name}
}

// queryPath 把上游任务键填进查询模板。
//
// 必须转义：任务键来自签名内容，虽然经过 HMAC 校验，但不该让含 "/" 的键改变请求路径。
func (t transport) queryPath(taskKey string) string {
	out := make([]byte, 0, len(t.Query)+len(taskKey))
	for i := 0; i < len(t.Query); i++ {
		if t.Query[i] == '{' && i+3 < len(t.Query) && t.Query[i:i+4] == "{id}" {
			out = append(out, []byte(escapePathSegment(taskKey))...)
			i += 3
			continue
		}
		out = append(out, t.Query[i])
	}
	return string(out)
}

// escapePathSegment 只转义会改变路径含义的字符，保留普通字符可读性。
func escapePathSegment(s string) string {
	const hex = "0123456789ABCDEF"
	var b []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '/' || c == '?' || c == '#' || c == '%' || c == ' ':
			b = append(b, '%', hex[c>>4], hex[c&15])
		default:
			b = append(b, c)
		}
	}
	return string(b)
}
