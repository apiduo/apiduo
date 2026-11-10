package mcp

import (
	"regexp"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
)

// ProtocolHint 是「这个模型该声明成什么」的建议，不是结论。
//
// 为什么要放在服务端：声明落库后直接决定 /mcp 的本地拦截与 payload 构造，
// 判错就改变了对外行为。规则只能有一份 —— 前端各自写一套正则，一旦漂移，
// 页面上标「视频」而实际填了 image，错误要到 agent 调用时才暴露。
//
// 判据强度不同，所以每条建议都带 confidence：
//   - 模型名是强信号，但可能被渠道的 model_mapping 改名，不能当铁证；
//   - 渠道类型是弱信号，同一个类型常常同时服务文本与图像；
//   - 模型端点（声明了 /v1/images/generations 之类）是显式配置，但会包含
//     按模型名推断出来的条目，因此只在名称认不出时兜底，不拿来把 medium 升成 high。
//
// 渠道类型一律不按常量名判断。反例就在本仓：type=54 的常量叫
// ChannelTypeDoubaoVideo，但显示名是 "Doubao"，端点映射只返回 [openai]，
// GetAdaptor 里也没有它的同步适配器 —— 它在系统里是「异步任务通道」，
// 不是「视频端点」。按常量名断言它产视频，等于用命名代替代码行为，
// 与「按模型名猜 kind」是同一个错误，只是换了个位置。
//
// 名称与其它判据矛盾时以模型名为准：名称是唯一直接描述模型的信号，
// 渠道描述的是「能承载什么」。矛盾只降置信度并打 Conflict 标记，不丢结论。
//
// Confidence 为 none 时 Kind/Protocol 留空，由管理员手动补全 ——
// 这比猜一个默认值更安全：猜错会被当成「已声明」静默生效。
type ProtocolHint struct {
	Kind     string `json:"kind,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	// Confidence 为 high / medium 时 Kind/Protocol 可直接带出；low 带出但需确认。
	Confidence string `json:"confidence"`
	// Conflict 表示名称与其它判据（渠道类型、模型端点）互相矛盾。
	// Kind 仍按名称给出，但界面必须加问号提示，不能当成干净结论。
	Conflict bool   `json:"conflict,omitempty"`
	Reason   string `json:"reason"`
}

const (
	hintHigh   = "high"
	hintMedium = "medium"
	hintLow    = "low"
	hintNone   = "none"
)

var (
	// 任务标记比模型名更可靠：t2v / i2v 是各家通用的「文生视频 / 图生视频」缩写，
	// 而 image2video 这种连写形式必须先于图像判据命中，否则会被当成图像模型。
	videoTaskPattern = regexp.MustCompile(`(?i)(t2v|i2v|kf2v|flf2v|image2video|img2video)`)
	// 视频模型家族。命中即认为是视频 —— 这些家族目前都不产出图片。
	videoFamilyPattern = regexp.MustCompile(`(?i)(seedance|seaweed|kling|hailuo|sora|veo|runway|pika|luma|vidu|cogvideo|mochi)`)
	// 图像任务标记。imageedit 不被下面的 image 词边界覆盖（写在一起），所以单列。
	imageTaskPattern = regexp.MustCompile(`(?i)(t2i|img2img|image2image|imageedit|gpt-image|nano-banana)`)
	// 图像模型家族。seedream 与 seedance 同属豆包但产物不同，不能合并。
	imageFamilyPattern = regexp.MustCompile(`(?i)(seedream|flux|dall[-_]?e|imagen|ideogram|recraft|kolors|qwen[^/]*image|midjourney|sdxl|sd3|stable-diffusion|hidream)`)
	// 兜底的「词」级判据，只在家族都没命中时用：video / image 作为独立词出现。
	videoWordPattern = regexp.MustCompile(`(?i)(^|[-_/])video([-_/]|$)`)
	imageWordPattern = regexp.MustCompile(`(?i)(^|[-_/])image([-_/]|$)`)
	// 文本模型家族。这是「反证」而不是「兜底」：这些名字落在视频渠道里也不能
	// 被渠道类型带成视频 —— 声明成视频后，agent 会拿文本模型去调视频上游，
	// 错误只能等到运行时才暴露。
	//
	// 只收一眼能认的家族，且必须让位于上面的图像/视频判据：
	// gpt-image / qwen-image / doubao-seedream / doubao-seedance 都已在前面命中；
	// doubao-seed-1-6 是豆包文本模型，与 seedream/seedance 靠数字区分。
	textFamilyPattern = regexp.MustCompile(`(?i)(^|[-_/])(gpt-[0-9]|chatgpt|claude|gemini-[0-9]|deepseek|llama|mistral|mixtral|command-r|ernie|glm-[0-9]|qwen-(max|plus|turbo|long)|doubao-seed-[0-9]|o[134][-_])`)
)

// nameKind 只按模型名判断产物类型，返回命中的依据用于展示。
//
// 顺序即优先级：任务标记 > 家族 > 词。任务标记最不容易误判，
// 例如 wanx2.1-imageedit 含 wanx 但不属于视频，靠 imageedit 抢先命中。
func nameKind(modelID string) (kind, reason string) {
	checks := []struct {
		kind    string
		label   string
		pattern *regexp.Regexp
	}{
		{"video", "视频任务标记", videoTaskPattern},
		{"video", "视频模型家族", videoFamilyPattern},
		{"image", "图像任务标记", imageTaskPattern},
		{"image", "图像模型家族", imageFamilyPattern},
		{"video", "video 字样", videoWordPattern},
		{"image", "image 字样", imageWordPattern},
	}
	for _, c := range checks {
		if hit := c.pattern.FindString(modelID); hit != "" {
			return c.kind, c.label + "「" + hit + "」"
		}
	}
	return "", ""
}

// channelKindHint 用渠道类型的端点映射做佐证。
//
// 唯一的可信来源是 common.GetEndpointTypesByChannelType：它描述「这个渠道类型
// 在当前代码里被绑到哪些对客端点」。只有被硬绑到单一专用端点时才有语义 ——
// 例如 Sora（55）恒为 [openai-video]，而 Doubao（54）恒为 [openai]。
//
// 这里刻意传空 modelName：把模型名传进去，函数内部的 IsImageGenerationModel
// 会按名字补一个图片端点，那是名称判据的同源复制，再拿来给名称判据背书就是
// 循环论证。
//
// 返回空表示「没有佐证」，不是「不支持」。渠道类型永远不单独定论。
func channelKindHint(channelTypes []int) (kind, reason string) {
	image, video := false, false
	for _, t := range channelTypes {
		if t <= 0 {
			continue
		}
		// 只采信「恰好一个端点」的类型。返回多个端点的类型（自定义渠道的
		// 预设路由可能同时含聊天与图片）说明它能承载多种形状，不构成排他证据。
		eps := common.GetEndpointTypesByChannelType(t, "")
		if len(eps) != 1 {
			continue
		}
		switch eps[0] {
		case constant.EndpointTypeImageGeneration:
			image = true
		case constant.EndpointTypeOpenAIVideo:
			video = true
		}
	}
	switch {
	case image && video:
		return "", ""
	case video:
		return "video", "渠道类型只绑定了视频端点"
	case image:
		return "image", "渠道类型只绑定了图片端点"
	}
	return "", ""
}

// endpointKind 用模型对外暴露的端点做判据，只在名称认不出时兜底。
//
// 端点来自 model.GetModelSupportEndpointTypes：渠道的自定义路由（按 incoming
// path 声明）与模型元数据里的 endpoints 都会进来，比渠道类型更贴近「这个模型
// 实际以什么形状提供服务」。但图像端点也可能由模型名推断补上（IsImageGenerationModel），
// 与名称判据同源，所以它只用于「名称没线索」的情况，不参与提高置信度。
//
// 同时暴露图片与视频端点时视为没有结论：这种情况只能人工选，猜哪个都可能错。
func endpointKind(endpoints []constant.EndpointType) (kind, reason string) {
	image, video := false, false
	for _, e := range endpoints {
		switch e {
		case constant.EndpointTypeImageGeneration:
			image = true
		case constant.EndpointTypeOpenAIVideo:
			video = true
		}
	}
	switch {
	case image && video:
		return "", ""
	case image:
		return "image", "模型端点只暴露图片生成接口"
	case video:
		return "video", "模型端点只暴露视频生成接口"
	}
	return "", ""
}

// SuggestProtocol 合并名称、渠道类型、模型端点三组判据，给出带置信度的建议。
//
// channelTypes 与 endpoints 都可以为空：调用方拿不到就传 nil，
// 判据少一层只会降低置信度，不会给出错误结论。
func SuggestProtocol(modelID string, channelTypes []int, endpoints []constant.EndpointType) ProtocolHint {
	kind, nameReason := nameKind(modelID)
	chKind, chReason := channelKindHint(channelTypes)
	epKind, epReason := endpointKind(endpoints)

	if kind != "" {
		conflict := ""
		switch {
		case chKind != "" && chKind != kind:
			conflict = chReason
		case epKind != "" && epKind != kind:
			conflict = epReason
		}
		if conflict != "" {
			return ProtocolHint{
				Kind:       kind,
				Protocol:   defaultProtocol(kind),
				Confidence: hintMedium,
				Conflict:   true,
				Reason: "模型名命中" + nameReason + "，但" + conflict +
					"；同一渠道可能同时服务两类模型，先按模型名判定，保存前请确认",
			}
		}
		if chKind == kind {
			return ProtocolHint{
				Kind:       kind,
				Protocol:   defaultProtocol(kind),
				Confidence: hintHigh,
				Reason:     "模型名命中" + nameReason + "，" + chReason,
			}
		}
		return ProtocolHint{
			Kind:       kind,
			Protocol:   defaultProtocol(kind),
			Confidence: hintMedium,
			Reason:     "模型名命中" + nameReason + "，无渠道类型佐证",
		}
	}

	// 名称明确是文本模型：这是唯一一个「连渠道类型都不该采信」的反证。
	// 返回 none 而不是猜一个 kind —— MCP 不服务文本，不需要声明。
	if textFamilyPattern.MatchString(modelID) {
		return ProtocolHint{
			Confidence: hintNone,
			Reason:     "模型名属于文本模型家族，图片/视频都不适用，无需声明",
		}
	}

	// 名称没有线索时才看端点：显式声明比「渠道类型像视频」可靠。
	if epKind != "" {
		return ProtocolHint{
			Kind:       epKind,
			Protocol:   defaultProtocol(epKind),
			Confidence: hintMedium,
			Reason:     epReason + "，模型名未识别",
		}
	}
	if chKind != "" {
		return ProtocolHint{
			Kind:       chKind,
			Protocol:   defaultProtocol(chKind),
			Confidence: hintLow,
			Reason:     chReason + "，模型名未识别",
		}
	}
	return ProtocolHint{Confidence: hintNone, Reason: "模型名与端点都没有明确线索，请手动选择"}
}
