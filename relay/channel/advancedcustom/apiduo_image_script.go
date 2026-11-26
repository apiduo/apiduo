package advancedcustom

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"mime/multipart"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/model_setting"
)

/*
 * 上游脚本层（转换点 ③）：canonical → 某个上游的端点 + 载体 + 编码。
 *
 * 一个上游一个脚本。配置面只有主端点，从端点由脚本推导（§3.4.1）：
 * 末段替换，scheme / host / query 全部保留。推不出来就报配置错，不猜路径。
 *
 * 载体是四个维度里唯一可能触发 IO 的（URL 要下载才能内联），所以留在这一层，
 * 不放进纯函数折叠层。上游只收 multipart 文件时，URL 垫图必然要下载；
 * 防 SSRF 靠下载层的强制校验，而不是「关掉下载」。
 *
 * 下载本身（拨号 / 重试 / SSRF / 大小判据）在 apiduo_image_fetch.go；
 * 本文件只做协议转换与 multipart 编码，不碰网络。
 */

type imagePlan struct {
	UpstreamPath string
	ContentType  string
	Body         []byte
	ImageCount   int
}

// planImageRequest 是测试用的便捷入口；生产路径从请求取 ctx，客户端断开时
// 正在下载的垫图能立刻停下。
func planImageRequest(c *imageCanonical, cfg *dto.ImageProtocolConfig) (*imagePlan, error) {
	return planImageRequestWithModel(context.Background(), c, cfg, "")
}

