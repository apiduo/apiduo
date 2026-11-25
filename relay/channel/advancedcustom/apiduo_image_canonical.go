package advancedcustom

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"mime/multipart"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

/*
 * canonical 层：三个入口折叠成唯一一份 generations JSON。
 *
 * 依据 openai-image-adapter §1/§2/§3.1：入口只决定「怎么读请求、怎么回包」，
 * 中间这段只有一份。①② 必须是纯函数——这里不出现网络、不出现厂商判断。
 *
 * fields 用 map 而不是结构体：vendor extras 一律透传（技巧 6「无损优先」），
 * 结构体收窄 additionalProperties 就是破坏适配。
 */

type imageCanonical struct {
	fields map[string]any
	// geminiEntry 标记请求来自 Gemini 原生入口。aspect_ratio / image_size 是这个
	// 入口的方言，只有 gemini 上游认；发给 ark / openai 形状的上游之前必须删掉
	// （折叠层已经把两者折成 size），否则就是「收下再丢」。
	geminiEntry bool
}

func newImageCanonical() *imageCanonical {
	return &imageCanonical{fields: make(map[string]any, 8)}
}

// canonicalFromJSON 用于 /v1/images/generations 入口。
func canonicalFromJSON(raw []byte) (*imageCanonical, error) {
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, errors.New("image request body must be a JSON object")
	}
	return &imageCanonical{fields: fields}, nil
}

// canonicalFromForm 用于 /v1/images/edits 的 multipart 入口（转换点 ①）。
// 复用已有折叠实现，保证 mime 嗅探 / 空 part / 多图规则只有一份。
func canonicalFromForm(form *multipart.Form) (*imageCanonical, error) {
	body, err := foldEditsMultipart(form)
	if err != nil {
		return nil, err
	}
	canonical, err := canonicalFromJSON(body)
	if err != nil {
		return nil, err
	}
	prompt, _ := canonical.fields["prompt"].(string)
	model, _ := canonical.fields["model"].(string)
	if prompt != "" {
		canonical.fields["prompt"] = prompt
	}
	if model != "" {
		canonical.fields["model"] = model
	}
	return canonical, nil
}

// canonicalFromChat 用于 /v1/chat/completions 入口（转换点 ②）。
//
// 取最后一条 user 轮；更早的 user 轮与全部 assistant 轮丢弃（有意的损失，
// 对话是历史、图像是单轮）；system/developer 文本前拼，它们的图片不参与截断。
func canonicalFromChat(request *dto.GeneralOpenAIRequest) (*imageCanonical, error) {
	if request == nil {
		return nil, errors.New("chat request is empty")
	}

	instructions := make([]string, 0, 2)
	var userText string
	var refs []string
	seenUser := false

	for _, message := range request.Messages {
		text, images, err := readChatContent(message.Content)
		if err != nil {
			return nil, err
		}
		switch message.Role {
		case "system", "developer":
			if strings.TrimSpace(text) != "" {
				instructions = append(instructions, text)
			}
			// 指令里的图片不参与截断：它属于提示词，不是垫图。
		case "user":
			seenUser = true
			userText, refs = text, images
		case "assistant", "tool", "function":
			continue
		default:
			return nil, fmt.Errorf("unsupported chat role %q", message.Role)
		}
	}
	if !seenUser {
		return nil, errors.New("chat request has no user message")
	}

	prompt := userText
	if len(instructions) > 0 {
		prompt = strings.Join(append(instructions, userText), "\n\n")
	}

	canonical := newImageCanonical()
	canonical.fields["model"] = request.Model
	canonical.fields["prompt"] = prompt
	// 刻意不搬 request.ResponseFormat：chat 里它是 {type, json_schema}，语义是
	// 「输出 JSON 还是自由文本」，与 images 的同名字段只是同名，不是载体开关。
	// 搬过来 normalize 只会看到一个不是字符串的值，结论没变但意图变模糊了。
	// 结果是：chat 入口永远给 url，客户端要 b64 就走 /v1/images/generations
	// —— 而且它连传都传不进来，字符串在反序列化阶段就被 dto.ResponseFormat 拒了。
	if len(refs) > 0 {
		if len(refs) == 1 {
			canonical.fields["image"] = refs[0]
		} else {
			canonical.fields["image"] = refs
		}
	}
	// 需求口径：chat 折叠只认 OpenAI 标准字段（messages 里的 text / image），
	// 用途只有一个——判断文生图还是图生图。n / size / temperature / tools
	// 等其它字段一律不往下传，避免把对话参数混进图像请求。
	return canonical, nil
}

