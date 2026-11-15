package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/google/jsonschema-go/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolDeps 是工具的依赖。只有上游客户端 —— 能力来自数据库，不需要注入。
type toolDeps struct {
	up *upClient
}

func registerTools(s *mcpsdk.Server, d *toolDeps) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:  "models.list",
		Title: "列出可用模型",
		Description: "列出已注册且启用的模型及其能力（kind / execution / input / parameters）。" +
			"调用生成类工具前若不确定有哪些模型、参数取什么值，先查这里。" +
			"未出现在列表里的模型都不可调用。",
	}, d.listModels)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "image.generate",
		Title:       "生成图片",
		InputSchema: generateInputSchema(),
		Description: "生成图片。不传 reference_images 即文生图，传了即图生图——模式由垫图推导，无需单独声明。" +
			"参考图用 http(s) URL；本地文件可读成 data:image/<mime>;base64,… 内联，但只有部分上游接受（gpt-image 支持，qwen/flux 会 502）。" +
			"model 可以不传，会用默认模型并在返回里说明是哪个；要指定就先调 models.list 看可选值，" +
			"按 summary 里的场景/成本挑，不要凭模型名猜。" +
			"models.list 里该模型声明的 parameters（aspect_ratio / resolution 等）直接平铺到顶层传，不要包进 extra。" +
			"prompt 也可省略，改传方舟形状的 content 数组（type=text / image_url），会被拆成提示词与参考图。" +
			"size/n/quality 可以不传。传的话：声明里有 enum 就取 enum 内的值；没有 enum 就看 note，" +
			"或按常见写法给（size 支持 WxH，如 1024x1024）。不要凭模型名臆造档位。",
	}, d.generateImage)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "video.generate",
		Title:       "生成视频",
		InputSchema: generateInputSchema(),
		Description: "生成视频。语义与 image.generate 相同，产物是视频；传 reference_images 可做图生视频。" +
			"参考图用 http(s) URL；data:image 内联只在部分上游可用。" +
			"model 可以不传，会用默认模型并在返回里说明；要指定就先调 models.list 挑，不要凭模型名猜。" +
			"也可以直接把方舟形状的 content 数组传进来（type=text / image_url），会被拆成提示词与参考图。" +
			"models.list 里该模型声明的 parameters（duration / resolution 等）直接平铺到顶层传，不要包进 extra。" +
			"多数视频模型是异步的，会立即返回 task_id，用 task.get 查询结果。" +
			"duration/resolution 可以不传。传的话取 models.list 里声明的 enum 值；" +
			"报错会给出最接近的合法值，照着改即可。",
	}, d.generateVideo)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:  "task.get",
		Title: "查询任务状态",
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
	// CatalogVersion 是这份清单的目录版本：声明一改就变。
	// agent 可以把上一次拿到的值记下来，下次调用对不上时主动重拉。
	CatalogVersion string     `json:"catalog_version,omitempty"`
	Note           string     `json:"note,omitempty"`
	Error          *toolError `json:"error,omitempty"`
}

func (d *toolDeps) listModels(ctx context.Context, _ *mcpsdk.CallToolRequest, in listInput) (*mcpsdk.CallToolResult, listOutput, error) {
	caller := callerFrom(ctx)
	// models.list 不走 relay，既没有额度也没有 relay 的限流，是唯一能被脚本打满的入口。
	if wait, ok := allowCatalogList(caller.TokenId); !ok {
		ms := int(wait.Milliseconds())
		if ms < 1000 {
			ms = 1000
		}
		return failList(&toolError{
			Code:         CodeRateLimited,
			Message:      fmt.Sprintf("models.list 调用过于频繁，%d 秒后再试", (ms+999)/1000),
			Retryable:    true,
			RetryAction:  "wait",
			RetryAfterMS: ms,
			Hint:         "能力清单变化不频繁，不需要高频轮询",
		})
	}

	snap, err := loadCatalog()
	if err != nil {
		return nil, listOutput{}, err
	}
	kind := strings.ToLower(strings.TrimSpace(in.Kind))
	out := make([]map[string]any, 0, len(snap.views))
	for _, v := range snap.views {
		if kind != "" && v.Kind != kind {
			continue
		}
		out = append(out, v.card())
	}
	res := listOutput{Models: out, Count: len(out), CatalogVersion: snap.version}
	if len(out) == 0 {
		// 空列表最常见的两种原因是「没注册」和「注册了但停用」，直接说清楚，
		// 免得 agent 反复调用同一个工具试探。
		res.Note = "没有匹配的已启用模型。请到 apiduo 的「MCP 能力注册」里添加并启用模型。"
		return textResult(res.Note), res, nil
	}
	// 版本进 text 而不是只放 structuredContent：模型下一轮看的是文本，
	// 它得能发现「这次的清单和上次不一样」。
	return textResult(modelsSummary(out) + "\n清单版本 " + snap.version), res, nil
}

// failList 把 listModels 的失败包成结构化错误。
func failList(te *toolError) (*mcpsdk.CallToolResult, listOutput, error) {
	out := listOutput{Error: te}
	res := &mcpsdk.CallToolResult{IsError: true}
	if te != nil {
		res.Content = []mcpsdk.Content{&mcpsdk.TextContent{Text: te.Message}}
	}
	return res, out, nil
}

// defaultedModelNote 说明本次用了哪个默认模型、还有哪些可选。
//
// 不做硬性必填是产品决定：MCP 的使用者是让 agent 帮忙做事的人，
// 多一轮「先查清单再画」的往返会把「画张图」变成一件麻烦事。
// 引导放在结果里，不放在门槛上。
func defaultedModelNote(views []modelView, kind, used string) string {
	ids := make([]string, 0, len(views))
	for _, v := range views {
		if v.Kind == kind {
			ids = append(ids, v.ID)
		}
	}
	note := "未指定 model，本次用了 " + used + "。"
	for _, v := range views {
		if v.ID == used && v.Cost != "" {
			note = "未指定 model，本次用了 " + used + "（" + v.Cost + "）。"
			break
		}
	}
	if len(ids) > 1 {
		note += "可选：" + strings.Join(ids, ", ") + "；下次带上 model 就能指定。"
	}
	return note
}

// modelsSummary 是给「只渲染 text 的客户端」看的清单。
//
// structuredContent 字段更全，但多数客户端只显示 text。只回一句「共 N 个模型」
// 等于让 agent 再猜一次；这里把 id、类型和一句简介直接写出来，它才选得动。
func modelsSummary(models []map[string]any) string {
	var b strings.Builder
	fmt.Fprintf(&b, "可用模型 %d 个（不指定 model 时用标了默认的那个）：", len(models))
	// models 已按 kind、priority 排好，所以每类第一条就是默认模型。
	// 直接标出来，比让 agent 自己推断「第一个是谁」可靠。
	defaulted := map[string]bool{}
	for _, m := range models {
		id, _ := m["id"].(string)
		kind, _ := m["kind"].(string)
		summary, _ := m["summary"].(string)
		if strings.TrimSpace(summary) == "" {
			summary = "无简介"
		}
		tag := kind
		if !defaulted[kind] {
			tag += "，默认"
			defaulted[kind] = true
		}
		line := fmt.Sprintf("\n- %s（%s）：%s", id, tag, clipRunes(summary, 80))
		if cost, _ := m["cost"].(string); cost != "" {
			line += "｜" + cost
		}
		b.WriteString(line)
		// 摘要按 80 字截断，枚举值经常正好被切掉。参数行单独给一份紧凑版，
		// 让只读 text 的客户端也能拿到「能传什么」，不用再去解 structuredContent。
		if params := paramSummary(m, 6); params != "" {
			b.WriteString("\n  参数：" + params)
		}
	}
	return b.String()
}

