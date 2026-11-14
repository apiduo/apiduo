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
	Name     string
	Generate string // 相对 base_url
	Query    string // {id} 为占位；空表示该协议没有任务查询
}

// transports 按产物类型索引：端点由 kind 决定，与上游厂商无关 —— 图片和视频
// 都走方舟，只是方舟自己给这两种产物开了两套接口。
//
// 🔴 图片只有这一个入口：`/v1/images/generations`，文生图与图生图共用
// （图生图多一个 `image` 字段）。不要为图生图加 edits 分支，也不要因为上游
// 是 gpt-image 就在协议层另开一条路 —— gpt-image 已适配主端点，渠道内部
// 是否转成 edits 是渠道的事，客户端看到的永远只有这一个端点。
var transports = map[string]transport{
	"image": {
		Name:     "image",
		Generate: "/images/generations",
		// 图片接口在 apiduo 上同步返回完整结果，没有任务可查。
	},
	"video": {
		Name:     "video",
		Generate: "/contents/generations/tasks",
		Query:    "/contents/generations/tasks/{id}",
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