// readChatContent 把 string 或 content parts 拆成文本与图片引用。
func readChatContent(content any) (string, []string, error) {
	if content == nil {
		return "", nil, nil
	}
	if text, ok := content.(string); ok {
		return text, nil, nil
	}

	// dto.Message.Content 可能是 []MediaContent、[]any 或 []map[string]any；
	// round-trip 一次可以统一成 map，不依赖上游具体类型。
	raw, err := json.Marshal(content)
	if err != nil {
		return "", nil, fmt.Errorf("chat content is not readable: %w", err)
	}
	var parts []map[string]any
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", nil, fmt.Errorf("chat content must be a string or an array of parts: %w", err)
	}

	var texts []string
	refs := make([]string, 0, 2)
	for _, part := range parts {
		partType, _ := part["type"].(string)
		if partType == "text" {
			if text, ok := part["text"].(string); ok {
				texts = append(texts, text)
			}
			continue
		}
		if partType != "image_url" {
			// 只认 OpenAI chat 的标准图片 part。input_image / image /
			// 裸字符串 url 等非标准写法一律不当图片，避免把别的内容
			// 误判成垫图（误判会让请求走图生图端点）。
			continue
		}
		imageURL, ok := part["image_url"].(map[string]any)
		if !ok {
			continue
		}
		// detail / mime_type 等其它字段忽略，只取 url。
		ref, ok := imageURL["url"].(string)
		if !ok {
			continue
		}
		if ref = strings.TrimSpace(ref); ref != "" {
			refs = append(refs, ref)
		}
	}
	return strings.Join(texts, "\n"), refs, nil
}

// normalize 实施 skill §2.3 的判定顺序：n → response_format → image → mask → prompt。
// 归一化与拒绝的分界线：null/[]、非法 n、非法 response_format → 归一化；
// 空串、无图无 prompt、mask 无 image → 400。
func (c *imageCanonical) normalize() error {
	if c == nil || c.fields == nil {
		return errors.New("image request body must be a JSON object")
	}
	if raw, exists := c.fields["n"]; exists {
		c.fields["n"] = normaliseN(raw)
	}
	// response_format 的口径：**只要不是 b64_json 就优先要 url**。
	// 缺席、非法值、显式的 "url" 一律归一到 "url"；只有客户端明确写
	// b64_json 才给 base64。
	//
	// 这是一处**显式偏差**，登记在此以免出现两种读法：skill 技巧 5 说
	// 「默认值不主动下发」，那是为了让上游的默认归上游。但图像响应的 base64
	// 动辄几 MB，塞进 chat 的 content 或 images 的 data[] 里没有任何好处，
	// 而客户端不写这个字段通常只是没想到 —— 于是这里选择主动要 url。
	// 上游若只认 b64 会忽略这个字段，无害：响应侧照实回包，不做二次转存。
	wantB64 := false
	if raw, exists := c.fields["response_format"]; exists {
		if value, ok := raw.(string); ok && value == "b64_json" {
			wantB64 = true
		}
	}
	if wantB64 {
		c.fields["response_format"] = "b64_json"
	} else {
		c.fields["response_format"] = "url"
	}

	if raw, exists := c.fields["image"]; exists {
		normalised, err := normaliseImageField(raw)
		if err != nil {
			return err
		}
		if normalised == nil {
			delete(c.fields, "image") // 长度 0 / 空值 = 文生图
		} else {
			// 保持客户端给的形状：["a"] 仍是一个数组，压成 "a" 属于静默改写。
			c.fields["image"] = normalised
		}
	}
	if raw, exists := c.fields["mask"]; exists {
		normalised, err := normaliseImageField(raw)
		if err != nil {
			return err
		}
		if normalised == nil {
			delete(c.fields, "mask")
		} else {
			switch typed := normalised.(type) {
			case string:
				c.fields["mask"] = typed
			case []string:
				c.fields["mask"] = typed[0]
			}
			if !c.hasImage() {
				return errors.New("mask requires an image")
			}
		}
	}

	if !c.hasImage() {
		prompt, _ := c.fields["prompt"].(string)
		if strings.TrimSpace(prompt) == "" {
			return errors.New("prompt is required when no image is provided")
		}
	}
	return nil
}