// paramSummary 把声明的参数压成一行：duration=5/10/15；resolution=720P。
//
// 只列名字不列值等于没说；列全量又会让清单失控。每个参数最多六个值，
// 超出用省略号 —— 需要完整列表时读 structuredContent。
func paramSummary(m map[string]any, limit int) string {
	params, ok := m["parameters"].(map[string]ParamSpec)
	if !ok || len(params) == 0 {
		return ""
	}
	names := make([]string, 0, len(params))
	for name := range params {
		names = append(names, name)
	}
	sort.Strings(names)

	parts := make([]string, 0, len(names))
	for _, name := range names {
		spec := params[name]
		switch {
		case len(spec.Enum) > 0:
			vals := make([]string, 0, limit+1)
			for i, v := range spec.Enum {
				if i >= limit {
					vals = append(vals, "…")
					break
				}
				vals = append(vals, fmt.Sprint(v))
			}
			parts = append(parts, name+"="+strings.Join(vals, "/"))
		case spec.Min != nil && spec.Max != nil && *spec.Min == *spec.Max:
			parts = append(parts, fmt.Sprintf("%s=%g", name, *spec.Min))
		case spec.Min != nil || spec.Max != nil:
			lo, hi := "-", "-"
			if spec.Min != nil {
				lo = fmt.Sprintf("%g", *spec.Min)
			}
			if spec.Max != nil {
				hi = fmt.Sprintf("%g", *spec.Max)
			}
			parts = append(parts, fmt.Sprintf("%s=%s..%s", name, lo, hi))
		default:
			parts = append(parts, name)
		}
	}
	return strings.Join(parts, "；")
}

// clipRunes 按字符截断，避免把中文切成半个字。
func clipRunes(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}

// generateInputSchema 放宽「额外属性」。
//
// 能力注册里声明的 parameters 是平铺的（aspect_ratio / resolution / duration…），
// 而工具入参结构体只有固定几个字段。SDK 默认给 struct 生成
// additionalProperties: false，于是 agent 照着 models.list 平铺传参时，会被
// schema 直接打回 —— 它通常换个措辞重试，用户看到的是「调用了却没有日志」。
// 放开之后由 absorbUndeclaredParams 收进 Extra，buildPayload 再决定哪些真正生效。
func generateInputSchema() *jsonschema.Schema {
	schema, err := jsonschema.ForType(reflect.TypeFor[generateInput](), &jsonschema.ForOptions{})
	if err != nil || schema == nil {
		return nil // nil 让 SDK 回落到反射生成，宁可严格也不让工具注册失败
	}
	schema.AdditionalProperties = &jsonschema.Schema{}
	return schema
}

// absorbUndeclaredParams 把 schema 放行、结构体却没有对应字段的顶层参数收进 Extra。
//
// 丢掉它们的后果不是报错，而是模型按默认值跑：用户传了 aspect_ratio，日志里
// 记的却是默认尺寸，看起来「参数没生效」——这比直接失败更难查。
func absorbUndeclaredParams(req *mcpsdk.CallToolRequest, in *generateInput) {
	if req == nil || len(req.Params.Arguments) == 0 {
		return
	}
	var raw map[string]any
	if err := json.Unmarshal(req.Params.Arguments, &raw); err != nil {
		return
	}
	// 结构体已声明的键由 SDK 解好，这里不能重复写入，否则 Extra 会覆盖显式字段。
	declared := map[string]bool{
		"prompt": true, "model": true, "reference_images": true,
		"size": true, "n": true, "quality": true, "extra": true,
		"client_request_id": true,
	}
	for key, value := range raw {
		if declared[key] {
			continue
		}
		// 方舟形状的 content 不是「多余参数」，是同一件事的另一种说法。
		// Agent 读方舟文档后照抄 content 数组很常见（线上最高频的一类报错），
		// 报错等于让它把已经说清楚的意思重说一遍。这里拆成 prompt +
		// reference_images，与直接传这两个字段走同一条路径。
		if key == "content" {
			if absorbArkContent(value, in) {
				continue
			}
		}
		if in.Extra == nil {
			in.Extra = make(map[string]any, len(raw))
		}
		if _, exists := in.Extra[key]; !exists {
			in.Extra[key] = value
		}
	}
	// agent 很爱把参数包进 extra（实测视频那组就写成 extra:{duration,ratio}）。
	// content 被包进去时同样要吸收，否则又退回到「未声明参数 content」那条老路。
	if v, ok := in.Extra["content"]; ok {
		if absorbArkContent(v, in) {
			delete(in.Extra, "content")
		}
	}
}

// ── 生成 ────────────────────────────────────────────────────────────────

type generateInput struct {
	// 不标 required：方舟形状的 content 数组是 prompt 的另一种说法，
	// SDK 在 schema 阶段拦住缺 prompt 的请求，absorbArkContent 就没机会跑了。
	// 空提示词改在 buildPayload 之后统一校验，那时 content 已经拆完。
	Prompt          string `json:"prompt,omitempty" jsonschema:"提示词，描述想要的内容。与 content 二选一"`
	Model           string `json:"model,omitempty" jsonschema:"模型 id。不传用默认模型（返回里会说明）；取值来自 models.list"`
	ReferenceImages any    `json:"reference_images,omitempty" jsonschema:"参考图 URL，单个字符串或字符串数组。传了就由图生图/图生视频"`
	Size            string `json:"size,omitempty" jsonschema:"尺寸。取值见该模型 parameters.size 的 enum 或 note；都没写时传 WxH，如 1024x1024"`
	N               *int   `json:"n,omitempty" jsonschema:"生成数量，上限由模型声明约束"`
	Quality         string `json:"quality,omitempty" jsonschema:"质量档位。取值见该模型 parameters.quality 的 enum 或 note；没写就不传"`
	// ClientRequestID 是调用方的幂等键：同一个值重发不会重复生成，
	// 直接返回首次结果（1–64 字符）。
	ClientRequestID string         `json:"client_request_id,omitempty" jsonschema:"幂等键，1–64 字符。同一键重发直接返回首次结果，不会重复生成"`
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
	Status  string         `json:"status"` // completed | working | failed
	Model   string         `json:"model"`
	TaskID  string         `json:"task_id,omitempty"`
	Outputs []outputItem   `json:"outputs,omitempty"`
	Usage   map[string]any `json:"usage,omitempty"`
	PollMS  int            `json:"poll_after_ms,omitempty"`
	Error   *toolError     `json:"error,omitempty"`
	// ModelDefaulted 表示调用方没点名模型，本次用了默认那个。
	// 客户端可以据此在界面上提示「换模型就带上 model」，而不是让它成为隐形行为。
	ModelDefaulted bool `json:"model_defaulted,omitempty"`
	// Replayed 表示这次没有真的生成，而是按 client_request_id 复用了首次结果。
	// 只写进 text 不够：只看 structuredContent 的客户端会把重放当成一次新生成，
	// 于是「同一次调用」在它的账上变成两次。
	Replayed bool `json:"replayed,omitempty"`
}

func (d *toolDeps) generateImage(ctx context.Context, req *mcpsdk.CallToolRequest, in generateInput) (*mcpsdk.CallToolResult, generateOutput, error) {
	return d.generate(ctx, "image", in, req)
}

func (d *toolDeps) generateVideo(ctx context.Context, req *mcpsdk.CallToolRequest, in generateInput) (*mcpsdk.CallToolResult, generateOutput, error) {
	return d.generate(ctx, "video", in, req)
}

