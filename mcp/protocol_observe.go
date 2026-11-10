package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// apiKeyCtxKey 承载本次请求的令牌。放在包内私有类型上，
// 避免与其他包的 context key 撞车。
type apiKeyCtxKey struct{}

func withAPIKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, apiKeyCtxKey{}, key)
}

func apiKeyFrom(ctx context.Context) string {
	s, _ := ctx.Value(apiKeyCtxKey{}).(string)
	return s
}

// callerCtxKey 承载本次调用的令牌归属。
//
// 协议端点挂在 gin.WrapH 后面，http.Handler 拿不到 gin.Context，所以由
// captureCaller 中间件搬一次。不搬的后果是日志只能记成 userId=0（系统）：
// 用户在「我的日志」里看不到自己发起的 MCP 调用，用量也对不上人。
type callerCtxKey struct{}

// mcpCaller 是日志归属需要的调用者信息，只取三项 —— 够定位到人、到令牌，
// 又不把用户档案整份搬进 context。
type mcpCaller struct {
	UserId   int
	Username string
	TokenId  int
}

func withCaller(ctx context.Context, caller mcpCaller) context.Context {
	return context.WithValue(ctx, callerCtxKey{}, caller)
}

func callerFrom(ctx context.Context) mcpCaller {
	caller, _ := ctx.Value(callerCtxKey{}).(mcpCaller)
	return caller
}

// callObservation 是一次调用的观察样本。
//
// 只记结构性参数：尺寸、时长、数量这类可枚举的值。提示词内容一个字都不落盘 ——
// 它可能含用户隐私，而且对「参数分布」这个统计目的没有价值。
type callObservation struct {
	// Tool 是工具名（image.generate / video.generate）。写进日志正文，
	// 让日志页一眼看出「这是 MCP 的哪个动作」，而不是一片 "mcp call"。
	Tool     string
	UserId   int
	Username string
	TokenId  int
	ModelID  string
	Params   map[string]string
	OK       bool
	ErrorMsg string
	Latency  time.Duration
}

// observableParams 是允许落盘的参数白名单。
//
// 白名单而不是黑名单：新增一个模型特有参数时，默认不记录比默认记录安全。
var observableParams = map[string]bool{
	"size": true, "duration": true, "n": true, "resolution": true,
	"aspect_ratio": true, "quality": true, "format": true,
}

var observeQueue = make(chan callObservation, 1024)

var observeOnce sync.Once

// startObserver 启动单写协程。
//
// 为什么不用「每次开一个 goroutine」：突发流量下 goroutine 数量不可控，
// 而且日志库的写入连接会被打满。队列满了就丢弃 —— 观察数据丢样本是可接受的，
// 阻塞生成请求不可接受。
func startObserver() {
	observeOnce.Do(func() {
		go func() {
			for o := range observeQueue {
				writeObservation(o)
			}
		}()
	})
}

// observeCall 把样本投进队列，永不阻塞调用方。
//
// 归属从 ctx 补，而不是让每个调用点自己填：调用点只关心「哪个模型、什么参数、
// 成没成」，把身份复制三遍迟早漏一处。
func observeCall(ctx context.Context, o callObservation) {
	caller := callerFrom(ctx)
	o.UserId, o.Username, o.TokenId = caller.UserId, caller.Username, caller.TokenId
	startObserver()
	select {
	case observeQueue <- o:
	default:
		// 队列满：丢样本。这里绝不能阻塞或 panic —— 观察是旁路，不是主链路。
	}
}

func writeObservation(o callObservation) {
	if model.LOG_DB == nil {
		return
	}
	adminInfo := map[string]any{
		"param_snapshot": o.Params,
		"mcp":            true,
	}
	if o.Tool != "" {
		adminInfo["tool"] = o.Tool
	}
	if o.ErrorMsg != "" {
		adminInfo["upstream_error"] = o.ErrorMsg
	}
	other := map[string]any{"admin_info": adminInfo}
	raw, err := json.Marshal(other)
	if err != nil {
		return
	}
	logType := logTypeConsume
	if !o.OK {
		logType = logTypeError
	}
	content := "mcp call"
	if o.Tool != "" {
		content = "mcp " + o.Tool
	}
	row := &model.Log{
		UserId:    o.UserId,
		Username:  o.Username,
		CreatedAt: common.GetTimestamp(),
		Type:      logType,
		ModelName: o.ModelID,
		TokenId:   o.TokenId,
		// quota 留 0：MCP 层的这条日志只用于观察，计费由 relay 完成。
		// 记成 0 而不是真实额度，避免运营看日志页时把两笔账加在一起。
		Quota:   0,
		UseTime: int(o.Latency.Seconds()),
		Content: content,
		Other:   string(raw),
	}
	// 必须走 model.EnqueueLog（= createLog）：它会补 request_id 并交给异步批量写入器。
	//
	// 直接 LOG_DB.Create 在本地 SQLite 上看着正常，到线上有一条隐蔽的失败链：
	//   1) 单行 INSERT 在 ClickHouse 里每次生成一个 data part，量上来触发 Too many parts；
	//   2) 线上 logs 是 ReplacingMergeTree(ORDER BY created_at, request_id)，
	//      request_id 为空的同秒行会在后台合并时折叠成一行 —— 样本静默变少，面板看不出来。
	// 失败仍然不重试：观察是旁路，丢一条比刷错误日志好。
	_ = model.EnqueueLog(row)
}

// snapshotParams 从请求体里挑出可观察的参数。
func snapshotParams(payload map[string]any) map[string]string {
	out := make(map[string]string, len(observableParams))
	for k, v := range payload {
		if !observableParams[k] {
			continue
		}
		switch v.(type) {
		case string, float64, int, int64, bool, json.Number:
			out[k] = fmt.Sprint(v)
		}
	}
	// 参考图只记张数，不记 URL —— URL 常带签名参数，等同凭证。
	if refs, ok := payload["image"].([]string); ok {
		out["reference_images"] = fmt.Sprint(len(refs))
	}
	return out
}

// snapshotInput 把「还没走到 payload」的入参也收成一份结构快照。
//
// 本地校验失败时没有 payload 可快照，但拒绝的原因往往就在这些参数上
// （size 越界、n 太大、参考图超限），所以按同一份白名单从入参重新挑一遍。
// 提示词照样不进快照 —— 它和「参数分布」无关，且可能是用户隐私。
func snapshotInput(in generateInput, refs []string) map[string]string {
	raw := make(map[string]any, 3+len(in.Extra))
	if in.Size != "" {
		raw["size"] = in.Size
	}
	if in.Quality != "" {
		raw["quality"] = in.Quality
	}
	if in.N != nil {
		raw["n"] = *in.N
	}
	for k, v := range in.Extra {
		raw[k] = v
	}
	out := snapshotParams(raw)
	if len(refs) > 0 {
		out["reference_images"] = fmt.Sprint(len(refs))
	}
	return out
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}