func normaliseN(raw any) int {
	switch value := raw.(type) {
	case int:
		if value >= 1 {
			return value
		}
	case int64:
		if value >= 1 {
			return int(value)
		}
	case float64:
		if value == float64(int(value)) && value >= 1 {
			return int(value)
		}
	case string:
		var parsed int
		if _, err := fmt.Sscanf(strings.TrimSpace(value), "%d", &parsed); err == nil && parsed >= 1 {
			return parsed
		}
	}
	return 1
}

// normaliseImageField 归一并保留形状。判据（需求口径）：
//   - 长度 0 / 全空元素的数组、null、空字符串 → nil（删键 = 文生图）
//   - 长度 1 的数组 → 仍是数组 = 图生图，不压成字符串
//   - 长度 ≥ 2 的数组 → 数组 = 图生图
//
// 与 skill §2.1 的差别：skill 把 image:"" 判 400（认为客户端主动写了空值）；
// 这里按需求口径把它当「没传」，归一化为文生图。见文档 §9.4。
func normaliseImageField(raw any) (any, error) {
	if raw == nil {
		return nil, nil
	}
	switch value := raw.(type) {
	case string:
		ref := strings.TrimSpace(value)
		if ref == "" {
			return nil, nil
		}
		return ref, nil
	case []any:
		refs := make([]string, 0, len(value))
		for _, item := range value {
			ref, ok := item.(string)
			if !ok {
				return nil, errors.New("every image reference must be a string")
			}
			if ref = strings.TrimSpace(ref); ref != "" {
				refs = append(refs, ref)
			}
		}
		if len(refs) == 0 {
			return nil, nil
		}
		return refs, nil
	case []string:
		refs := make([]string, 0, len(value))
		for _, item := range value {
			if ref := strings.TrimSpace(item); ref != "" {
				refs = append(refs, ref)
			}
		}
		if len(refs) == 0 {
			return nil, nil
		}
		return refs, nil
	default:
		return nil, errors.New("image must be a string or an array of strings")
	}
}

// hasImage 只看「归一后有没有引用」，不看键在不在：
// 长度 0 的数组删键后就是文生图，长度 1 也是图生图。
func (c *imageCanonical) hasImage() bool {
	return len(c.imageRefs()) > 0
}

func (c *imageCanonical) imageRefs() []string {
	switch value := c.fields["image"].(type) {
	case string:
		if strings.TrimSpace(value) == "" {
			return nil
		}
		return []string{value}
	case []string:
		return value
	case []any:
		refs := make([]string, 0, len(value))
		for _, item := range value {
			if ref, ok := item.(string); ok && strings.TrimSpace(ref) != "" {
				refs = append(refs, ref)
			}
		}
		return refs
	}
	return nil
}

func (c *imageCanonical) model() string {
	value, _ := c.fields["model"].(string)
	return value
}

func (c *imageCanonical) bytes() ([]byte, error) {
	return json.Marshal(c.fields)
}

func (c *imageCanonical) clone() *imageCanonical {
	copyOf := newImageCanonical()
	copyOf.geminiEntry = c.geminiEntry
	for key, value := range c.fields {
		copyOf.fields[key] = value
	}
	return copyOf
}

func (c *imageCanonical) drop(keys ...string) {
	for _, key := range keys {
		delete(c.fields, key)
	}
}