func (d *toolDeps) generate(ctx context.Context, kind string, in generateInput, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, generateOutput, error) {
	key := apiKeyFrom(ctx)
	absorbUndeclaredParams(req, &in)
	caller := callerFrom(ctx)
	// 一行入口日志。用户的「我明明调了，日志里没有」有一半是客户端根本没发出
	// tools/call（握手 5ms，生图要几秒，两者在访问日志里长得完全不同）；有了这行，
	// 「没到服务端」和「到了但记账失败」当场分得开。
	common.SysLog(common.LogText("[mcp] %s.generate start: user=%d token=%d model=%q size=%q",
		kind, caller.UserId, caller.TokenId, in.Model, in.Size))
	if strings.TrimSpace(in.Prompt) == "" {
		return d.fail(in.Model, userParamErrorf(CodePromptRequired, "prompt 不能为空"))
	}

	snap, err := loadCatalog()
	if err != nil {
		return nil, generateOutput{}, err
	}
	views := snap.views
	m, err := pickView(views, kind, in.Model)
	if err != nil {
		return d.fail(in.Model, err)
	}

	refs, err := parseRefs(in.ReferenceImages)
	if err != nil {
		return d.fail(m.ID, err)
	}
	payload, err := buildPayload(m, in, refs)
	if err != nil {
		// 本地拒绝必须留样本：它正是「这条声明是不是收得太紧」的唯一信号。
		//
		// 之前这里直接 return，而 observeCall 排在出站之后，于是被本地拒掉的调用
		// 一条都不落库 —— 面板「观察记录」永远 0 次样本，巡检还会把它判成
		// 「参数从未探测」，等于把「声明写错了」显示成「还没人用过」。
		// 这里没有出站，Latency 记 0；只记结构性参数，提示词不进快照。
		observeCall(ctx, callObservation{Tool: kind + ".generate", ModelID: m.ID, Params: snapshotInput(in, refs), ErrorMsg: errText(err)})
		return d.fail(m.ID, err)
	}

	// 幂等占位放在本地校验之后、出站之前：参数错的请求不该占用调用方的键，
	// 而占位之后就不能再让第二个同键请求打到上游。
	if in.ClientRequestID != "" {
		if err := validateClientRequestID(in.ClientRequestID); err != nil {
			return d.fail(m.ID, err)
		}
		replay, reserveErr := reserveIdempotency(caller.TokenId, kind, in.ClientRequestID, requestFingerprint(kind, payload))
		switch {
		case reserveErr == nil && replay != nil:
			out := replay.Output
			out.Replayed = true
			// 异步任务的重放：表里存的是「提交时」的快照，任务可能早就完成了。
			// 查一次当前状态（本地读，30 秒上限）再返回，省掉 agent 拿着
			// 「进行中」再轮询一轮 —— 这一步不产生任何上游调用。
			if out.Status == "failed" {
				// 首次是失败（超时 / 无产物 / 句柄签不出来），重放必须维持 isError=true，
				// 否则同一件事第一次是错误、第二次是成功，agent 无从判断。
				return &mcpsdk.CallToolResult{
					IsError: true,
					Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: replayText(replay)}},
				}, out, nil
			}
			if out.Status == "working" && out.TaskID != "" {
				if res, fresh, ferr := d.getTask(ctx, nil, taskInput{TaskID: out.TaskID}); ferr == nil && res != nil && !res.IsError {
					freshGen := taskToGenerateOutput(fresh)
					freshGen.Replayed = true
					summary := taskSummary(fresh.Model, fresh.Status, fresh)
					if fresh.Status == "completed" {
						// 只有成功才写回：失败的任务每次重放都重新查一次（本地读，很便宜），
						// 这样它的 isError 一直是 false（查询成功、任务失败），不会因为
						// 第一次刷新把 failed 写回表、第二次就走成 isError=true。
						completeIdempotency(caller.TokenId, kind, in.ClientRequestID, summary, freshGen)
					}
					return textResult(replayRefreshedText(summary)), freshGen, nil
				}
			}
			return textResult(replayText(replay)), out, nil
		case reserveErr == nil:
			// 首次调用，继续往下走。
		case errors.Is(reserveErr, errIdempotencyInFlight):
			return d.failWith(m.ID, &toolError{
				Code:         CodeRequestInFlight,
				Message:      "同一 client_request_id 的请求还在处理中",
				Retryable:    true,
				RetryAction:  "wait",
				RetryAfterMS: 2000,
				Hint:         "不要换一个新的 client_request_id 重发，否则会真的多生成一次",
			})
		case errors.Is(reserveErr, errIdempotencyKeyReused):
			return d.failWith(m.ID, &toolError{
				Code:        CodeIdempotencyKeyReused,
				Message:     "client_request_id 已用于另一组参数，不能复用",
				Retryable:   false,
				RetryAction: "none",
				Hint:        "换一个新的 client_request_id 再发；用同一个键不会产生新的生成，只会拿回无关的旧结果",
			})
		case errors.Is(reserveErr, errReplayUnavailable):
			return d.failWith(m.ID, &toolError{
				Code:        CodeReplayUnavailable,
				Message:     "首次调用已成功，但结果太大没有缓存，无法重放",
				Retryable:   false,
				RetryAction: "none",
				Hint:        "到控制台任务页取产物；不要重发，会再计费一次",
			})
		default:
			return d.fail(m.ID, reserveErr)
		}
	}

	t, err := getTransport(m.Kind)
	if err != nil {
		return d.fail(m.ID, err)
	}

	// 生成本身的硬超时也来自设置：上游变慢时先放宽，别等发版。
	ctx, cancel := context.WithTimeout(ctx, secondsToDuration(LoadSettings().UpstreamTimeoutS))
	defer cancel()

	started := time.Now()
	var (
		resp           map[string]any
		relayRequestId string
		out            []outputItem
		partialFailed  int
		partialErr     string
	)
	if parts := fanoutCount(m, kind, payload); parts > 1 {
		out, partialFailed, partialErr, err = d.generateParts(ctx, key, t, payload, parts)
	} else {
		resp, relayRequestId, err = d.up.generate(ctx, key, t, payload)
		if err == nil {
			out = parseOutputs(resp)
		}
	}

	// 观察埋点：无论成败都记一条，失败的样本比成功的更值钱。
	// 只记结构性参数，提示词内容一个字都不落盘。
	// 拆单时没有单一 relay 请求号，RelayRequestId 留空；部分失败记在 ErrorMsg 里。
	obsErr := errText(err)
	if obsErr == "" {
		obsErr = partialErr
	}
	observeCall(ctx, callObservation{
		Tool:           kind + ".generate",
		ModelID:        m.ID,
		Params:         snapshotParams(payload),
		OK:             err == nil,
		ErrorMsg:       obsErr,
		RelayRequestId: relayRequestId,
		Latency:        time.Since(started),
	})

	if err != nil {
		if in.ClientRequestID != "" {
			if shouldReleaseIdempotency(err) {
				releaseIdempotency(caller.TokenId, kind, in.ClientRequestID)
			} else {
				// 请求已经发出、没等到响应：上游很可能已经受理并计费。
				// 释放键 = 允许下一次重试再扣一次，所以这里把结论写死，
				// 重放时明确告诉调用方去后台确认，而不是悄悄重生成。
				completeIdempotency(caller.TokenId, kind, in.ClientRequestID,
					"首次调用超时，结果未能取回；产物可能已在控制台任务页可见。为避免重复扣费，本次不再重新生成",
					generateOutput{Status: "failed", Model: m.ID, Error: classifyToolError(m.ID, err)})
			}
		}
		return d.fail(m.ID, err)
	}

	if tid, ok := firstString(resp, "task_id", "id"); ok && m.Async {
		signed, signErr := signTaskID(taskPayload{
			Model:    m.ID,
			Upstream: tid,
			Kind:     m.Kind, // 查询端点必须与生成端点同源
			UserId:   caller.UserId,
		})
		if signErr != nil {
			// 上游已经受理并计费，但句柄签不出来：如实说，别假装成功。
			text := "任务已提交但签发 task_id 失败: " + signErr.Error()
			if in.ClientRequestID != "" {
				// 记为 done 而不是释放：重试会再计费一次，宁可在重放时说明签不出句柄。
				completeIdempotency(caller.TokenId, kind, in.ClientRequestID, text,
					generateOutput{Status: "failed", Model: m.ID, Error: classifyToolError(m.ID, signErr)})
			}
			return d.fail(m.ID, fmt.Errorf("任务已提交但签发 task_id 失败: %w", signErr))
		}
		text := "已提交（模型 " + m.ID + "），任务 " + signed + " 进行中"
		out := generateOutput{
			Status: "working", Model: m.ID, TaskID: signed, PollMS: pollAfter(),
			ModelDefaulted: strings.TrimSpace(in.Model) == "",
		}
		if in.ClientRequestID != "" {
			completeIdempotency(caller.TokenId, kind, in.ClientRequestID, text, out)
		}
		return textResult(text), out, nil
	}

	if len(out) == 0 {
		// 上游没给可用产物：如实说明，不要伪造成功。
		// 不带键时可以退避重试一次（同参数、不同上游实例，往往就过了）。
		msg := "上游未返回图片/视频数据"
		if resp != nil {
			msg += "：" + brief(resp)
		} else if partialErr != "" {
			msg += "：" + partialErr
		}
		te := &toolError{
			Code:         CodeUpstreamError,
			Message:      msg,
			Retryable:    true,
			RetryAction:  "wait",
			RetryAfterMS: 5000,
			Hint:         "同参数退避后重试一次；仍失败就换模型",
		}
		if in.ClientRequestID != "" {
			// 200 说明这次调用已经走完，计费很可能已经发生。释放键 = 放行第二次扣费，
			// 所以标记 done：重放拿到的是这句话，而不是重新生成。
			te.Retryable = false
			te.RetryAction = "none"
			te.Hint = "计费可能已发生；同一 client_request_id 不会重新生成，确认后台没有产物后换一个新的键再试"
			completeIdempotency(caller.TokenId, kind, in.ClientRequestID,
				"生成已走完但上游未返回可用产物；为避免重复扣费，本次不再重新生成，请到控制台任务页确认",
				generateOutput{Status: "failed", Model: m.ID, Error: te})
		}
		return d.failWith(m.ID, te)
	}
	inline := 0
	for _, o := range out {
		if o.URL == "" && o.B64JSON != "" {
			inline++
		}
	}
	summary := appendOutputLinks(fmt.Sprintf("生成完成（模型 %s），共 %d 个产物", m.ID, len(out)), out)
	if partialFailed > 0 {
		summary += fmt.Sprintf("（另有 %d 张失败：%s）", partialFailed, clipRunes(partialErr, 120))
	}
	if strings.TrimSpace(in.Model) == "" {
		// The panel and the log page are searched by model name. Silently
		// defaulting to the first enabled model produced a real support case:
		// the caller asked for "flux", the log recorded flux-3, and the query
		// for flux-3-image came back empty. Say which model ran — and which
		// others were available, so the next call can name one.
		summary = defaultedModelNote(views, kind, m.ID) + summary
	}
	if inline > 0 {
		// 明确告诉调用方「图在 b64_json 里」，否则它会去找 url 找不到。
		summary += fmt.Sprintf("（其中 %d 个以 base64 内联返回，字段 b64_json）", inline)
	}
	final := generateOutput{
		Status: "completed", Model: m.ID, Outputs: out, Usage: usageOf(resp),
		ModelDefaulted: strings.TrimSpace(in.Model) == "",
	}
	if in.ClientRequestID != "" {
		completeIdempotency(caller.TokenId, kind, in.ClientRequestID, summary, final)
	}
	return textResult(summary), final, nil
}

