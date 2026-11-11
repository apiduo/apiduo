package advancedcustom

import (
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
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