// dropEntryDialect 删掉「只有入口对应的上游才认」的方言字段。
//
// aspect_ratio / image_size 是 Gemini 的协议字段，折叠层已经把它们合成了
// canonical 的 size。发给 ark / openai 形状的上游时再带上，就是「收下再丢」：
// 不认这两个键的上游会忽略，客户端却会以为尺寸生效了（不变量 2）。
// 只在 gemini 入口生效 —— 其它入口的同名键是 vendor extra，仍按透传处理。
func (c *imageCanonical) dropEntryDialect() {
	if c == nil || !c.geminiEntry {
		return
	}
	delete(c.fields, "aspect_ratio")
	delete(c.fields, "image_size")
}

// canonicalFromGemini 折叠 Gemini 原生入口。
//
// 只取最后一条 user 轮：更早的轮次丢弃，与 chat 入口同一条损失。role 为空
// 按 user 处理——generateContent 允许省略 role，省略时它就是 user。
//
// 载体有两种，按 parts 出现顺序合并进同一个 image 数组：inlineData（base64）
// 与 fileData（公网 URL）。顺序就是语义（客户端 prompt 里「第一张的颜色 +
// 第二张的形状」），混合载体也不许先收完 b64 再补 URL。
//
// URL 要不要落成字节由脚本层按上游决定：ark 形状的上游 image 字段本来就收
// URL（零下载），openai / gemini 形状才下载（SSRF、体积、超时限制都在
// apiduo_image_fetch.go，本层不做网络）。inlineData.data 写成 URL 仍 400 ——
// 那不是 Gemini 的语法，公网引用属于 fileData.fileUri。
func canonicalFromGemini(request *dto.GeminiChatRequest, model string) (*imageCanonical, error) {
	if request == nil {
		return nil, errors.New("gemini request is empty")
	}

	var chosen *dto.GeminiChatContent
	for i := range request.Contents {
		role := strings.TrimSpace(request.Contents[i].Role)
		if role == "" || strings.EqualFold(role, "user") {
			chosen = &request.Contents[i]
		}
	}
	if chosen == nil {
		return nil, errors.New("gemini request has no user content")
	}

	texts := make([]string, 0, len(chosen.Parts))
	refs := make([]string, 0, len(chosen.Parts))
	for _, part := range chosen.Parts {
		if text := strings.TrimSpace(part.Text); text != "" {
			texts = append(texts, text)
		}
		// 一个 part 只有一个载体：两种都给说明客户端拼错了报文，
		// 挑一个丢一个等于替它决定参考图是哪张。
		if part.InlineData != nil && part.FileData != nil {
			return nil, errors.New("a part carries either parts[].inlineData or parts[].fileData, not both")
		}
		if part.FileData != nil {
			// 非图片引用（音频 / PDF）不参与图像语义：与 text part 里的无关内容
			// 同等对待，不算「字段被丢」。
			if mimeType := strings.TrimSpace(part.FileData.MimeType); mimeType != "" &&
				!strings.HasPrefix(strings.ToLower(mimeType), "image/") {
				continue
			}
			ref, err := geminiFileReference(part.FileData)
			if err != nil {
				return nil, err
			}
			// 保序：与 inlineData 混用时也按出现顺序进同一个数组。
			refs = append(refs, ref)
			continue
		}
		if part.InlineData == nil {
			continue
		}
		mimeType := strings.TrimSpace(part.InlineData.MimeType)
		data := strings.TrimSpace(part.InlineData.Data)
		if mimeType == "" {
			return nil, errors.New("parts[].inlineData.mimeType is required")
		}
		// 非图片 inlineData（音频 / PDF）不参与图像语义：与 text part 里的无关内容
		// 同等对待，不算「字段被丢」。
		if !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
			continue
		}
		if data == "" {
			return nil, errors.New("parts[].inlineData.data is empty")
		}
		if inlineDataIsURL(data) {
			return nil, errors.New("parts[].inlineData.data must be base64, not a URL; put public image URLs in parts[].fileData.fileUri")
		}
		refs = append(refs, "data:"+mimeType+";base64,"+data)
	}

	canonical := newImageCanonical()
	canonical.geminiEntry = true
	canonical.fields["model"] = strings.TrimSpace(model)
	canonical.fields["prompt"] = strings.Join(texts, "\n")
	switch len(refs) {
	case 0:
	case 1:
		canonical.fields["image"] = refs[0]
	default:
		// 多垫图保序：顺序就是语义，不许重排，也不许压成标量。
		canonical.fields["image"] = refs
	}

	if len(request.GenerationConfig.ImageConfig) > 0 {
		var imageConfig struct {
			AspectRatio string `json:"aspectRatio"`
			ImageSize   string `json:"imageSize"`
		}
		if err := json.Unmarshal(request.GenerationConfig.ImageConfig, &imageConfig); err != nil {
			return nil, errors.New("generationConfig.imageConfig must be an object with aspectRatio / imageSize")
		}
		ratio := strings.TrimSpace(imageConfig.AspectRatio)
		tier := strings.TrimSpace(imageConfig.ImageSize)
		if ratio != "" || tier != "" {
			size, err := geminiCanonicalSize(ratio, tier)
			if err != nil {
				return nil, err
			}
			// size 是 canonical 的规范尺寸字段：ark / openai 形状的上游只认它。
			// 不折这一步，Gemini 请求经非 gemini 上游时比例和档位会一起丢（实测恒为上游原生方形）。
			canonical.fields["size"] = size
			// 原字段保留给 planGemini：从像素反推比例是有损的（表外比例会被算回去）。
			if ratio != "" {
				canonical.fields["aspect_ratio"] = ratio
			}
			if tier != "" {
				canonical.fields["image_size"] = strings.ToUpper(tier)
			}
		}
	}
	return canonical, nil
}

