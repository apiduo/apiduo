package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolDeps 是工具的依赖。只有上游客户端 —— 能力来自数据库，不需要注入。
type toolDeps struct {
	up *upClient
}

func registerTools(s *mcpsdk.Server, d *toolDeps) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "models.list",
		Description: "列出已注册且启用的模型及其能力（kind / execution / input / parameters）。" +
			"调用生成类工具前若不确定有哪些模型、参数取什么值，先查这里。" +
			"未出现在列表里的模型都不可调用。",
	}, d.listModels)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "image.generate",
		Description: "生成图片。不传 reference_images 即文生图，传了即图生图——模式由垫图推导，无需单独声明。" +
			"size/n/quality 留空即交给上游默认；从提示词推断时只能取 models.list 里声明的 enum 值，不要臆造。",
	}, d.generateImage)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "video.generate",
		Description: "生成视频。语义与 image.generate 相同，产物是视频；传 reference_images 可做图生视频。" +
			"多数视频模型是异步的，会立即返回 task_id，用 task.get 查询结果。" +
			"duration/resolution 留空即交给上游默认；从提示词推断时只能取 models.list 里声明的值。",
	}, d.generateVideo)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "task.get",
		Description: "查询异步任务状态。task_id 由 video.generate 返回，自包含签名，任何副本都能处理。" +
			"status=working 时按 poll_after_ms 再查；status=failed 时不要用同样参数重试。",
	}, d.getTask)
}

// ── models.list ─────────────────────────────────────────────────────────

type listInput struct {
	Kind string `json:"kind,omitempty" jsonschema:"按产物类型过滤：image / video。留空返回全部"`
}

type listOutput struct {
	Models []map[string]any `json:"models"`
	Count  int              `json:"count"`
	Note   string           `json:"note,omitempty"`
}

func (d *toolDeps) listModels(ctx context.Context, _ *mcpsdk.CallToolRequest, in listInput) (*mcpsdk.CallToolResult, listOutput, error) {
	views, err := listEnabledViews()
	if err != nil {
		return nil, listOutput{}, err
	}
	kind := strings.ToLower(strings.TrimSpace(in.Kind))
	out := make([]map[string]any, 0, len(views))
	for _, v := range views {
		if kind != "" && v.Kind != kind {
			continue
		}
		out = append(out, v.card())
	}
	res := listOutput{Models: out, Count: len(out)}
	if len(out) == 0 {
		// 空列表最常见的两种原因是「没注册」和「注册了但停用」，直接说清楚，
		// 免得 agent 反复调用同一个工具试探。
		res.Note = "没有匹配的已启用模型。请到 apiduo 的「MCP 能力注册」里添加并启用模型。"
	}
	return textResult(fmt.Sprintf("共 %d 个模型", len(out))), res, nil
}

// ── 生成 ────────────────────────────────────────────────────────────────

type generateInput struct {
	Prompt          string         `json:"prompt" jsonschema:"提示词，描述想要的内容"`
	Model           string         `json:"model,omitempty" jsonschema:"模型 id。留空用该类型的第一个已启用模型"`
	ReferenceImages any            `json:"reference_images,omitempty" jsonschema:"参考图 URL，单个字符串或字符串数组。传了就由图生图/图生视频"`
	Size            string         `json:"size,omitempty" jsonschema:"尺寸，取值见 models.list 里该模型的 parameters.size.enum"`
	N               *int           `json:"n,omitempty" jsonschema:"生成数量，上限由模型声明约束"`
	Quality         string         `json:"quality,omitempty" jsonschema:"质量档位，取值见该模型的 parameters.quality.enum"`
	Extra           map[string]any `json:"extra,omitempty" jsonschema:"该模型声明的其他参数，键必须在 models.list 里存在"`
}