// fanoutCount 决定这次要并发出几个单张请求。
//
// 上游的 n 是串行生成的：qwen n=1 约 85 秒、n=2 约 154 秒、n=4 约 349 秒，
// 近似线性 —— 一次调用必然先撞客户端超时。所以 MCP 不把 n 交给上游，而是自己
// 拆成 n 个单张并发：实测两个单张并发 85.7 秒完成（不是 170 秒）。
//
// 只对图片生效：视频一次一任务，n 没有意义。
func fanoutCount(m modelView, kind string, payload map[string]any) int {
	if kind != "image" {
		return 1
	}
	n, ok := toFloat(payload["n"])
	if !ok || n < 2 {
		return 1
	}
	// 声明的最小值大于 1 时不能拆：单张请求会违反模型自己的约束。
	if spec, ok := m.Params["n"]; ok && spec.Min != nil && *spec.Min > 1 {
		return 1
	}
	return min(int(n), LoadSettings().MaxBatchN)
}

// generateParts 并发发起 n 个单张请求，按顺序聚合产物。
//
// 部分失败时把已经生成的图交出去（钱已经花了，丢掉更亏），失败数量与原因
// 由调用方写进 text。全部失败才返回错误，交给 classifyToolError 归类。
func (d *toolDeps) generateParts(ctx context.Context, key string, t transport, payload map[string]any, n int) (out []outputItem, failed int, firstErr string, err error) {
	// 每个分片显式传 n=1。不省略这个键：多数上游的 n 默认就是 1，但显式写出来
	// 才不会依赖各家默认值；省略只是少一个字段，不解决「上游可能自己再串行」的问题。
	base := make(map[string]any, len(payload))
	maps.Copy(base, payload)
	base["n"] = 1

	type part struct {
		items []outputItem
		err   error
	}
	parts := make([]part, n)
	// 并发上限可配：上游对同一个 key 的并发容忍度不一样，打太多会互相排队。
	sem := make(chan struct{}, max(1, LoadSettings().FanoutParallel))
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			resp, _, e := d.up.generate(ctx, key, t, base)
			if e != nil {
				parts[i].err = e
				return
			}
			parts[i].items = parseOutputs(resp)
		}(i)
	}
	wg.Wait()

	hardErr := 0
	var firstHardErr error
	for i := range parts {
		p := &parts[i]
		switch {
		case p.err != nil:
			failed++
			hardErr++
			if firstHardErr == nil {
				firstHardErr = p.err
			}
			if firstErr == "" {
				firstErr = errText(p.err)
			}
		case len(p.items) == 0:
			failed++
			if firstErr == "" {
				firstErr = "上游返回成功但没有产物"
			}
		default:
			out = append(out, p.items...)
		}
	}
	if len(out) == 0 && hardErr == n {
		return nil, failed, firstErr, firstHardErr
	}
	return out, failed, firstErr, nil
}

