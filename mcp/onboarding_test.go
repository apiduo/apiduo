package mcp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// 线上最高频的一类报错是「未声明参数 content」：agent 读方舟文档后照抄
// content 数组，而工具入参是 prompt + reference_images。这里锁住「同一件事
// 的另一种说法能被接受」，而不是让调用方重说一遍。
func TestAbsorbArkContent(t *testing.T) {
	in := &generateInput{}
	ok := absorbArkContent([]any{
		map[string]any{"type": "text", "text": "一只猫在窗台"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://x/1.png"}},
	}, in)
	if !ok {
		t.Fatal("方舟形状应当被吸收")
	}
	if in.Prompt != "一只猫在窗台" {
		t.Errorf("prompt = %q", in.Prompt)
	}
	refs, _ := in.ReferenceImages.([]string)
	if len(refs) != 1 || refs[0] != "https://x/1.png" {
		t.Errorf("reference_images = %#v", in.ReferenceImages)
	}

	// 显式 prompt 优先：两者同时给出时，content 不许盖掉调用方写的那句。
	in2 := &generateInput{Prompt: "显式提示词"}
	absorbArkContent([]any{map[string]any{"type": "text", "text": "content 里的"}}, in2)
	if in2.Prompt != "显式提示词" {
		t.Errorf("显式 prompt 被覆盖成 %q", in2.Prompt)
	}

	// 认不出的形状不吞：让它照旧走「未声明参数」，静默丢弃参数更难查。
	if absorbArkContent([]any{map[string]any{"type": "video_url"}}, &generateInput{}) {
		t.Error("不认识的 part 不该被吸收")
	}
	if absorbArkContent("不是数组", &generateInput{}) {
		t.Error("非数组不该被吸收")
	}
}

// 数值型枚举给最接近的值。线上真实发生过 duration=7（可选 5/10/15）。
func TestCheckEnumSuggestsNearest(t *testing.T) {
	m := modelView{ID: "v", Params: map[string]ParamSpec{
		"duration": {Enum: []any{5, 10, 15}},
	}}
	err := checkEnum(m, "duration", "7")
	if err == nil {
		t.Fatal("7 应当被拒")
	}
	if !strings.Contains(err.Error(), "最接近的是 5") {
		t.Errorf("错误里应给出最接近值，实际 %q", err.Error())
	}
	var pe *paramError
	if !errors.As(err, &pe) || pe.Code != CodeParamOutOfRange {
		t.Errorf("应当是可分类的参数错误，实际 %#v", err)
	}

	if err := checkEnum(m, "duration", "10"); err != nil {
		t.Errorf("合法值不该被拒: %v", err)
	}
}

// 模型名写错时要给候选。线上出现过 agent 直接写 nano-banana-2（本部署没有）。
func TestModelNotFoundListsCandidates(t *testing.T) {
	views := []modelView{
		{ID: "flux-3-image", Kind: "image"},
		{ID: "qwen-image-3.0", Kind: "image"},
	}
	_, err := pickView(views, "image", "nano-banana-2")
	if err == nil {
		t.Fatal("未注册模型应当被拒")
	}
	msg := err.Error()
	if !strings.Contains(msg, "flux-3-image") || !strings.Contains(msg, "qwen-image-3.0") {
		t.Errorf("错误里应列出可选模型，实际 %q", msg)
	}
}

// 产物地址必须在 text content 里 —— 多数客户端只渲染 text，只给
// structuredContent 会变成「agent 说生成了、用户看不到图」。
func TestAppendOutputLinks(t *testing.T) {
	one := appendOutputLinks("生成完成", []outputItem{{URL: "https://x/1.png"}})
	if !strings.HasSuffix(one, "：https://x/1.png") {
		t.Errorf("单图应直接追加地址，实际 %q", one)
	}

	two := appendOutputLinks("生成完成", []outputItem{{URL: "u1"}, {URL: "u2"}})
	if !strings.Contains(two, "1. u1") || !strings.Contains(two, "2. u2") {
		t.Errorf("多图应逐行编号，实际 %q", two)
	}

	b64 := appendOutputLinks("生成完成", []outputItem{{B64JSON: "abcdef"}})
	if !strings.Contains(b64, "base64") {
		t.Errorf("b64 情形应说明内联，实际 %q", b64)
	}
}

// 本地校验错误要带稳定码，agent 才能分支，而不是解析中文。
func TestClassifyToolErrorCodes(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{userParamErrorf(CodePromptRequired, "prompt 不能为空"), CodePromptRequired},
		{&errKindMismatch{Model: "m", Actual: "search", Want: "image"}, CodeKindMismatch},
		{&errModelNotAvailable{Model: "x", Kind: "image", Available: []string{"a"}}, CodeModelNotFound},
		{errNoModelOfKind("image"), CodeModelNotFound},
		{errors.New("随便什么错"), CodeInternal},
	}
	for _, c := range cases {
		got := classifyToolError("m", c.err)
		if got == nil || got.Code != c.want {
			t.Errorf("%v: code = %#v, want %s", c.err, got, c.want)
		}
		if got.Retryable {
			t.Errorf("%v: 本地错误不该标可重试", c.err)
		}
	}
}

