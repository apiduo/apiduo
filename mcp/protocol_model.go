package mcp

import (
	"sort"
	"strings"
)

// modelView 是工具层看到的能力视图。
//
// 它由「mcp_model_profiles 的一行」直接拼成，不做多级合并 ——
// 与旧版 MCP 的 catalog 不同：那里要合并上游 /v1/models、协议模板、覆盖层三份，
// 现在能力的唯一来源就是这张表，上游列表只用来回答「这个模型现在真的可用吗」。
//
// 不落库、不缓存：结构太轻，缓存省下的远不如失效逻辑带来的复杂度。
type modelView struct {
	ID             string
	Kind           string
	Transport      string
	Async          bool
	MaxRefImages   *int
	MaxRefVideos   *int
	MaxPromptChars *int
	Params         map[string]ParamSpec
	Summary        string
	Tags           []string
	Cost           string
	Priority       int
}

func viewFromProfile(p ModelProfile) modelView {
	v := modelView{
		ID:             p.ModelId,
		Kind:           p.Kind,
		Transport:      p.Kind,
		Async:          p.Async,
		MaxRefImages:   p.MaxRefImages,
		MaxRefVideos:   p.MaxRefVideos,
		MaxPromptChars: p.MaxPromptChars,
		Params:         ParseSpecs(p.Parameters),
		Summary:        p.Summary,
		Cost:           p.Cost,
		Priority:       p.Priority,
	}
	if p.Tags != "" {
		for _, t := range strings.Split(p.Tags, ",") {
			if t = strings.TrimSpace(t); t != "" {
				v.Tags = append(v.Tags, t)
			}
		}
	}
	return v
}

// card 是 models.list 返回的单条画像。字段命名对齐 MCP 契约 v4，
// 但只输出「有值」的项：把未声明的键输出成 null/0 会让 agent 误判为「不支持」。
func (m modelView) card() map[string]any {
	exec := map[string]any{"task": "sync"}
	if m.Async {
		exec["task"] = "async"
	}
	out := map[string]any{
		"id": m.ID,
		// 管理 API 的字段叫 model_id，这里补一份同义键：agent 从 models.list
		// 拿到 id 之后去调 /profiles/by-model?model_id= 是最常见的接法，
		// 两个接口用不同名字只会让人多踩一次 404。id 保留，不动既有契约。
		"model_id":  m.ID,
		"object":    "model",
		"kind":      m.Kind,
		"execution": exec,
	}
	if m.Summary != "" {
		out["summary"] = m.Summary
	}
	if len(m.Tags) > 0 {
		out["tags"] = m.Tags
	}
	if m.Cost != "" {
		out["cost"] = m.Cost
	}
	input := map[string]any{}
	if m.MaxRefImages != nil {
		input["max_reference_images"] = *m.MaxRefImages
	}
	if m.MaxRefVideos != nil {
		input["max_reference_videos"] = *m.MaxRefVideos
	}
	if m.MaxPromptChars != nil {
		input["max_prompt_chars"] = *m.MaxPromptChars
	}
	if len(input) > 0 {
		out["input"] = input
	}
	if len(m.Params) > 0 {
		out["parameters"] = m.Params
	}
	return out
}

// listEnabledViews 返回已启用声明的模型，按 kind、id 排序。
//
// 只暴露「已注册且启用」的模型是刻意的：未注册的模型 agent 不知道它的参数约束，
// 放出去只会让它靠猜 —— 猜错的代价是 400 和一次计费。
func listEnabledViews() ([]modelView, error) {
	profiles, err := ListProfiles()
	if err != nil {
		return nil, err
	}
	out := make([]modelView, 0, len(profiles))
	for _, p := range profiles {
		if !p.Enabled {
			continue
		}
		out = append(out, viewFromProfile(p))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		// 默认模型由运维用 priority 指定，不能交给字典序 ——
		// 否则新加一个名字靠前的模型会静默变成默认。
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func pickView(views []modelView, kind, want string) (modelView, error) {
	byKind := make([]modelView, 0, len(views))
	for _, v := range views {
		if v.Kind == kind {
			byKind = append(byKind, v)
		}
	}
	// 调用方点名了模型，就先回答「这个模型到底能不能用」。
	//
	// 反过来（先判 len(byKind)==0）会把「你挑错了模型」说成「你去注册模型」：
	// 线上实测给 image.generate 传一个 search 模型，收到的是
	// 「没有已启用的 image 模型，请先在能力注册里添加」，agent 会照着去建声明，
	// 而真正要改的是 model 参数。
	want = strings.TrimSpace(want)
	if want == "" {
		// 不点名就回落第一个已启用模型，而不是报错。
		//
		// 这条是刻意的：MCP 的用户是「让 agent 帮忙做事」的人，不是读文档的开发者。
		// 硬性要求 model 会把「画张图」变成「先查清单再画」，多一轮往返，
		// 用户体感是「用不了」。默认可用的代价是可能选错，所以返回里必须说清
		// 用了哪个、还有哪些可选 —— 引导放在结果里，不放在门槛上。
		if len(byKind) == 0 {
			return modelView{}, errNoModelOfKind(kind)
		}
		return byKind[0], nil
	}
	if want != "" {
		for _, v := range byKind {
			if v.ID == want {
				return v, nil
			}
		}
		// 明确报「类型不对」而不是「不存在」，否则用户会以为模型没注册。
		for _, v := range views {
			if v.ID == want {
				return modelView{}, &errKindMismatch{Model: want, Actual: v.Kind, Want: kind}
			}
		}
		ids := make([]string, 0, len(byKind))
		for _, v := range byKind {
			ids = append(ids, v.ID)
		}
		return modelView{}, &errModelNotAvailable{Model: want, Kind: kind, Available: ids}
	}
	return modelView{}, errNoModelOfKind(kind)
}

type errNoModelOfKind string

func (e errNoModelOfKind) Error() string {
	return "没有已启用的 " + string(e) + " 模型，请先在能力注册里添加"
}

type errKindMismatch struct{ Model, Actual, Want string }

func (e *errKindMismatch) Error() string {
	return "模型 " + e.Model + " 的产物类型是 " + e.Actual + "，不能用于 " + e.Want
}

type errModelNotAvailable struct {
	Model     string
	Kind      string
	Available []string
}

func (e *errModelNotAvailable) Error() string {
	// 给候选而不是只说「没注册」：线上出现过 agent 凭训练知识直接写
	// nano-banana-2 —— 它不在本部署的能力表里，光说「未注册」它下一轮还会猜。
	if len(e.Available) == 0 {
		return "模型 " + e.Model + " 未注册或已停用，先用 models.list 查看可用模型"
	}
	return "模型 " + e.Model + " 未注册或已停用；可用的 " + e.Kind + " 模型：" +
		strings.Join(e.Available, ", ") + "。先用 models.list 查看能力"
}