// buildPayload 把 MCP 入参翻译成 apiduo /v1 的通用请求体。
//
// 两种产物各有一套形状，不能混：
//   - 图片 /images/generations：OpenAI 的 prompt + image[]，方舟图片接口兼容它
//   - 视频 /contents/generations/tasks：方舟的 content[]，不认 prompt / image /
//     response_format 这三个键，缺 content 直接 400
//
// 其余规则相同：reference_images 统一发数组；只有声明过的参数才接受，其余本地
// 拒绝（越界值打到上游既浪费一次计费，又要在错误里反推是哪个参数不对）。
func buildPayload(m modelView, in generateInput, refs []string) (map[string]any, error) {
	// 声明了提示词上限就必须拦：声明只是给 agent 的事前提示，
	// 不拦的话超长提示词会打到上游换一个 400 和一次计费。
	// 按字符数而不是字节数 —— 中文提示词按字节算会短三倍。
	// 上限取「模型声明」与「全局硬顶」的较小值：没声明的模型也要有天花板，
	// 否则 1 MB 提示词会一路打到上游换一个 400，白占一次连接与一次计费。
	limit := LoadSettings().MaxPromptChars
	if m.MaxPromptChars != nil && *m.MaxPromptChars > 0 && *m.MaxPromptChars < limit {
		limit = *m.MaxPromptChars
	}
	if n := len([]rune(in.Prompt)); n > limit {
		return nil, userParamErrorf(CodePromptTooLong, "prompt 长度 %d 超过上限 %d（模型 %s）", n, limit, m.ID)
	}

	if len(refs) > 0 {
		// 只有「已知上限」才拦截。未知就交给上游判定 ——
		// 按 0 处理会误杀图生图能力。
		if m.MaxRefImages != nil {
			if *m.MaxRefImages <= 0 {
				return nil, userParamErrorf(CodeRefImagesUnsupported, "模型 %s 不支持参考图（上限 0），去掉 reference_images 再试", m.ID)
			}
			if len(refs) > *m.MaxRefImages {
				return nil, userParamErrorf(CodeRefImagesExceed, "模型 %s 最多接受 %d 张参考图，收到 %d 张", m.ID, *m.MaxRefImages, len(refs))
			}
		}
	}

	isVideo := m.Kind == "video"
	if isVideo && (in.Size != "" || in.Quality != "") {
		// 之前是「校验后丢掉」：agent 传了 size=1920x1080，拿到 720P 且没有任何提示。
		// 静默丢弃一个直接影响产物形状的参数，比拒绝更难查。
		return nil, userParamErrorf(CodeParamNotDeclared,
			"视频模型 %s 不收 size/quality；画幅用 ratio，清晰度用 resolution", m.ID)
	}
	p := map[string]any{"model": m.ID}
	if isVideo {
		content := make([]map[string]any, 0, len(refs)+1)
		content = append(content, map[string]any{"type": "text", "text": in.Prompt})
		for _, ref := range refs {
			content = append(content, map[string]any{
				"type":      "image_url",
				"image_url": map[string]string{"url": ref},
				// 参考图必须显式声明角色：happyhorse11 只做参考图，缺这个键
				// 会被上游当成「首帧」而拒绝（它不支持首尾帧）。
				"role": "reference_image",
			})
		}
		p["content"] = content
	} else {
		p["prompt"] = in.Prompt
		p["response_format"] = "url"
		if len(refs) > 0 {
			p["image"] = refs
		}
	}

	// size / quality / n 是图片的批量与尺寸概念，视频一次一任务，不上送。
	if in.Size != "" {
		if err := checkEnum(m, "size", in.Size); err != nil {
			return nil, err
		}
		if !isVideo {
			p["size"] = in.Size
		}
	}
	if in.Quality != "" {
		if err := checkEnum(m, "quality", in.Quality); err != nil {
			return nil, err
		}
		if !isVideo {
			p["quality"] = in.Quality
		}
	}
	if in.N != nil {
		if err := checkBatchN(m, float64(*in.N)); err != nil {
			return nil, err
		}
		if !isVideo {
			p["n"] = *in.N
		}
	}
	for k, v := range in.Extra {
		if _, ok := m.Params[k]; !ok && !standardParams[k] {
			return nil, userParamErrorf(CodeParamNotDeclared, "模型 %s 未声明参数 %q，可用：%s", m.ID, k, strings.Join(paramNames(m), ", "))
		}
		if k == "n" {
			// n 是直接乘价格的参数：extra 通道必须和顶层走同一条判定，
			// 否则 extra.n 能绕过 ≥1 与 maxBatchN（只被 relay 的 128 截住）。
			// 也不能落到 checkExtra：它不带「要更多请分多次调用」那句，
			// 同一个超限值从两条通道传进来会得到两种说法。
			n, numeric := toFloat(v)
			if !numeric {
				return nil, userParamErrorf(CodeParamOutOfRange, "n 必须是数字，收到 %v", v)
			}
			if err := checkBatchN(m, n); err != nil {
				return nil, err
			}
			p[k] = v
			continue
		}
		if err := checkExtra(m, k, v); err != nil {
			return nil, err
		}
		p[k] = v
	}

	// 声明里的 default 必须真的生效：models.list 写着 duration 默认 5，agent
	// 不传就该是 5。只透传显式入参会让 default 沦为装饰 —— 上游收到的是缺省，
	// 而方舟把「没给时长」当成 -1（模型自选）直接拒单，理由是时长决定积分。
	// 显式传过的键优先，这里只补缺口。
	for name, spec := range m.Params {
		if spec.Default == nil {
			continue
		}
		if _, present := p[name]; present {
			continue
		}
		p[name] = spec.Default
	}
	return p, nil
}

// maxBatchN 是「模型没声明 n 上限」时的兜底数量。
const maxBatchN = 8

// maxPromptChars 是提示词的全局硬顶（按字符数，中文不按字节算）。
// 3.2 万字符已经远超正常提示词；再长的请求只会换来一个上游 400。
const maxPromptChars = 32000

// checkBatchN 是 n 的唯一判定点。
//
// 顶层字段与 extra 两条通道都收敛到这里：只在顶层拦，extra.n 就能绕过；
// 只在 extra 拦，顶层又会漏。「没配置上限」不该等价于「没有上限」。
func checkBatchN(m modelView, n float64) error {
	if n < 1 {
		return userParamErrorf(CodeParamOutOfRange, "n 必须 ≥ 1，收到 %g", n)
	}
	batchCap := LoadSettings().MaxBatchN
	if _, declared := m.Params["n"]; !declared && n > float64(batchCap) {
		return userParamErrorf(CodeParamOutOfRange,
			"模型 %s 未声明 n 的上限，单次最多 %d；要更多请分多次调用", m.ID, batchCap)
	}
	return checkRange(m, "n", n)
}

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
	// 数值型枚举给最接近的可选值：线上把「5 秒」写成 7 的情况真实发生过，
	// 只说「不可用」会让 agent 在附近继续猜。
	if target, err := strconv.ParseFloat(val, 64); err == nil {
		best, bestDiff := "", math.Inf(1)
		for _, v := range spec.Enum {
			f, ok := toFloat(v)
			if !ok {
				continue
			}
			if d := math.Abs(f - target); d < bestDiff {
				best, bestDiff = fmt.Sprint(v), d
			}
		}
		if best != "" {
			return userParamErrorf(CodeParamOutOfRange, "%s",
				withSpecNote(fmt.Sprintf("%s 取值 %q 不可用，最接近的是 %s；可选：%s",
					name, val, best, strings.Join(opts, ", ")), spec))
		}
	}
	return userParamErrorf(CodeParamOutOfRange, "%s",
		withSpecNote(fmt.Sprintf("%s 取值 %q 不被模型 %s 接受，可选：%s",
			name, val, m.ID, strings.Join(opts, ", ")), spec))
}

// withSpecNote 把声明里的说明附在错误后面。
//
// agent 收到「不可用」时最想知道的是「为什么」——特别是当某个值看起来
// 应该存在（如 1080P）。没有这一句，它下一轮大概率还在同一个方向上试。
func withSpecNote(msg string, spec ParamSpec) string {
	if note := strings.TrimSpace(spec.Note); note != "" {
		return msg + "；说明：" + note
	}
	return msg
}