// 声明里的 note 必须随错误下发。1080P 是真实案例：档位存在但当前号池开不了，
// 只列「可选 720P」会让 agent 反复试探，写清原因它才会换方向。
func TestCheckEnumCarriesNote(t *testing.T) {
	m := modelView{ID: "v", Params: map[string]ParamSpec{
		"resolution": {Enum: []any{"720P"}, Note: "1080P 需会员，当前号池不可用"},
	}}
	err := checkEnum(m, "resolution", "1080P")
	if err == nil {
		t.Fatal("1080P 应当被拒")
	}
	if !strings.Contains(err.Error(), "1080P 需会员") {
		t.Errorf("错误里应带声明说明，实际 %q", err.Error())
	}
}

// note 要随 models.list 下发，否则 agent 看不到「为什么这个值不可用」。
func TestCardCarriesParamNote(t *testing.T) {
	p := ModelProfile{
		ModelId:    "m",
		Kind:       "image",
		Parameters: `{"size":{"note":"任意 WxH，仅用于推导比例"}}`,
	}
	card := viewFromProfile(p).card()
	raw, err := json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "仅用于推导比例") {
		t.Errorf("card 应带出 note，实际 %s", raw)
	}
}

// 不指定模型时回落第一个已启用模型，并在返回里说清用了哪个、还有哪些可选。
// 硬性必填会把「画张图」变成「先查清单再画」，用户体感是「用不了」。
func TestPickViewModelFallback(t *testing.T) {
	views := []modelView{
		{ID: "flux-3-image", Kind: "image"},
		{ID: "qwen-image-3.0", Kind: "image"},
	}
	got, err := pickView(views, "image", "")
	if err != nil {
		t.Fatalf("不传 model 不该报错: %v", err)
	}
	if got.ID != "flux-3-image" {
		t.Errorf("应回落到第一个，实际 %s", got.ID)
	}
	if note := defaultedModelNote(views, "image", got.ID); !strings.Contains(note, "qwen-image-3.0") {
		t.Errorf("引导语应列出其他可选模型，实际 %q", note)
	}

	// 该类型下一个模型都没有：这是「去后台注册」，不是「少传参数」。
	if _, err := pickView(views, "video", ""); err == nil {
		t.Fatal("没有 video 模型时应当被拒")
	} else {
		var nk errNoModelOfKind
		if !errors.As(err, &nk) {
			t.Fatalf("应当是 errNoModelOfKind，实际 %T", err)
		}
	}
}

// models.list 的 text 必须是可读清单。只回「共 N 个模型」时，
// 只渲染 text 的客户端拿不到任何选择依据。
func TestModelsSummaryListsIDs(t *testing.T) {
	out := modelsSummary([]map[string]any{
		{"id": "flux-3-image", "kind": "image", "summary": "写实，多参考图"},
		{"id": "qwen-image-3.0", "kind": "image", "summary": strings.Repeat("很长的简介", 40)},
	})
	if !strings.Contains(out, "flux-3-image") || !strings.Contains(out, "不指定 model") {
		t.Errorf("清单缺少关键信息: %q", out)
	}
	if strings.Contains(out, strings.Repeat("很长的简介", 40)) {
		t.Error("简介应当被截断")
	}
}

// 只传方舟形状的 content、不传 prompt，必须能走到 buildPayload。
//
// 这条是端到端才暴露的：单测里 absorbArkContent 一直是好的，但 SDK 会在
// schema 阶段先拦「缺少 prompt」，agent 照方舟文档传 content 时永远进不来。
func TestContentOnlyCallReachesUpstream(t *testing.T) {
	setupDB(t)
	rec := mockUpstream(t)
	resetProtocol()

	p := ModelProfile{
		ModelId: "content-only", Kind: "video", Protocol: "ark",
		Enabled: true, Async: true,
		Parameters: `{"duration":{"enum":[5,10]}}`,
	}
	if err := SaveProfile(&p); err != nil {
		t.Fatal(err)
	}
	srv := mcpServer(t)

	res := callTool(t, srv.URL, "sk-test", "video.generate", map[string]any{
		"model":    "content-only",
		"content":  []any{map[string]any{"type": "text", "text": "海浪拍打礁石"}},
		"duration": 5,
	})
	if res["status"] != "working" {
		t.Fatalf("应当已提交，实际 %v", res)
	}

	rec.mu.Lock()
	body := rec.video
	rec.mu.Unlock()
	content, _ := body["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("上游没收到 content，实际 %v", body)
	}
	first, _ := content[0].(map[string]any)
	if first["type"] != "text" || first["text"] != "海浪拍打礁石" {
		t.Errorf("提示词没有从 content 拆出来，实际 %v", first)
	}
}
