package mcp

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
)

// 判据的价值全在「会不会判错」上：判对了只是省一次点击，
// 判错了会把一个没约束的模型按错误协议放出去。所以这里重点测
// 容易混淆的命名，而不是把常见模型都列一遍。
func TestSuggestProtocolByName(t *testing.T) {
	cases := []struct {
		name     string
		modelID  string
		wantKind string
		wantConf string
	}{
		{"方舟视频家族", "doubao-seedance-1-0-pro-250528", "video", hintMedium},
		{"可灵", "kling-v3", "video", hintMedium},
		{"海螺", "hailuo-02", "video", hintMedium},
		{"veo", "veo-3.1-generate-preview", "video", hintMedium},
		{"sora", "sora-2-pro", "video", hintMedium},
		{"通用文生视频缩写", "wanx2.1-t2v", "video", hintMedium},
		{"图生视频缩写", "wanx2.1-i2v", "video", hintMedium},
		// 含 image 但产物是视频：这条如果判成图像，声明后图片工具会去调视频上游。
		{"连写的 image2video", "kling-image2video", "video", hintMedium},

		{"豆包图像家族", "doubao-seedream-4-0", "image", hintMedium},
		{"flux", "flux-1.1-pro", "image", hintMedium},
		{"gpt-image", "gpt-image-2", "image", hintMedium},
		{"dall-e", "dall-e-3", "image", hintMedium},
		{"带 imageedit 的万相", "wanx2.1-imageedit", "image", hintMedium},

		// 两条线索都没有：不能猜，必须留空。
		{"纯文本模型", "gpt-4o", "", hintNone},
		{"自定义别名", "my-model-v1", "", hintNone},

		// 文本家族是反证：即使落在视频渠道里也不该被带成视频。
		{"豆包文本模型", "doubao-seed-1-6-250615", "", hintNone},
		{"claude", "claude-sonnet-4", "", hintNone},
		{"gemini", "gemini-2.5-flash", "", hintNone},
		{"推理模型", "o3-mini", "", hintNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SuggestProtocol(tc.modelID, nil, nil)
			if got.Kind != tc.wantKind {
				t.Fatalf("%s: kind = %q，期望 %q（依据 %s）", tc.modelID, got.Kind, tc.wantKind, got.Reason)
			}
			if got.Confidence != tc.wantConf {
				t.Fatalf("%s: confidence = %q，期望 %q", tc.modelID, got.Confidence, tc.wantConf)
			}
			if got.Conflict {
				t.Fatalf("%s: 没有反证时不应打 conflict", tc.modelID)
			}
			if tc.wantKind != "" && got.Protocol != defaultProtocol(tc.wantKind) {
				t.Fatalf("%s: protocol = %q，应与 defaultProtocol 一致", tc.modelID, got.Protocol)
			}
		})
	}
}

// 渠道类型是佐证，不是判据：只有被端点映射硬绑到视频端点的类型（Sora=55）
// 才配给名称背书。
func TestSuggestProtocolChannelEvidence(t *testing.T) {
	h := SuggestProtocol("doubao-seedance-1-0-pro", []int{constant.ChannelTypeSora}, nil)
	if h.Kind != "video" || h.Confidence != hintHigh {
		t.Fatalf("名称 + Sora 视频渠道应为 high，实际 %q/%q", h.Kind, h.Confidence)
	}

	// 只有渠道类型、名称认不出：可以给建议，但必须标 low 让人确认。
	h = SuggestProtocol("custom-abc-v1", []int{constant.ChannelTypeSora}, nil)
	if h.Kind != "video" || h.Confidence != hintLow {
		t.Fatalf("仅渠道佐证应为 low，实际 %q/%q", h.Kind, h.Confidence)
	}

	// 名称说图像、渠道说视频：以模型名为准，降为 medium，
	// 但必须打 conflict 标记并说明，界面会加问号。
	h = SuggestProtocol("gpt-image-2", []int{constant.ChannelTypeSora}, nil)
	if h.Kind != "image" || h.Confidence != hintMedium || !h.Conflict {
		t.Fatalf("名称与渠道矛盾时应按名称给 image/medium 并标 conflict，实际 %q/%q/%v",
			h.Kind, h.Confidence, h.Conflict)
	}
	if h.Reason == "" {
		t.Fatal("矛盾时必须给出理由，否则管理员无从判断")
	}

	// 无类型渠道（0，「渠道已删」）不能当佐证，也不构成矛盾。
	h = SuggestProtocol("doubao-seedance-1-0-pro", []int{0}, nil)
	if h.Confidence != hintMedium || h.Conflict {
		t.Fatalf("未知渠道类型不应升级置信度或触发冲突，实际 %q/%v", h.Confidence, h.Conflict)
	}

	// 文本模型落在视频渠道里：名称反证优先于渠道，否则会把 gpt-4o-mini
	// 声明成视频模型，agent 真去调视频上游。
	h = SuggestProtocol("gpt-4o-mini", []int{constant.ChannelTypeSora}, nil)
	if h.Kind != "" || h.Confidence != hintNone {
		t.Fatalf("文本模型不应被渠道类型带成视频，实际 %q/%q", h.Kind, h.Confidence)
	}
	if h.Reason == "" {
		t.Fatal("给出否定结论时也要说明理由")
	}
}