// geminiImageSizeTable 是 Gemini imageConfig 的官方像素表：[比例][1K/2K/4K]。
//
// 为什么不按面积现算：客户端从 Gemini 迁过来，期望的就是 Google 表里的宽高
// （16:9 的 1K 是 1376×768，不是按 1024² 面积算出的 1368×768）。与官方表一致，
// 同一份 imageConfig 在这条链路和 Google 直连上拿到同一组像素。
// 表外比例仍按面积算 —— 不因为网关枚举落后就 400（Gemini 的比例集在扩）。
var geminiImageSizeTable = map[string][3][2]int{
	"1:1":  {{1024, 1024}, {2048, 2048}, {4096, 4096}},
	"2:3":  {{848, 1264}, {1696, 2528}, {3392, 5056}},
	"3:2":  {{1264, 848}, {2528, 1696}, {5056, 3392}},
	"3:4":  {{896, 1200}, {1792, 2400}, {3584, 4800}},
	"4:3":  {{1200, 896}, {2400, 1792}, {4800, 3584}},
	"4:5":  {{928, 1152}, {1856, 2304}, {3712, 4608}},
	"5:4":  {{1152, 928}, {2304, 1856}, {4608, 3712}},
	"9:16": {{768, 1376}, {1536, 2752}, {3072, 5504}},
	"16:9": {{1376, 768}, {2752, 1536}, {5504, 3072}},
	"21:9": {{1584, 672}, {3168, 1344}, {6336, 2688}},
	"1:4":  {{512, 2048}, {1024, 4096}, {2048, 8192}},
	"4:1":  {{2048, 512}, {4096, 1024}, {8192, 2048}},
	"1:8":  {{384, 3072}, {768, 6144}, {1536, 12288}},
	"8:1":  {{3072, 384}, {6144, 768}, {12288, 1536}},
}

// geminiCanonicalSize 把 (aspectRatio, imageSize) 折成 canonical 的 size。
//
// 口径与 Google 的 imageConfig 语义对齐：
//
//	两者都有 → 官方表里的精确 WxH。ark 形状只有一个像素目标字段，档位与比例无法同时表达，
//	           要同时兑现比例和像素，唯一形态就是 WxH。
//	只有比例 → 该比例的 1K 档（imageSize 的官方默认值就是 1K）。
//	只有档位 → 该档位的 1:1（aspectRatio 的官方默认值就是 1:1）。
//
// 非法值直接 400：这个值上游也认不出，静默丢掉等于让客户端以为比例生效了。
func geminiCanonicalSize(ratio, tier string) (string, error) {
	column, ok := geminiSizeTierColumn(tier)
	if !ok {
		return "", fmt.Errorf("imageConfig.imageSize must be 1K, 2K or 4K, got %q", tier)
	}
	normalised, ok := normaliseAspectRatio(ratio)
	if !ok {
		return "", fmt.Errorf("imageConfig.aspectRatio must look like %q, got %q", "16:9", ratio)
	}
	if pixels, exists := geminiImageSizeTable[normalised]; exists {
		return fmt.Sprintf("%dx%d", pixels[column][0], pixels[column][1]), nil
	}
	w, h := geminiTierPixels(normalised, column)
	return fmt.Sprintf("%dx%d", w, h), nil
}