func checkRange(m modelView, name string, val float64) error {
	spec, ok := m.Params[name]
	if !ok {
		return nil
	}
	if spec.Min != nil && val < *spec.Min {
		return userParamErrorf(CodeParamOutOfRange, "%s 不能小于 %g，收到 %g", name, *spec.Min, val)
	}
	if spec.Max != nil && val > *spec.Max {
		return userParamErrorf(CodeParamOutOfRange, "%s 最多 %g，收到 %g；要更多请分多次调用", name, *spec.Max, val)
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
		// 与 checkEnum 共用一套「最接近值」提示：duration / resolution 这类参数
		// 不在入参结构体里，走的是 Extra 通道。早先只有 checkEnum 会算最接近值，
		// 于是同一个 duration=7，从顶层传和从 extra 传拿到的提示不一样。
		return checkEnum(m, name, fmt.Sprint(val))
	}
	f, numeric := toFloat(val)
	if !numeric {
		return nil
	}
	if spec.Min != nil && f < *spec.Min {
		return userParamErrorf(CodeParamOutOfRange, "%s", withSpecNote(fmt.Sprintf("%s 不能小于 %g", name, *spec.Min), spec))
	}
	if spec.Max != nil && f > *spec.Max {
		return userParamErrorf(CodeParamOutOfRange, "%s", withSpecNote(fmt.Sprintf("%s 最多 %g，收到 %g", name, *spec.Max, f), spec))
	}
	return nil
}

// ── task.get ────────────────────────────────────────────────────────────

type taskInput struct {
	TaskID string `json:"task_id" jsonschema:"video.generate 返回的 task_id"`
}

type taskOutput struct {
	Status  string       `json:"status"` // working | completed | failed
	TaskID  string       `json:"task_id"`
	Model   string       `json:"model"`
	Outputs []outputItem `json:"outputs,omitempty"`
	// Error 与 generateOutput 同形：agent 靠 code / retryable 决定下一步，
	// 纯字符串它只能读中文猜。
	Error  *toolError `json:"error,omitempty"`
	PollMS int        `json:"poll_after_ms,omitempty"`
	// Unchanged 表示这次返回的状态与上一次相同，产物/错误字段被省略。
	// 调用方据此知道「没有新信息」，可以继续按 poll_after_ms 等。
	Unchanged bool `json:"unchanged,omitempty"`
}

func (d *toolDeps) getTask(ctx context.Context, _ *mcpsdk.CallToolRequest, in taskInput) (*mcpsdk.CallToolResult, taskOutput, error) {
	key := apiKeyFrom(ctx)
	caller := callerFrom(ctx)
	// 轮询是所有工具里频率最高的那个：一个 agent 盯 10 个任务、5 秒一轮就是
	// 120 次/分钟。没有窗口的话，这条纯读路径可以被脚本打到任意 QPS。
	if wait, ok := allowTaskGet(caller.TokenId); !ok {
		ms := max(int(wait.Milliseconds()), 1000)
		// 这里**不能**用 failTaskWith：它把 status 写成 failed，agent 会以为任务失败了。
		// 失败的是这次查询，不是任务 —— 对调用方来说正确动作是「等一会儿再查」，
		// 那正是 working 的语义。
		te := &toolError{
			Code:         CodeRateLimited,
			Message:      fmt.Sprintf("task.get 调用过于频繁，%d 秒后再试", (ms+999)/1000),
			Retryable:    true,
			RetryAction:  "wait",
			RetryAfterMS: ms,
			Hint:         "按上一次返回的 poll_after_ms 轮询，不要空转",
		}
		out := taskOutput{Status: "working", TaskID: clipRunes(in.TaskID, 128), Error: te, PollMS: ms}
		res := &mcpsdk.CallToolResult{IsError: true, Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: te.Message}}}
		return res, out, nil
	}
	p, err := parseTaskID(in.TaskID, caller.UserId)
	if err != nil {
		d.observeTaskError(ctx, "", in.TaskID, err)
		switch {
		case errors.Is(err, errTaskIDExpired):
			return d.failTask("", in.TaskID, userParamErrorf(CodeTaskIDExpired,
				"task_id 已过期（有效期 %d 天）；结果可在控制台任务页查看", LoadSettings().TaskIDTTLHours/24))
		case errors.Is(err, errTaskIDForeign):
			return d.failTask("", in.TaskID, userParamErrorf(CodeTaskIDInvalid, "task_id 不属于当前用户"))
		default:
			return d.failTask("", in.TaskID, userParamErrorf(CodeTaskIDInvalid, "task_id 无效或已损坏，需重新生成"))
		}
	}
	t, err := getTransport(p.Kind)
	if err != nil {
		// 类型来自签名内容，能到这里说明句柄不是本服务签的（或签的时候还没有这个类型）。
		d.observeTaskError(ctx, p.Model, in.TaskID, err)
		return d.failTask(p.Model, in.TaskID, userParamErrorf(CodeTaskIDInvalid, "task_id 记录的产物类型不可用：%s", p.Kind))
	}
	settings := LoadSettings()
	cacheKey := taskCacheKey(caller.TokenId, in.TaskID)
	if settings.TaskCacheTTLS > 0 {
		// 终态不会再变：TTL 内直接回缓存，连 relay 都不查。
		if e, ok := loadTaskCache(cacheKey, settings.TaskCacheTTLS); ok && e.status != "working" {
			out := e.out
			out.Unchanged = true
			return textResult(taskSummary(out.Model, out.Status, out)), out, nil
		}
	}

	raw, err := d.up.getTask(ctx, key, t, p.Upstream)
	if err != nil {
		d.observeTaskError(ctx, p.Model, in.TaskID, err)
		return d.failTask(p.Model, in.TaskID, err)
	}
	// 生成端点回扁平结构，查询端点把记录包在 {code,message,data} 里。
	// 只认扁平形状的后果很具体：任务状态永远读成 working，
	// 完成时也取不到 result_url —— 而单测里 mock 的一直是扁平形状。
	resp := unwrapTaskBody(raw)

	status := normalizeStatus(str(resp, "status"))
	out := taskOutput{Status: status, TaskID: in.TaskID, Model: p.Model}
	switch status {
	case "completed":
		out.Outputs = parseOutputs(resp)
	case "working":
		out.PollMS = pollAfter()
	case "failed":
		// 这是「查询成功、任务失败」，不是工具错误：仍回 isError=false，
		// 但错误体与 generate 一样带 code / retryable，agent 才不用猜。
		// cancelled / expired 走 normalizeStatus 也会落到 failed，但它们的下一步
		// 动作不同（取消是用户行为、过期是上游排队过久），不能共用「参数不对」的提示。
		switch strings.ToLower(str(resp, "status")) {
		case "cancelled", "canceled":
			out.Error = &toolError{
				Code: CodeTaskFailed, Message: "任务已被取消", Retryable: false,
				RetryAction: "none", Hint: "取消不是失败原因；需要结果请重新提交一次生成",
			}
		case "expired":
			out.Error = &toolError{
				Code: CodeTaskFailed, Message: "任务已过期（上游未在有效期内完成）", Retryable: true,
				RetryAction: "wait", RetryAfterMS: 5000,
				Hint: "过期通常是上游排队过久；失败任务已退款，可以重新提交",
			}
		default:
			out.Error = classifyTaskFailure(p.Model, upstreamFailReason(resp))
		}
	}
	if settings.TaskCacheTTLS > 0 {
		prev, seen := loadTaskCache(cacheKey, 0)
		storeTaskCache(cacheKey, status, out)
		// 只对「还在跑」做省略：终态第一次到达时要把产物/错误完整下发一次，
		// 之后由上面的缓存分支接住。
		if seen && prev.status == status && status == "working" {
			brief := taskOutput{
				Status: "working", TaskID: in.TaskID, Model: p.Model,
				PollMS: pollAfter(), Unchanged: true,
			}
			return textResult(fmt.Sprintf("任务进行中（%s），状态未变；约 %d 秒后再查",
				p.Model, (pollAfter()+999)/1000)), brief, nil
		}
	}
	return textResult(taskSummary(p.Model, status, out)), out, nil
}