type outputItem struct {
	URL string `json:"url,omitempty"`
	// B64JSON 是上游直接回图的情况（FLUX 系常见）。必须支持：
	// 有些渠道会忽略 response_format=url 照旧回 base64，
	// 只认 url 的话 MCP 会误报「上游未返回图片地址」。
	B64JSON  string `json:"b64_json,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
}

type generateOutput struct {
	Status   string         `json:"status"` // completed | working
	Model    string         `json:"model"`
	TaskID   string         `json:"task_id,omitempty"`
	Outputs  []outputItem   `json:"outputs,omitempty"`
	Usage    map[string]any `json:"usage,omitempty"`
	PollMS   int            `json:"poll_after_ms,omitempty"`
	Upstream map[string]any `json:"upstream,omitempty"`
}

func (d *toolDeps) generateImage(ctx context.Context, _ *mcpsdk.CallToolRequest, in generateInput) (*mcpsdk.CallToolResult, generateOutput, error) {
	return d.generate(ctx, "image", in)
}

func (d *toolDeps) generateVideo(ctx context.Context, _ *mcpsdk.CallToolRequest, in generateInput) (*mcpsdk.CallToolResult, generateOutput, error) {
	return d.generate(ctx, "video", in)
}

func (d *toolDeps) generate(ctx context.Context, kind string, in generateInput) (*mcpsdk.CallToolResult, generateOutput, error) {
	key := apiKeyFrom(ctx)
	if strings.TrimSpace(in.Prompt) == "" {
		return nil, generateOutput{}, fmt.Errorf("prompt 不能为空")
	}

	views, err := listEnabledViews()
	if err != nil {
		return nil, generateOutput{}, err
	}
	m, err := pickView(views, kind, in.Model)
	if err != nil {
		return nil, generateOutput{}, err
	}

	refs, err := parseRefs(in.ReferenceImages)
	if err != nil {
		return nil, generateOutput{}, err
	}
	payload, err := buildPayload(m, in, refs)
	if err != nil {
		// 本地拒绝必须留样本：它正是「这条声明是不是收得太紧」的唯一信号。
		//
		// 之前这里直接 return，而 observeCall 排在出站之后，于是被本地拒掉的调用
		// 一条都不落库 —— 面板「观察记录」永远 0 次样本，巡检还会把它判成
		// 「参数从未探测」，等于把「声明写错了」显示成「还没人用过」。
		// 这里没有出站，Latency 记 0；只记结构性参数，提示词不进快照。
		observeCall(callObservation{ModelID: m.ID, Params: snapshotInput(in, refs), ErrorMsg: errText(err)})
		return nil, generateOutput{}, err
	}

	t, err := getTransport(m.Transport)
	if err != nil {
		return nil, generateOutput{}, err
	}

	started := time.Now()
	resp, err := d.up.generate(ctx, key, t, payload)

	// 观察埋点：无论成败都记一条，失败的样本比成功的更值钱。
	// 只记结构性参数，提示词内容一个字都不落盘。
	observeCall(callObservation{
		ModelID:  m.ID,
		Params:   snapshotParams(payload),
		OK:       err == nil,
		ErrorMsg: errText(err),
		Latency:  time.Since(started),
	})

	if err != nil {
		return nil, generateOutput{}, err
	}

	if tid, ok := firstString(resp, "task_id", "id"); ok && m.Async {
		signed := signTaskID(key, taskPayload{
			Model:     m.ID,
			Upstream:  tid,
			Kind:      m.Kind,
			Transport: m.Transport, // 查询端点必须与生成端点同源
			IssuedAt:  time.Now().Unix(),
		})
		return textResult("已提交，任务 " + signed + " 进行中"), generateOutput{
			Status: "working", Model: m.ID, TaskID: signed, PollMS: 2000, Upstream: resp,
		}, nil
	}

	out := parseOutputs(resp)
	if len(out) == 0 {
		// 上游没给可用产物：如实说明，不要伪造成功。
		return nil, generateOutput{}, fmt.Errorf("上游未返回图片/视频数据：%s", brief(resp))
	}
	inline := 0
	for _, o := range out {
		if o.URL == "" && o.B64JSON != "" {
			inline++
		}
	}
	summary := fmt.Sprintf("生成完成，共 %d 个产物", len(out))
	if inline > 0 {
		// 明确告诉调用方「图在 b64_json 里」，否则它会去找 url 找不到。
		summary += fmt.Sprintf("（其中 %d 个以 base64 内联返回，字段 b64_json）", inline)
	}
	return textResult(summary), generateOutput{
		Status: "completed", Model: m.ID, Outputs: out, Usage: usageOf(resp), Upstream: resp,
	}, nil
}

// buildPayload 把 MCP 入参翻译成 apiduo /v1 的通用请求体。
//
// 三条规则：
//   - reference_images → image，统一发数组
//   - response_format 固定 url，不暴露给调用方
//   - 只有声明过的参数才接受，其余本地拒绝（越界值打到上游既浪费一次计费，
//     又要在错误里反推是哪个参数不对）
func buildPayload(m modelView, in generateInput, refs []string) (map[string]any, error) {
	// 声明了提示词上限就必须拦：声明只是给 agent 的事前提示，
	// 不拦的话超长提示词会打到上游换一个 400 和一次计费。
	// 按字符数而不是字节数 —— 中文提示词按字节算会短三倍。
	if m.MaxPromptChars != nil && *m.MaxPromptChars > 0 {
		if n := len([]rune(in.Prompt)); n > *m.MaxPromptChars {
			return nil, fmt.Errorf("prompt 长度 %d 超过模型 %s 的上限 %d", n, m.ID, *m.MaxPromptChars)
		}
	}

	p := map[string]any{
		"model":           m.ID,
		"prompt":          in.Prompt,
		"response_format": "url",
	}
	if len(refs) > 0 {
		// 只有「已知上限」才拦截。未知就交给上游判定 ——
		// 按 0 处理会误杀图生图能力。
		if m.MaxRefImages != nil {
			if *m.MaxRefImages <= 0 {
				return nil, fmt.Errorf("模型 %s 不支持参考图（上限 0）", m.ID)
			}
			if len(refs) > *m.MaxRefImages {
				return nil, fmt.Errorf("模型 %s 最多接受 %d 张参考图，收到 %d 张", m.ID, *m.MaxRefImages, len(refs))
			}
		}
		p["image"] = refs
	}
	if in.Size != "" {
		if err := checkEnum(m, "size", in.Size); err != nil {
			return nil, err
		}
		p["size"] = in.Size
	}
	if in.Quality != "" {
		if err := checkEnum(m, "quality", in.Quality); err != nil {
			return nil, err
		}
		p["quality"] = in.Quality
	}
	if in.N != nil {
		if *in.N < 1 {
			return nil, fmt.Errorf("n 必须 ≥ 1，收到 %d", *in.N)
		}
		if err := checkRange(m, "n", float64(*in.N)); err != nil {
			return nil, err
		}
		if _, declared := m.Params["n"]; !declared && *in.N > maxBatchN {
			// 没声明上限时的兜底：上游常常只认 n、不给 max，
			// 一次 n=1000 就能把额度打光。「没配置」不该等价于「没有上限」。
			return nil, fmt.Errorf("模型 %s 未声明 n 的上限，单次最多 %d；要更多请分多次调用", m.ID, maxBatchN)
		}
		p["n"] = *in.N
	}
	for k, v := range in.Extra {
		if _, ok := m.Params[k]; !ok && !standardParams[k] {
			return nil, fmt.Errorf("模型 %s 未声明参数 %q，可用：%s", m.ID, k, strings.Join(paramNames(m), ", "))
		}
		if err := checkExtra(m, k, v); err != nil {
			return nil, err
		}
		p[k] = v
	}
	return p, nil
}

// maxBatchN 是「模型没声明 n 上限」时的兜底数量。
const maxBatchN = 8

// standardParams 是跨厂商通用的参数名。模型声明缺失时这几个仍允许透传，
// 否则 size/n 这类标准参数会被白名单误杀，模型直接不可用。
var standardParams = map[string]bool{
	"size": true, "quality": true, "n": true, "format": true,
	"duration": true, "resolution": true, "aspect_ratio": true,
}

func paramNames(m modelView) []string {
	out := make([]string, 0, len(m.Params))
	for k := range m.Params {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func checkEnum(m modelView, name, val string) error {
	spec, ok := m.Params[name]
	if !ok || len(spec.Enum) == 0 {
		// 没声明就无从校验，交给上游判定
		return nil
	}
	opts := make([]string, 0, len(spec.Enum))
	for _, v := range spec.Enum {
		if fmt.Sprint(v) == val {
			return nil
		}
		opts = append(opts, fmt.Sprint(v))
	}
	return fmt.Errorf("%s 取值 %q 不被模型 %s 接受，可选：%s", name, val, m.ID, strings.Join(opts, ", "))
}

func checkRange(m modelView, name string, val float64) error {
	spec, ok := m.Params[name]
	if !ok {
		return nil
	}
	if spec.Min != nil && val < *spec.Min {
		return fmt.Errorf("%s 不能小于 %g", name, *spec.Min)
	}
	if spec.Max != nil && val > *spec.Max {
		return fmt.Errorf("%s 不能大于 %g", name, *spec.Max)
	}
	return nil
}

// checkExtra 校验模型特有参数。enum 优先，没有 enum 才看 min/max；
// 两样都没声明就放行 —— 声明了参数名但没给约束是允许的，等于「任意值」。
func checkExtra(m modelView, name string, val any) error {
	spec, ok := m.Params[name]
	if !ok {
		return nil
	}
	if len(spec.Enum) > 0 {
		s := fmt.Sprint(val)
		opts := make([]string, 0, len(spec.Enum))
		for _, v := range spec.Enum {
			if fmt.Sprint(v) == s {
				return nil
			}
			opts = append(opts, fmt.Sprint(v))
		}
		return fmt.Errorf("%s 取值 %q 不被模型 %s 接受，可选：%s", name, s, m.ID, strings.Join(opts, ", "))
	}
	f, numeric := toFloat(val)
	if !numeric {
		return nil
	}
	if spec.Min != nil && f < *spec.Min {
		return fmt.Errorf("%s 不能小于 %g", name, *spec.Min)
	}
	if spec.Max != nil && f > *spec.Max {
		return fmt.Errorf("%s 不能大于 %g", name, *spec.Max)
	}
	return nil
}

// ── task.get ────────────────────────────────────────────────────────────

type taskInput struct {
	TaskID string `json:"task_id" jsonschema:"video.generate 返回的 task_id"`
}

type taskOutput struct {
	Status   string         `json:"status"` // working | completed | failed
	TaskID   string         `json:"task_id"`
	Model    string         `json:"model"`
	Outputs  []outputItem   `json:"outputs,omitempty"`
	Error    string         `json:"error,omitempty"`
	PollMS   int            `json:"poll_after_ms,omitempty"`
	Upstream map[string]any `json:"upstream,omitempty"`
}

func (d *toolDeps) getTask(ctx context.Context, _ *mcpsdk.CallToolRequest, in taskInput) (*mcpsdk.CallToolResult, taskOutput, error) {
	key := apiKeyFrom(ctx)
	p, err := parseTaskID(key, in.TaskID)
	if err != nil {
		return nil, taskOutput{}, fmt.Errorf("task_id 无效或已过期（换过 api_key？）")
	}
	t, err := getTransport(p.Transport)
	if err != nil {
		return nil, taskOutput{}, err
	}
	raw, err := d.up.getTask(ctx, key, t, p.Upstream)
	if err != nil {
		return nil, taskOutput{}, err
	}
	// 生成端点回扁平结构，查询端点把记录包在 {code,message,data} 里。
	// 只认扁平形状的后果很具体：任务状态永远读成 working，
	// 完成时也取不到 result_url —— 而单测里 mock 的一直是扁平形状。
	resp := unwrapTaskBody(raw)

	status := normalizeStatus(str(resp, "status"))
	out := taskOutput{Status: status, TaskID: in.TaskID, Model: p.Model, Upstream: resp}
	switch status {
	case "completed":
		out.Outputs = parseOutputs(resp)
	case "working":
		out.PollMS = 2000
	case "failed":
		out.Error = firstNonEmpty(str(resp, "fail_reason"), str(resp, "error"), str(resp, "message"), "上游任务失败")
	}
	return textResult("任务状态：" + status), out, nil
}

// ── 小工具 ──────────────────────────────────────────────────────────────

func textResult(s string) *mcpsdk.CallToolResult {
	return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: s}}}
}

// parseRefs 兼容 string 与 string[]：入口宽松，内部统一成数组。
//
// 入参声明为 any 而不是 json.RawMessage：后者是 []byte，SDK 推断 schema 时
// 会当成字节数组，客户端传字符串会被拦在协议层。
func parseRefs(v any) ([]string, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case string:
		if strings.TrimSpace(t) == "" {
			return nil, nil
		}
		return []string{t}, nil
	case []any:
		out := make([]string, 0, len(t))
		for _, it := range t {
			sv, ok := it.(string)
			if !ok {
				return nil, fmt.Errorf("reference_images 数组元素必须是字符串")
			}
			if sv = strings.TrimSpace(sv); sv != "" {
				out = append(out, sv)
			}
		}
		return out, nil
	case []string:
		out := make([]string, 0, len(t))
		for _, sv := range t {
			if sv = strings.TrimSpace(sv); sv != "" {
				out = append(out, sv)
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("reference_images 需为字符串或字符串数组")
}

// parseOutputs 从上游响应里提取产物地址，兼容几种常见形状。
func parseOutputs(resp map[string]any) []outputItem {
	var out []outputItem
	for _, item := range toSlice(resp["data"]) {
		u := str(item, "url")
		b := str(item, "b64_json")
		switch {
		case u != "":
			out = append(out, outputItem{URL: u, MimeType: str(item, "mime_type")})
		case b != "":
			out = append(out, outputItem{B64JSON: b, MimeType: str(item, "mime_type")})
		}
	}
	for _, item := range toSlice(resp["outputs"]) {
		if u := str(item, "url"); u != "" {
			out = append(out, outputItem{URL: u, MimeType: str(item, "mime_type")})
		}
	}
	if u := str(resp, "url"); u != "" {
		out = append(out, outputItem{URL: u})
	}
	// apiduo 的视频任务：{url, format}
	if u := str(resp, "video_url"); u != "" {
		out = append(out, outputItem{URL: u})
	}
	// apiduo 的任务 DTO：result_url 才是最终产物地址
	if u := str(resp, "result_url"); u != "" {
		out = append(out, outputItem{URL: u})
	}
	return out
}

// unwrapTaskBody 解掉 apiduo 任务查询响应的一层壳。
//
// 只解一层是刻意的：data.data 是上游原始响应，再往下解会让上游的
// "queued" 覆盖网关已经落定的 "SUCCESS"。
func unwrapTaskBody(resp map[string]any) map[string]any {
	if str(resp, "code") != "success" {
		return resp
	}
	inner, ok := resp["data"].(map[string]any)
	if !ok {
		return resp
	}
	return inner
}

func usageOf(resp map[string]any) map[string]any {
	if u, ok := resp["usage"].(map[string]any); ok {
		return u
	}
	return nil
}

func normalizeStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "completed", "succeeded", "success", "done", "finished":
		return "completed"
	case "failed", "error", "cancelled", "canceled", "failure":
		return "failed"
	default:
		return "working"
	}
}

func str(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	s, _ := m[k].(string)
	return s
}

func firstString(m map[string]any, keys ...string) (string, bool) {
	for _, k := range keys {
		if s := str(m, k); s != "" {
			return s, true
		}
	}
	return "", false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func toSlice(v any) []map[string]any {
	switch t := v.(type) {
	case []any:
		out := make([]map[string]any, 0, len(t))
		for _, it := range t {
			if m, ok := it.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	case []map[string]any:
		return t
	}
	return nil
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

func brief(resp map[string]any) string {
	keys := make([]string, 0, len(resp))
	for k := range resp {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b, _ := json.Marshal(resp)
	s := string(b)
	if len(s) > 400 {
		s = s[:400] + "…"
	}
	return fmt.Sprintf("字段 [%s] %s", strings.Join(keys, ","), s)
}