// geminiSizeTierColumn 把档位映射到官方表的列；空值按 1K（官方默认）。
func geminiSizeTierColumn(tier string) (int, bool) {
	switch strings.ToUpper(strings.TrimSpace(tier)) {
	case "", "1K":
		return 0, true
	case "2K":
		return 1, true
	case "4K":
		return 2, true
	}
	return 0, false
}

// normaliseAspectRatio 接受 "16:9" / "16 / 9" 这类写法，输出 "16:9"。
// 空值按 1:1（官方默认）。上界 64 挡住 "9999:1" 这种会让面积算法溢出的输入。
func normaliseAspectRatio(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "1:1", true
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ':' || r == '/' || r == '×'
	})
	if len(parts) != 2 {
		return "", false
	}
	a, errA := strconv.Atoi(strings.TrimSpace(parts[0]))
	b, errB := strconv.Atoi(strings.TrimSpace(parts[1]))
	if errA != nil || errB != nil || a <= 0 || b <= 0 || a > 64 || b > 64 {
		return "", false
	}
	return fmt.Sprintf("%d:%d", a, b), true
}

// geminiTierPixels 给表外比例算像素：档位决定面积，比例决定宽高。
// 1K 档取 1024²，与官方表的基准一致；宽高取 8 的倍数，
// 与主流上游的对齐要求一致。
func geminiTierPixels(ratio string, column int) (int, int) {
	base := 1024 << column // 1K=1024, 2K=2048, 4K=4096
	parts := strings.SplitN(ratio, ":", 2)
	a, _ := strconv.Atoi(parts[0])
	b, _ := strconv.Atoi(parts[1])
	area := float64(base) * float64(base)
	h := int(math.Round(math.Sqrt(area * float64(b) / float64(a))))
	w := int(math.Round(float64(h) * float64(a) / float64(b)))
	return roundTo8(w), roundTo8(h)
}

func roundTo8(value int) int {
	if value < 8 {
		return 8
	}
	return (value + 4) / 8 * 8
}

// inlineDataIsURL 识别「本该是 base64 却给了链接」的载荷。
//
// 只做前缀判断，不尝试解码整段 base64：几 MB 的载荷每个请求都解一遍纯属浪费，
// 而真正的畸形 b64 上游会拒绝，错误归因落在上游侧。
func inlineDataIsURL(data string) bool {
	lower := strings.ToLower(strings.TrimSpace(data))
	for _, prefix := range []string{"http://", "https://", "data:", "gs://"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// geminiFileReference 把 parts[].fileData 折成 canonical 的 URL 引用。
//
// 官方把「用网址传图」与 inlineData、File API 并列为三种输入方式，所以这里是
// 收下而不是拒绝（旧代码 400 属于方向错）。只收公网 http(s)：gs:// 与 file://
// 网关和上游都取不到，收下也只会在下游变成一次看不懂的失败。
//
// 字面量内网地址在这里就挡住，网络留给下载层：ark 形状的上游会原样转发这个
// 链接并自己抓取，不挡等于让客户端借上游的网段做 SSRF。域名不在这里解析 ——
// 折叠层是纯函数（不碰网络），解析后的校验在 validateImageRefURL。
func geminiFileReference(fileData *dto.GeminiFileData) (string, error) {
	raw := strings.TrimSpace(fileData.FileUri)
	if raw == "" {
		return "", errors.New("parts[].fileData.fileUri is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parts[].fileData.fileUri is not a valid URL: %q", truncateForLog(raw))
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return "", fmt.Errorf("parts[].fileData.fileUri must be a public http(s) URL, got prefix %q (gs:// and file:// cannot be fetched by the gateway or the upstream)", parsed.Scheme+"://")
	}
	host := strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return "", errors.New("parts[].fileData.fileUri has no host")
	}
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return "", fmt.Errorf("parts[].fileData.fileUri host %q is not allowed", host)
	}
	if ip := net.ParseIP(host); ip != nil {
		if err := imageRefAllowAddress(ip); err != nil {
			return "", fmt.Errorf("parts[].fileData.fileUri: %w", err)
		}
	}
	return raw, nil
}

