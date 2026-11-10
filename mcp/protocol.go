package mcp

// 协议族（transport）是「生成端点 + 查询端点」的绑定，三者必须同源。
//
// 只保留 apiduo 网关自有的两套形状 —— 这正是「两套协议兼容所有图片视频模型」
// 的落点：各厂商的差异由 apiduo 的渠道与 JS 插件吸收，MCP 层不需要知道。
//
// 不要在 MCP 层新增「某家厂商的形状」（例如方舟的 contents/generations/tasks）：
// 那会让 MCP 依赖网关的内部实现，插件一换就断。
type transport struct {
	Name     string
	Generate string // 相对 base_url
	Query    string // {id} 为占位；空表示该协议没有任务查询
}

var transports = map[string]transport{
	"openai_image": {
		Name:     "openai_image",
		Generate: "/images/generations",
		// 图片接口在 apiduo 上同步返回完整结果，没有任务可查。
	},
	"openai_video": {
		Name:     "openai_video",
		Generate: "/video/generations",
		Query:    "/video/generations/{id}",
	},
}

// ErrUnknownTransport 表示模型声明里写了不认识的协议。
type ErrUnknownTransport struct{ Name string }

func (e *ErrUnknownTransport) Error() string {
	return "未知协议: " + e.Name + "（可用: openai_image, openai_video）"
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