// observeTaskError 把查询失败落成观察样本。轮询失败是「任务声明/句柄写错」
// 的主要信号，但成功轮询不记 —— 300 次/分钟会把日志库刷满。
func (d *toolDeps) observeTaskError(ctx context.Context, modelID, taskID string, err error) {
	if !LoadSettings().RecordTaskErrors {
		return
	}
	observeCall(ctx, callObservation{
		Tool: "task.get", ModelID: modelID, OK: false, ErrorMsg: errText(err),
	})
}

// taskSummary 是 task.get 的 text 出口。
//
// 与 generate 同理：多数客户端只渲染 text，产物地址只放 structuredContent
// 等于「agent 说完成了、用户拿不到链接」。轮询间隔与失败原因也一样 —— 它们
// 是 agent 下一步动作的依据，不该只存在于结构化字段里。
func taskSummary(model, status string, out taskOutput) string {
	switch status {
	case "completed":
		if len(out.Outputs) == 0 {
			// 上游判成功但没给地址：这不是「完成」，如实说清楚，别让 agent 编一个。
			return fmt.Sprintf("任务完成（%s），但上游没有返回产物地址；可在控制台任务页查看", model)
		}
		return appendOutputLinks(fmt.Sprintf("任务完成（%s），共 %d 个产物", model, len(out.Outputs)), out.Outputs)
	case "failed":
		// text 是给人看的一行，上游原文可能很长；全文仍在 structuredContent。
		reason := "上游任务失败"
		if out.Error != nil && out.Error.Message != "" {
			reason = out.Error.Message
		}
		return fmt.Sprintf("任务失败（%s）：%s", model, clipRunes(reason, 200))
	case "working":
		return fmt.Sprintf("任务进行中（%s），约 %d 秒后再查", model, (pollAfter()+999)/1000)
	default:
		return "任务状态：" + status
	}
}

// upstreamFailReason 取失败原因，按「网关归一化过的字段优先」排序。
//
// 只读字符串字段会漏掉 {"error":{"message":...}} 这种 OpenAI 形状 —— 那是
// 上游最常见的错误包装，漏了就只能回一句「上游任务失败」，真正原因留在日志里。
func upstreamFailReason(resp map[string]any) string {
	if s := str(resp, "fail_reason"); s != "" {
		return s
	}
	if e, ok := resp["error"].(map[string]any); ok {
		if s := firstNonEmpty(str(e, "message"), str(e, "code")); s != "" {
			return s
		}
	}
	if s := firstNonEmpty(str(resp, "error"), str(resp, "message")); s != "" {
		return s
	}
	return "上游任务失败"
}

// shouldReleaseIdempotency 决定这条失败能不能让调用方「原样重试」。
//
// 只有「确定什么都没发生」才释放占位：
//   - 上游给了 HTTP 响应（4xx/5xx）：无论成败，这次调用的结论已知；
//   - 拨号超时：连都没连上，请求没发出去。
//
// 等响应超时不能释放 —— 请求已经在路上，上游可能已经受理并计费，
// 释放等于放行第二次扣费。
func shouldReleaseIdempotency(err error) bool {
	// 没发出去：释放。
	var ns *errNotSent
	if errors.As(err, &ns) {
		return true
	}
	var ut *errUpstreamTimeout
	if errors.As(err, &ut) {
		return ut.BeforeSend
	}
	// 有 HTTP 状态码：这次调用的结论已知（无论 4xx 还是 5xx），释放。
	// 200 但解析失败、读响应体失败都落到这里返回 false —— 结论未知，
	// 宁可让调用方去后台确认，也不要放行第二次扣费。
	var ue *upError
	return errors.As(err, &ue)
}

// failTask 把查询失败包成 isError + 结构化错误。
//
// 与 generate 的 fail 同构，但返回 taskOutput：task.get 的 outputSchema 声明了
// error 字段，只有把 out 一起返回（err 传 nil），SDK 才会写 structuredContent。
// 之前直接 return err，SDK 只能包成一段纯文本，声明出来的 error 永远拿不到。
func (d *toolDeps) failTask(modelId, taskID string, err error) (*mcpsdk.CallToolResult, taskOutput, error) {
	return d.failTaskWith(modelId, taskID, classifyToolError(modelId, err))
}

// failTaskWith 用现成的 toolError 组装失败响应（限流这类不是 error 值）。
func (d *toolDeps) failTaskWith(modelId, taskID string, te *toolError) (*mcpsdk.CallToolResult, taskOutput, error) {
	// 回显要截断：非法 task_id 可能是任意长的输入，原样塞回 structuredContent
	// 等于把响应放大成请求的大小。调用方自己知道发的是什么。
	out := taskOutput{Status: "failed", TaskID: clipRunes(taskID, 128), Model: modelId, Error: te}
	res := &mcpsdk.CallToolResult{IsError: true}
	if te != nil {
		res.Content = []mcpsdk.Content{&mcpsdk.TextContent{Text: te.Message}}
	}
	return res, out, nil
}

// taskToGenerateOutput 把任务快照转成 generate 的输出形状。
//
// 重放时复用任务查询的结果，两者字段几乎一致，但状态机不同：
// taskOutput 没有 usage / upstream（这两项只在首次生成时有意义）。
func taskToGenerateOutput(t taskOutput) generateOutput {
	return generateOutput{
		Status: t.Status, Model: t.Model, TaskID: t.TaskID,
		Outputs: t.Outputs, Error: t.Error, PollMS: t.PollMS,
	}
}

// classifyTaskFailure 判断「任务失败」这件事该不该重试。
//
// 之前一律 retryable=false，但失败原因可能是上游瞬时故障（我们自己复现过 502）。
// 失败任务在 apiduo 侧会走 RefundTaskQuota 退款，所以引导重试不会造成二次扣费；
// 反过来，内容违规这类重试多少次都一样，必须明确禁止。
func classifyTaskFailure(modelID, reason string) *toolError {
	if rule := matchErrorRule(modelID, 0, reason); rule != nil {
		return &toolError{
			Code:         CodeTaskFailed,
			Message:      reason,
			Retryable:    rule.Retryable,
			RetryAction:  rule.RetryAction,
			RetryAfterMS: rule.RetryAfterMs,
			Hint:         rule.Hint,
		}
	}
	if transientTaskFailure(reason) {
		return &toolError{
			Code:         CodeTaskFailed,
			Message:      reason,
			Retryable:    true,
			RetryAction:  "wait",
			RetryAfterMS: 30000,
			Hint:         "上游临时故障，失败任务已退款；可以稍后重新提交",
		}
	}
	return &toolError{
		Code:        CodeTaskFailed,
		Message:     reason,
		Retryable:   false,
		RetryAction: "none",
		Hint:        "不要用同样参数重试；先确认参数或换模型",
	}
}

// transientTaskFailure 只看「上游自己抖了」这一类措辞。
// 宁可漏判也不要把内容违规判成可重试 —— 那会让 agent 反复提交同一个被拒的内容。
func transientTaskFailure(reason string) bool {
	s := strings.ToLower(reason)
	for _, k := range []string{
		"timeout", "timed out", "502", "503", "504", "rate limit",
		"temporarily", "try again", "retry later", "unavailable", "overloaded",
		"connection reset", "busy", "internal error",
		// 网关兜底文案：上游只说「失败」没给原因，按瞬时处理（失败任务会退款）
		"upstream reported failure",
	} {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// ── 小工具 ──────────────────────────────────────────────────────────────

func textResult(s string) *mcpsdk.CallToolResult {
	return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: s}}}
}