// canonicalFromResponses 直接折成 canonical，不经过 chat。
//
// 绕一层 chat 会把 input_image 的 data URI 全量序列化一遍（图片动辄几 MB），
// 也会把与图片无关的工具调用、previous_response_id 语义带进来。这里只做
// 图片入口真正需要的一件事：取最后一条 user 输入，instructions 前拼。
func canonicalFromResponses(request *dto.OpenAIResponsesRequest, model string) (*imageCanonical, error) {
	if request == nil {
		return nil, errors.New("responses request is empty")
	}

	instructions := ""
	if len(request.Instructions) > 0 {
		var text string
		if err := json.Unmarshal(request.Instructions, &text); err == nil {
			instructions = strings.TrimSpace(text)
		}
	}

	prompt := ""
	var refs []string
	seenUser := false

	if len(request.Input) > 0 {
		var single string
		if err := json.Unmarshal(request.Input, &single); err == nil {
			prompt = strings.TrimSpace(single)
			seenUser = prompt != ""
		} else {
			var items []dto.Input
			if err := json.Unmarshal(request.Input, &items); err != nil {
				return nil, errors.New("responses input must be a string or an array")
			}
			for _, item := range items {
				role := strings.ToLower(strings.TrimSpace(item.Role))
				if role != "" && role != "user" {
					continue
				}
				if role == "" && item.Type != "" && item.Type != "message" {
					continue
				}
				text, images, err := readResponsesContent(item.Content)
				if err != nil {
					return nil, err
				}
				if text == "" && len(images) == 0 {
					continue
				}
				prompt, refs = text, images
				seenUser = true
			}
		}
	}
	if !seenUser {
		return nil, errors.New("responses request has no user input")
	}
	if instructions != "" {
		if prompt != "" {
			prompt = instructions + "\n\n" + prompt
		} else {
			prompt = instructions
		}
	}

	canonical := newImageCanonical()
	canonical.fields["model"] = strings.TrimSpace(model)
	canonical.fields["prompt"] = prompt
	switch len(refs) {
	case 0:
	case 1:
		canonical.fields["image"] = refs[0]
	default:
		canonical.fields["image"] = refs
	}
	return canonical, nil
}

// readResponsesContent 读一条 responses input 的 content：input_text 拼成
// 提示词，input_image 收成引用。只认标准 part，其余（input_file / audio）
// 忽略——它们不在图片入口的语义里。
func readResponsesContent(raw json.RawMessage) (string, []string, error) {
	if len(raw) == 0 {
		return "", nil, nil
	}
	var parts []map[string]any
	if err := json.Unmarshal(raw, &parts); err != nil {
		var text string
		if textErr := json.Unmarshal(raw, &text); textErr == nil {
			return text, nil, nil
		}
		return "", nil, errors.New("responses content must be a string or an array")
	}

	texts := make([]string, 0, len(parts))
	refs := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part["type"] {
		case "input_text", "output_text", "text":
			if text, ok := part["text"].(string); ok && strings.TrimSpace(text) != "" {
				texts = append(texts, text)
			}
		case "input_image":
			switch value := part["image_url"].(type) {
			case string:
				if ref := strings.TrimSpace(value); ref != "" {
					refs = append(refs, ref)
				}
			case map[string]any:
				if url, ok := value["url"].(string); ok && strings.TrimSpace(url) != "" {
					refs = append(refs, url)
				}
			}
		}
	}
	return strings.Join(texts, "\n"), refs, nil
}
