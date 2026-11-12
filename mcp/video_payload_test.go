package mcp

import (
	"testing"
)

// videoModel 是 happyhorse1.1 的最小声明：视频、只认方舟形状。
func videoModel() modelView {
	refs := 9
	return modelView{
		ID:           "happyhorse1.1",
		Kind:         "video",
		Async:        true,
		MaxRefImages: &refs,
		Params: map[string]ParamSpec{
			"content":    {},
			"resolution": {Enum: []any{"720P", "1080P"}, Default: "720P"},
			"duration":   {Enum: []any{5, 10, 15}, Default: 5},
			"ratio":      {Enum: []any{"16:9", "9:16", "adaptive"}, Default: "16:9"},
			"n":          {Min: floatPtr(1), Max: floatPtr(1)},
		},
	}
}

func floatPtr(v float64) *float64 { return &v }

func TestBuildPayloadVideoSendsArkContent(t *testing.T) {
	p, err := buildPayload(videoModel(), generateInput{Prompt: "镜头推进"}, nil)
	if err != nil {
		t.Fatalf("buildPayload: %v", err)
	}

	// 方舟只认 content；这三个键会让上游回 400「content 必须是非空数组」
	for _, banned := range []string{"prompt", "image", "response_format"} {
		if _, present := p[banned]; present {
			t.Fatalf("视频载荷不该带 %q: %v", banned, p)
		}
	}

	parts, ok := p["content"].([]map[string]any)
	if !ok || len(parts) != 1 {
		t.Fatalf("content = %#v, want 单个 text part", p["content"])
	}
	if parts[0]["type"] != "text" || parts[0]["text"] != "镜头推进" {
		t.Fatalf("text part = %#v", parts[0])
	}
}

func TestBuildPayloadVideoMarksReferenceImages(t *testing.T) {
	// happyhorse11 不做首帧，参考图缺 role 会被上游当成首帧拒绝。
	p, err := buildPayload(videoModel(), generateInput{Prompt: "让画面动起来"}, []string{"data:image/png;base64,AAA"})
	if err != nil {
		t.Fatalf("buildPayload: %v", err)
	}

	parts := p["content"].([]map[string]any)
	if len(parts) != 2 {
		t.Fatalf("content 应有 text + image 两项，得到 %d 项", len(parts))
	}
	image := parts[1]
	if image["type"] != "image_url" || image["role"] != "reference_image" {
		t.Fatalf("image part = %#v", image)
	}
	if nested, _ := image["image_url"].(map[string]string); nested["url"] != "data:image/png;base64,AAA" {
		t.Fatalf("image_url.url = %#v", image["image_url"])
	}
}

func TestBuildPayloadVideoPassesDeclaredExtras(t *testing.T) {
	p, err := buildPayload(videoModel(), generateInput{
		Prompt: "日落",
		Extra:  map[string]any{"resolution": "720P", "duration": 5, "ratio": "9:16"},
	}, nil)
	if err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	if p["resolution"] != "720P" || p["duration"] != 5 || p["ratio"] != "9:16" {
		t.Fatalf("声明过的参数应原样上送: %v", p)
	}
}

func TestBuildPayloadVideoRejectsUndeclaredExtra(t *testing.T) {
	_, err := buildPayload(videoModel(), generateInput{
		Prompt: "日落",
		Extra:  map[string]any{"seed": 42},
	}, nil)
	if err == nil {
		t.Fatal("未声明的参数应被本地拒绝")
	}
}

func TestBuildPayloadVideoDropsBatchFields(t *testing.T) {
	n := 1
	p, err := buildPayload(videoModel(), generateInput{Prompt: "日落", N: &n}, nil)
	if err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	// 方舟一次一任务，n 上送没有意义；校验仍然保留
	if _, present := p["n"]; present {
		t.Fatalf("视频不该带 n: %v", p)
	}
}

func TestBuildPayloadImageKeepsOpenAIShape(t *testing.T) {
	refs := 10
	m := modelView{
		ID:           "flux-3-image",
		Kind:         "image",
		MaxRefImages: &refs,
		Params:       map[string]ParamSpec{"n": {Min: floatPtr(1), Max: floatPtr(4)}},
	}
	n := 2
	p, err := buildPayload(m, generateInput{Prompt: "一只猫", Size: "1024x1024", N: &n}, []string{"https://example.com/a.png"})
	if err != nil {
		t.Fatalf("buildPayload: %v", err)
	}

	if p["prompt"] != "一只猫" || p["response_format"] != "url" || p["size"] != "1024x1024" || p["n"] != 2 {
		t.Fatalf("图片载荷应保持 OpenAI 形状: %v", p)
	}
	if image, ok := p["image"].([]string); !ok || len(image) != 1 {
		t.Fatalf("image = %#v", p["image"])
	}
	if _, present := p["content"]; present {
		t.Fatalf("图片不该走 content: %v", p)
	}
}