// fail 把错误包成 isError 结果，让 agent 拿到码与下一步，而不是一段中文。
func (d *toolDeps) fail(modelId string, err error) (*mcpsdk.CallToolResult, generateOutput, error) {
	return d.failWith(modelId, classifyToolError(modelId, err))
}

func (d *toolDeps) failWith(modelId string, te *toolError) (*mcpsdk.CallToolResult, generateOutput, error) {
	out := generateOutput{Status: "failed", Model: modelId, Error: te}
	res := &mcpsdk.CallToolResult{IsError: true}
	if te != nil {
		res.Content = []mcpsdk.Content{&mcpsdk.TextContent{Text: te.Message}}
	}
	return res, out, nil
}

// appendOutputLinks 把产物地址写进人读文本。
//
// 只放在 structuredContent 里不够：多数 MCP 客户端只渲染 text content，
// agent 于是回一句「已生成」而用户拿不到图。这条摘要就是它们的唯一出口。
func appendOutputLinks(summary string, out []outputItem) string {
	lines := make([]string, 0, len(out))
	for i, o := range out {
		switch {
		case o.URL != "" && len(out) == 1:
			lines = append(lines, o.URL)
		case o.URL != "":
			lines = append(lines, fmt.Sprintf("%d. %s", i+1, o.URL))
		case o.B64JSON != "":
			lines = append(lines, fmt.Sprintf("%d. base64 内联（b64_json，%d 字符）", i+1, len(o.B64JSON)))
		}
	}
	if len(lines) == 0 {
		return summary
	}
	if len(lines) == 1 {
		return summary + "：" + lines[0]
	}
	return summary + "：\n" + strings.Join(lines, "\n")
}

// absorbArkContent 把方舟形状的 content 落到 prompt / reference_images 上。
//
// 显式给出的 prompt 优先：两者同时存在时，说明调用方有意覆盖，不要用 content 盖掉它。
func absorbArkContent(raw any, in *generateInput) bool {
	prompt, refs, ok := parseArkContent(raw)
	if !ok {
		return false
	}
	if strings.TrimSpace(in.Prompt) == "" && prompt != "" {
		in.Prompt = prompt
	}
	if in.ReferenceImages == nil && len(refs) > 0 {
		in.ReferenceImages = refs
	}
	return true
}

// parseArkContent 把方舟形状的 content 数组拆成提示词与参考图。
//
// 只认方舟文档里的两种 part：{type:text,text} 与 {type:image_url,image_url:{url}}。
// 认不出就返回 ok=false，让调用方照旧报「未声明参数」—— 静默吞掉一个不认识的
// 形状，比直接报错更难查。
func parseArkContent(raw any) (string, []string, bool) {
	parts, ok := raw.([]any)
	if !ok {
		return "", nil, false
	}
	texts := make([]string, 0, 1)
	refs := make([]string, 0, 1)
	for _, item := range parts {
		part, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch fmt.Sprint(part["type"]) {
		case "text":
			if text, ok := part["text"].(string); ok && strings.TrimSpace(text) != "" {
				texts = append(texts, text)
			}
		case "image_url":
			switch v := part["image_url"].(type) {
			case map[string]any:
				if u, ok := v["url"].(string); ok && strings.TrimSpace(u) != "" {
					refs = append(refs, u)
				}
			case string:
				if strings.TrimSpace(v) != "" {
					refs = append(refs, v)
				}
			}
		}
	}
	if len(texts) == 0 && len(refs) == 0 {
		return "", nil, false
	}
	return strings.Join(texts, "\n"), refs, true
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
		if err := validateRefURL(t); err != nil {
			return nil, err
		}
		return []string{t}, nil
	case []any:
		out := make([]string, 0, len(t))
		for _, it := range t {
			sv, ok := it.(string)
			if !ok {
				return nil, userParamErrorf(CodeParamInvalid, "reference_images 数组元素必须是字符串，收到 %T", it)
			}
			if sv = strings.TrimSpace(sv); sv != "" {
				if err := validateRefURL(sv); err != nil {
					return nil, err
				}
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
	return nil, userParamErrorf(CodeParamInvalid, "reference_images 需为字符串或字符串数组，收到 %T", v)
}

// validateRefURL 只放行 http(s) 与 data:image/video。
//
// 之前什么都不校验：空格、相对路径、file:// 都会被原样转给上游，换回来一个
// 看不懂的 400。data URL 是本地文件的正路 —— 客户端把图片 base64 内联进来，
// 走的是 /v1/images/generations 的 base64 参考图，不需要额外上传接口。
func validateRefURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return userParamErrorf(CodeParamInvalid, "参考图地址不合法：%s", clipRunes(raw, 80))
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return nil
	case "data":
		lower := strings.ToLower(raw)
		if strings.HasPrefix(lower, "data:image/") || strings.HasPrefix(lower, "data:video/") {
			return nil
		}
		return userParamErrorf(CodeParamInvalid, "data URL 只接受 data:image/* 或 data:video/*")
	case "":
		return userParamErrorf(CodeParamInvalid, "参考图必须是绝对 URL 或 data URL：%s", clipRunes(raw, 80))
	default:
		return userParamErrorf(CodeParamInvalid, "参考图协议 %s 不支持（只收 http / https / data）", u.Scheme)
	}
}

// parseOutputs 从上游响应里提取产物地址，兼容几种常见形状。
func parseOutputs(resp map[string]any) []outputItem {
	out := make([]outputItem, 0, 4)
	// 同一个地址经常从两条路径同时出现：方舟任务的 content.video_url 由网关
	// 从 result_url 补出来，两边都在时不去重就会把同一个视频报成两个产物 ——
	// text 里连着两行一样的链接，用户会以为生成了两个。
	seen := make(map[string]bool, 4)
	add := func(it outputItem) {
		key := "u:" + it.URL
		if it.URL == "" {
			key = "b:" + it.B64JSON
		}
		if key == "u:" || key == "b:" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, it)
	}
	for _, item := range toSlice(resp["data"]) {
		u := str(item, "url")
		b := str(item, "b64_json")
		switch {
		case u != "":
			add(outputItem{URL: u, MimeType: str(item, "mime_type")})
		case b != "":
			add(outputItem{B64JSON: b, MimeType: str(item, "mime_type")})
		}
	}
	for _, item := range toSlice(resp["outputs"]) {
		if u := str(item, "url"); u != "" {
			add(outputItem{URL: u, MimeType: str(item, "mime_type")})
		}
	}
	if u := str(resp, "url"); u != "" {
		add(outputItem{URL: u})
	}
	// apiduo 的视频任务：{url, format}
	if u := str(resp, "video_url"); u != "" {
		add(outputItem{URL: u})
	}
	// apiduo 的任务 DTO：result_url 才是最终产物地址
	if u := str(resp, "result_url"); u != "" {
		add(outputItem{URL: u})
	}
	// 方舟任务对象：产物在 content.video_url，比上面几个都深一层
	if c, ok := resp["content"].(map[string]any); ok {
		if u := str(c, "video_url"); u != "" {
			add(outputItem{URL: u})
		}
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

// pollAfterMS 是建议的两次 task.get 间隔。
//
// 视频任务动辄 3-4 分钟，2 秒一轮等于把同一个任务问 100 多次；5 秒够快
// 让 agent 觉得有进展，又不至于把一个任务的等待放大成上百次请求。
const pollAfterMS = 5000

// pollAfter 返回当前配置的建议轮询间隔（毫秒）。
func pollAfter() int { return LoadSettings().PollAfterMS }

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
	// expired 是上游的终态之一（本地超时、不再查上游），漏掉它会掉进
	// default 被当成 working，agent 就会一直轮询一个再也不会变的任务。
	case "failed", "error", "cancelled", "canceled", "failure", "expired":
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