// planImageRequestWithModel 多一个上游模型名：Gemini 的模型与动词都在路径里，
// 且必须是重定向后的名字。其它上游忽略它。
func planImageRequestWithModel(ctx context.Context, c *imageCanonical, cfg *dto.ImageProtocolConfig, upstreamModel string) (*imagePlan, error) {
	if cfg == nil || !cfg.Enabled {
		return nil, errors.New("image protocol is not enabled")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	// 上游一次只产出一张：n 是计费口径，不是扇出指令。网关不会按 n 发多次上游
	// 请求，原样转发只会让上游重复出图（按次重复扣费）或直接 400。这里只改写
	// 发往上游的副本，canonical 保留客户端原值 —— prepareImageProtocol 随后用
	// 它当计费上限（结算按实际交付收紧）。缺席保持缺席：默认值归上游，效果同样是 1 张。
	upstreamView := c.clone()
	if _, exists := upstreamView.fields["n"]; exists {
		upstreamView.fields["n"] = 1
	}
	// 历史值（seedream / gpt-image-2）在这里一并收敛，见 NormalizeImageUpstream。
	switch dto.NormalizeImageUpstream(cfg.Upstream) {
	case dto.ImageUpstreamARK:
		return planARK(upstreamView, cfg)
	case dto.ImageUpstreamOpenAI:
		return planOpenAI(ctx, upstreamView, cfg)
	case dto.ImageUpstreamGemini:
		return planGemini(ctx, upstreamView, upstreamModel)
	default:
		return nil, fmt.Errorf("unknown image upstream %q", cfg.Upstream)
	}
}

// planARK：单端点，带 image 即图生图。image 保持 str | list<str>，
// JSON 编码。mask 在这个形状里没有通道，不转发；size 档位换算属方言，
// 这里不擅自改写（会静默改变语义），留给显式配置。
func planARK(c *imageCanonical, cfg *dto.ImageProtocolConfig) (*imagePlan, error) {
	upstream := c.clone()
	upstream.drop("mask")
	// Gemini 入口的 aspect_ratio / image_size 已在折叠层折成 size；再下传
	// 就是让上游收下两个它不认的键（上游会忽略，客户端却以为尺寸生效）。
	upstream.dropEntryDialect()

	// 裸 base64 不是合法引用：补上 data URI 前缀，否则上游当 URL 解析并报
	// invalid url specified（skill §3.5 实证）。
	if refs := upstream.imageRefs(); len(refs) > 0 {
		normalised := make([]string, len(refs))
		for i, ref := range refs {
			normalised[i] = inlineBareBase64(ref)
		}
		// 保留客户端给的形状：长度 1 的数组仍是数组（str | list<str> 都收）。
		switch upstream.fields["image"].(type) {
		case []string, []any:
			upstream.fields["image"] = normalised
		default:
			upstream.fields["image"] = normalised[0]
		}
	}

	body, err := upstream.bytes()
	if err != nil {
		return nil, err
	}
	return &imagePlan{
		UpstreamPath: cfg.Primary(),
		ContentType:  "application/json",
		Body:         body,
		ImageCount:   len(c.imageRefs()),
	}, nil
}

// planOpenAI：文生图走主端点（不收 image）；图生图走从端点，编码成 multipart 文件。
func planOpenAI(ctx context.Context, c *imageCanonical, cfg *dto.ImageProtocolConfig) (*imagePlan, error) {
	if !c.hasImage() {
		upstream := c.clone()
		upstream.drop("image", "mask")
		upstream.dropEntryDialect()
		body, err := upstream.bytes()
		if err != nil {
			return nil, err
		}
		return &imagePlan{
			UpstreamPath: cfg.Primary(),
			ContentType:  "application/json",
			Body:         body,
		}, nil
	}

	editPath := strings.TrimSpace(cfg.EditPath)
	if editPath == "" {
		var err error
		editPath, err = deriveEditPath(cfg.Primary())
		if err != nil {
			return nil, err
		}
	}

	// 垫图与 mask 共用同一个整批预算：两者串行，各给 90s 会把请求拖到三分钟。
	ctx, cancel := context.WithTimeout(ctx, imageRefBatchTimeout)
	defer cancel()

	images, err := resolveImageRefs(ctx, c.imageRefs())
	if err != nil {
		return nil, err
	}

	var mask *imageBlob
	if raw, exists := c.fields["mask"]; exists {
		if ref, ok := raw.(string); ok {
			blob, err := resolveImageRef(ctx, ref)
			if err != nil {
				return nil, fmt.Errorf("mask reference: %w", err)
			}
			mask = &blob
		}
	}

	// multipart 分支同样不能带 Gemini 方言字段：encodeImageForm 会遍历全部字段，
	// 不先删就会把 aspect_ratio / image_size 写进表单。
	form := c.clone()
	form.dropEntryDialect()
	body, contentType, err := encodeImageForm(form, images, mask)
	if err != nil {
		return nil, err
	}
	return &imagePlan{
		UpstreamPath: editPath,
		ContentType:  contentType,
		Body:         body,
		ImageCount:   len(images),
	}, nil
}

// planGemini：单端点，模型与动词都在路径里。路径不读主端点——
// 主端点前缀是 /v1，Gemini 的版本前缀是 /v1beta，从前者推导只能靠猜。
// 版本取 model_setting，与 Gemini 原生渠道同一份配置。
func planGemini(ctx context.Context, c *imageCanonical, upstreamModel string) (*imagePlan, error) {
	model := strings.TrimSpace(upstreamModel)
	if model == "" {
		model = strings.TrimSpace(c.model())
	}
	if model == "" {
		return nil, errors.New("gemini upstream requires a model name")
	}

	// Gemini 没有独立的 mask 通道：图生图靠 prompt + inlineData，局部重绘只能靠提示词
	// 描述。静默忽略会让客户端以为自己在做 inpainting，实际拿到一张整图重绘——
	// 有输出、有计费、结果不对，比直接拒绝更糟。
	if _, exists := c.fields["mask"]; exists {
		return nil, errors.New("mask is not supported by the gemini upstream: describe the region in the prompt instead")
	}

	prompt, _ := c.fields["prompt"].(string)
	refs := c.imageRefs()
	if strings.TrimSpace(prompt) == "" && len(refs) == 0 {
		return nil, errors.New("prompt is required when no image is provided")
	}

	parts := make([]any, 0, 1+len(refs))
	if strings.TrimSpace(prompt) != "" {
		parts = append(parts, map[string]any{"text": prompt})
	}
	if len(refs) > 0 {
		blobs, err := resolveImageRefs(ctx, refs)
		if err != nil {
			return nil, err
		}
		for _, blob := range blobs {
			parts = append(parts, map[string]any{
				"inlineData": map[string]any{
					"mimeType": blob.MimeType,
					"data":     base64.StdEncoding.EncodeToString(blob.Data),
				},
			})
		}
	}

	ratio, tier, err := geminiImageConfig(c)
	if err != nil {
		return nil, err
	}
	imageConfig := make(map[string]any, 2)
	if ratio != "" {
		imageConfig["aspectRatio"] = ratio
	}
	if tier != "" {
		imageConfig["imageSize"] = tier
	}

	// 只声明 IMAGE：实测 google/gemini-nano-banana-2.1 在 ["TEXT","IMAGE"] 下会
	// 走「草稿图 + 成品图」两遍，一次调用回两个 inlineData；只写 ["IMAGE"] 就只回
	// 一张（详见 docs/apiduo/gemini-image-protocol.md §3.5）。candidateCount 与
	// thinkingBudget 都不影响张数，模态是唯一有效的手段。
	generationConfig := map[string]any{"responseModalities": []string{"IMAGE"}}
	if len(imageConfig) > 0 {
		generationConfig["imageConfig"] = imageConfig
	}

	body, err := json.Marshal(map[string]any{
		"contents":         []any{map[string]any{"role": "user", "parts": parts}},
		"generationConfig": generationConfig,
	})
	if err != nil {
		return nil, err
	}

	version := strings.Trim(model_setting.GetGeminiVersionSetting(model), "/")
	if version == "" {
		version = "v1beta"
	}
	return &imagePlan{
		UpstreamPath: "/" + version + "/models/" + escapeGeminiModelPath(model) + ":generateContent",
		ContentType:  "application/json",
		Body:         body,
		ImageCount:   len(refs),
	}, nil
}

// escapeGeminiModelPath 按段转义，保留 `/`。聚合商的模型 ID 是
// `google/gemini-nano-banana-2.1`，整串 PathEscape 会把斜杠变成 %2F，
// 上游按单段解析后 404。每段单独转义才能既保留路径分隔，又转义段内字符。
func escapeGeminiModelPath(model string) string {
	segments := strings.Split(model, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

// geminiImageConfig 决定发给上游的 imageConfig（比例 + 档位）。
//
// 对外契约只有两维输入：size（OpenAI / 方舟写法，档位或像素）与 aspect_ratio
// （显式比例，可覆盖 size 推出来的那一维）。二者与上游的 (aspectRatio, imageSize)
// 是同构的，所以响应侧会把真实像素写回 size（见 decodeGeminiImageResponse）。
//
// image_size 是 gemini 原生方言，只作为 size 的档位别名保留给 0.0.230 之前的老
// 客户端，不参与优先级：两个都写时 size 说了算。
func geminiImageConfig(c *imageCanonical) (string, string, error) {
	ratio := ""
	if raw, ok := c.fields["aspect_ratio"].(string); ok && strings.TrimSpace(raw) != "" {
		normalised, valid := normaliseAspectRatio(raw)
		if !valid {
			return "", "", fmt.Errorf("aspect_ratio must look like %q, got %q", "16:9", raw)
		}
		ratio = normalised
	}

	fromSizeRatio, fromSizeTier, ratioStated, tierStated, err := geminiFieldsFromSize(c.fields["size"])
	if err != nil {
		return "", "", err
	}
	if ratio == "" && ratioStated {
		ratio = fromSizeRatio
	}
	tier := fromSizeTier

	if !tierStated {
		if raw, exists := c.fields["image_size"]; exists {
			label, _ := raw.(string)
			column, valid := geminiSizeTierColumn(label)
			if !valid {
				return "", "", fmt.Errorf("image_size must be 1K, 2K or 4K, got %q", raw)
			}
			if strings.TrimSpace(label) != "" {
				tier = geminiTierLabel(column)
			}
		}
	}
	return ratio, tier, nil
}

// geminiFieldsFromSize 把 size 反推成 (aspectRatio, imageSize)，两种写法都收：
//
//   - 像素 "2048x2048" / "3840x2160"：比例取最接近的官方档，档位按面积取最近，
//     但降质超过三分之一就升一档 —— 按次计费下，把 3840x2160 降成 2K(2752x1536)
//     是在替客户省钱的同时偷走他点的 4K，比多给像素更糟。
//   - 比例 "1:1" / "16:9"：只表态比例，档位留给上游默认。
//   - 档位 "1K" / "2K" / "4K"：方舟也用它表达清晰度，比例留给上游。
//   - 空 / "auto"：不表态，两项都交给上游。
//
// 两个 stated 分开返回：size 只写了比例时，image_size 别名仍然可以补档位，
// 一个整体布尔会把 "1:1 + image_size=4K" 这种组合误判成「size 全说了」。
//
// 超过 4K 档两倍直接报错：再往上没有档位可给，静默截断等于骗客户。
func geminiFieldsFromSize(raw any) (string, string, bool, bool, error) {
	text, _ := raw.(string)
	text = strings.TrimSpace(text)
	if text == "" || strings.EqualFold(text, "auto") {
		return "", "", false, false, nil
	}
	if column, valid := geminiSizeTierColumn(text); valid {
		return "", geminiTierLabel(column), false, true, nil
	}

	w, h, ok := parsePixelSize(text)
	if !ok {
		// 比例写法：size="1:1" 与 aspect_ratio="1:1" 等价。
		ratio, valid := normaliseAspectRatio(text)
		if !valid {
			return "", "", false, false, fmt.Errorf(
				"size must be a tier like %q, a pixel size like %q, a ratio like %q, or %q, got %q",
				"2K", "2048x2048", "1:1", "auto", text)
		}
		return ratio, "", true, false, nil
	}

	ratio, pixels := nearestGeminiAspectRatio(w, h)
	requestedArea := int64(w) * int64(h)
	column := geminiColumnForArea(requestedArea, pixels)
	if int64(pixels[2][0])*int64(pixels[2][1])*2 < requestedArea {
		return "", "", false, false, fmt.Errorf(
			"size %dx%d exceeds the largest supported image (%dx%d for %s)",
			w, h, pixels[2][0], pixels[2][1], ratio)
	}
	return ratio, geminiTierLabel(column), true, true, nil
}

// nearestGeminiAspectRatio 按对数比距离取最近比例，并返回该比例的三档像素。
// 固定遍历顺序：map 迭代随机，比例打平时同一尺寸会在两次请求里给出不同结果。
func nearestGeminiAspectRatio(w, h int) (string, [3][2]int) {
	target := math.Log(float64(w) / float64(h))
	best := geminiAspectRatioOrder[0]
	bestDistance := math.Inf(1)
	for _, ratio := range geminiAspectRatioOrder {
		pixels := geminiImageSizeTable[ratio]
		distance := math.Abs(target - math.Log(float64(pixels[0][0])/float64(pixels[0][1])))
		if distance < bestDistance {
			bestDistance, best = distance, ratio
		}
	}
	return best, geminiImageSizeTable[best]
}

// parsePixelSize 只认 "2048x2048" 这类显式像素；"2K" 与 "auto" 在调用方分流。
// 上界挡住 9e18 量级：int64(w)*int64(h) 溢出成负数会让面积比较得出完全相反的结论。
func parsePixelSize(text string) (int, int, bool) {
	parts := strings.FieldsFunc(text, func(r rune) bool {
		return r == 'x' || r == 'X' || r == '×' || r == '*'
	})
	if len(parts) != 2 {
		return 0, 0, false
	}
	w, errW := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, errH := strconv.Atoi(strings.TrimSpace(parts[1]))
	if errW != nil || errH != nil || w <= 0 || h <= 0 || w > 100000 || h > 100000 {
		return 0, 0, false
	}
	return w, h, true
}

func geminiTierLabel(column int) string {
	switch column {
	case 0:
		return "1K"
	case 1:
		return "2K"
	default:
		return "4K"
	}
}

// deriveEditPath 末段替换 /generations → /edits，query 原样保留。
// 末段不是 generations 就报错，不猜路径。
func deriveEditPath(primary string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(primary))
	if err != nil {
		return "", fmt.Errorf("image_protocol.primary_path is invalid: %w", err)
	}
	segments := strings.Split(strings.TrimRight(parsed.Path, "/"), "/")
	if len(segments) == 0 || segments[len(segments)-1] != "generations" {
		return "", fmt.Errorf(
			"cannot derive the image edit endpoint from %q: set image_protocol.edit_path",
			primary,
		)
	}
	segments[len(segments)-1] = "edits"
	parsed.Path = strings.Join(segments, "/")
	return parsed.String(), nil
}

// imageReferenceFetchError 标记「请求本身合法，失败发生在网关到图片源的链路上」。
// 上层据此回 502 并保留重试机会；400 只留给客户端能自己修正的错误（URL 非法、
// 指向内网、目标明确 4xx、内容不是图片）。

func inlineBareBase64(ref string) string {
	if strings.HasPrefix(ref, "data:") || strings.HasPrefix(ref, "http://") ||
		strings.HasPrefix(ref, "https://") {
		return ref
	}
	decoded, err := base64.StdEncoding.DecodeString(ref)
	if err != nil || len(decoded) == 0 {
		return ref
	}
	mimeType, err := detectImageMimeType(decoded, "")
	if err != nil {
		return ref
	}
	return "data:" + mimeType + ";base64," + ref
}

// encodeImageForm 把 canonical 的文本字段 + 图片字节写成 multipart。
// 单图用 image，多图用 image[]（OpenAI edits 约定）。
func encodeImageForm(c *imageCanonical, images []imageBlob, mask *imageBlob) ([]byte, string, error) {
	// 8 张几 MB 的图加上 multipart 头，不预分配会让 bytes.Buffer 反复翻倍扩容，
	// 峰值内存近乎翻番。
	payloadBytes := 0
	for _, blob := range images {
		payloadBytes += len(blob.Data)
	}
	if mask != nil {
		payloadBytes += len(mask.Data)
	}
	var buf bytes.Buffer
	buf.Grow(payloadBytes + 4096)
	writer := multipart.NewWriter(&buf)

	for key, value := range c.fields {
		switch key {
		case "image", "mask":
			continue // 图片单独编码成文件 part
			// response_format 决定上游返回 url 还是 b64_json，必须随 form 下传。
			// /v1/images/edits 同样认这个字段（bfl2openai 的 Form(None)），
			// 丢掉会让客户端点名要 url 时仍收到默认的大 b64_json。
		}
		switch typed := value.(type) {
		case string:
			if err := writer.WriteField(key, typed); err != nil {
				return nil, "", err
			}
		case int:
			if err := writer.WriteField(key, fmt.Sprintf("%d", typed)); err != nil {
				return nil, "", err
			}
		default:
			encoded, err := json.Marshal(value)
			if err != nil {
				return nil, "", err
			}
			if err := writer.WriteField(key, string(encoded)); err != nil {
				return nil, "", err
			}
		}
	}

	field := "image"
	if len(images) > 1 {
		field = "image[]"
	}
	for index, blob := range images {
		name := fmt.Sprintf("image_%d%s", index+1, extensionForMime(blob.MimeType))
		if err := writeFilePart(writer, field, name, blob); err != nil {
			return nil, "", err
		}
	}
	if mask != nil {
		if err := writeFilePart(writer, "mask", "mask"+extensionForMime(mask.MimeType), *mask); err != nil {
			return nil, "", err
		}
	}

	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), writer.FormDataContentType(), nil
}

func writeFilePart(writer *multipart.Writer, field, filename string, blob imageBlob) error {
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, field, filename))
	header.Set("Content-Type", blob.MimeType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	_, err = part.Write(blob.Data)
	return err
}

func extensionForMime(mimeType string) string {
	switch strings.ToLower(mimeType) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".bin"
	}
}