// 回归：ChannelTypeDoubaoVideo（54）的常量名里有 Video，但它在系统里是
// 异步任务通道 —— 显示名是 "Doubao"，端点映射只返回 [openai]，
// GetAdaptor 没有它的同步适配器。按常量名把它当视频佐证，会让整批挂在
// 方舟渠道上的模型被错判，这正是「用命名代替代码行为」的原始 bug。
func TestSuggestProtocolDoubaoVideoChannelIsNotVideoEvidence(t *testing.T) {
	// 名称是视频：不能再升 high，因为渠道没有视频端点语义。
	h := SuggestProtocol("doubao-seedance-1-0-pro", []int{constant.ChannelTypeDoubaoVideo}, nil)
	if h.Kind != "video" || h.Confidence != hintMedium {
		t.Fatalf("Doubao(54) 不构成视频佐证，应停在 medium，实际 %q/%q", h.Kind, h.Confidence)
	}

	// 名称认不出：渠道不能替它定论。
	h = SuggestProtocol("custom-abc-v1", []int{constant.ChannelTypeDoubaoVideo}, nil)
	if h.Kind != "" || h.Confidence != hintNone {
		t.Fatalf("Doubao(54) 不应单独给出 kind，实际 %q/%q", h.Kind, h.Confidence)
	}

	// 名称说图像：渠道既不背书也不冲突，不能凭空冒出 conflict。
	h = SuggestProtocol("doubao-seedream-5-0-flash", []int{constant.ChannelTypeDoubaoVideo}, nil)
	if h.Kind != "image" || h.Confidence != hintMedium || h.Conflict {
		t.Fatalf("Doubao(54) 与图像名称不矛盾，不应标 conflict，实际 %q/%q/%v",
			h.Kind, h.Confidence, h.Conflict)
	}

	// 端点映射本身也必须是这个结论，否则上面的用例只是碰巧通过。
	// 传空 modelName，避免 IsImageGenerationModel 按名字补端点。
	for _, ep := range common.GetEndpointTypesByChannelType(constant.ChannelTypeDoubaoVideo, "") {
		if ep == constant.EndpointTypeOpenAIVideo || ep == constant.EndpointTypeImageGeneration {
			t.Fatalf("Doubao(54) 的端点映射出现了生成类端点 %q，判据前提已变，需重新评估", ep)
		}
	}
}

// 名称认不出时用模型端点兜底：端点声明比渠道类型更贴近实际服务形状。
func TestSuggestProtocolEndpointFallback(t *testing.T) {
	image := []constant.EndpointType{constant.EndpointTypeOpenAI, constant.EndpointTypeImageGeneration}
	video := []constant.EndpointType{constant.EndpointTypeOpenAI, constant.EndpointTypeOpenAIVideo}

	// 名称没有任何线索，但端点是显式的图片接口。
	h := SuggestProtocol("my-own-alias-7", nil, image)
	if h.Kind != "image" || h.Confidence != hintMedium {
		t.Fatalf("端点兜底应为 image/medium，实际 %q/%q", h.Kind, h.Confidence)
	}

	h = SuggestProtocol("my-own-alias-7", nil, video)
	if h.Kind != "video" || h.Confidence != hintMedium {
		t.Fatalf("视频端点兜底应为 video/medium，实际 %q/%q", h.Kind, h.Confidence)
	}

	// 同时暴露图片与视频端点：猜哪个都可能错，留空。
	h = SuggestProtocol("my-own-alias-7", nil, append(append([]constant.EndpointType{}, image...), video...))
	if h.Kind != "" || h.Confidence != hintNone {
		t.Fatalf("端点互相矛盾时应留空，实际 %q/%q", h.Kind, h.Confidence)
	}

	// 端点矛盾时，渠道类型仍可作最后兜底（low）。
	h = SuggestProtocol("my-own-alias-7", []int{constant.ChannelTypeSora},
		append(append([]constant.EndpointType{}, image...), video...))
	if h.Kind != "video" || h.Confidence != hintLow {
		t.Fatalf("端点矛盾、渠道为视频时应回落到 low，实际 %q/%q", h.Kind, h.Confidence)
	}

	// 名称命中图像、端点说视频：仍按名称，但打 conflict。
	h = SuggestProtocol("doubao-seedream-5-0-flash", nil, video)
	if h.Kind != "image" || h.Confidence != hintMedium || !h.Conflict {
		t.Fatalf("名称与端点矛盾时应按名称并标 conflict，实际 %q/%q/%v",
			h.Kind, h.Confidence, h.Conflict)
	}

	// 名称与端点一致：图像端点可能本来就从模型名推断出来，所以不能升 high。
	h = SuggestProtocol("doubao-seedream-5-0-flash", nil, image)
	if h.Kind != "image" || h.Confidence != hintMedium || h.Conflict {
		t.Fatalf("名称与端点同源，不应升级置信度，实际 %q/%q/%v",
			h.Kind, h.Confidence, h.Conflict)
	}
}
