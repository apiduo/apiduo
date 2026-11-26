package advancedcustom

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/pkg/apiduo/trace"
	"github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

/*
 * ⑤ 上游 → canonical、⑥ canonical → 各入口的回包，以及 chat 入口的按张计费。
 *
 * 为什么这两个必须在同一个文件：skill 技巧 7「去程与回程由同一实体负责」，
 * 拆开写就没人对「请求与响应对称」负责。
 */

type imageResponseItem struct {
	URL     string
	B64JSON string
	Extras  map[string]any
}

type imageResponse struct {
	Created int64
	Data    []imageResponseItem
	Extras  map[string]any
	// Usage 只有 Gemini 上游提供。它既是账本口径（billedUsage），
	// 也是按模态计价（img_o）的唯一来源。
	Usage *imageUsageSummary
	// fallbackUsage 是本次请求的本地下界估算（relay 层算好挂在 RelayInfo 上）。
	// 上游不给 usage 时用它，保证回包 usage 与账本同源。
	fallbackUsage *dto.Usage
}

// withEstimatedUsage attaches the request's local estimate. It returns the
// response unchanged when the relay layer produced none, which happens for
// models without a published token table.
func (r *imageResponse) withEstimatedUsage(info *common.RelayInfo) *imageResponse {
	if r != nil && info != nil {
		r.fallbackUsage = info.EstimatedImageUsage
	}
	return r
}

// imageUsageSummary 是 Gemini usageMetadata 里允许出网关的字段。
// trafficType（ON_DEMAND / PROVISIONED）是上游的调度信息，不进回包。
type imageUsageSummary struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	ThoughtsTokens   int
	// ImageOutputTokens 是 candidatesTokensDetails 里 IMAGE 模态的 token 数。
	// 表达式计费的 img_o 靠它：c 会把这块扣掉，让输出图片和输出文字分开定价。
	ImageOutputTokens int
}

// decodeImageResponse 解析上游 {created, data:[{url|b64_json, …}]}。
// 附加字段只留两类：envelope 的 usage（计费依据）与 item 的 revised_prompt。
// 上游的 model / system_fingerprint / service_tier 进入回包就是身份泄漏。
func decodeImageResponse(raw []byte) (*imageResponse, error) {
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope == nil {
		return nil, errors.New("upstream image response is not a JSON object")
	}

	result := &imageResponse{Extras: make(map[string]any)}
	if created, ok := envelope["created"].(float64); ok {
		result.Created = int64(created)
	}
	if result.Created == 0 {
		result.Created = time.Now().Unix()
	}
	for key, value := range envelope {
		// 只保留通用用量字段。上游的 model / system_fingerprint / service_tier
		// 一旦进入回包就是身份。
		if key == "usage" {
			result.Extras[key] = value
		}
	}

	items, exists := envelope["data"]
	if !exists {
		return nil, errors.New("upstream image response has no data array")
	}
	list, ok := items.([]any)
	if !ok {
		return nil, errors.New("upstream image response data must be an array")
	}
	for _, entry := range list {
		object, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		item := imageResponseItem{Extras: make(map[string]any)}
		item.URL, _ = object["url"].(string)
		item.B64JSON, _ = object["b64_json"].(string)
		if revised, ok := object["revised_prompt"]; ok {
			item.Extras["revised_prompt"] = revised
		}
		// 上游的 size / model / size_applied 一律不回显（§5.1）；但交付字节里的
		// 宽高要回填 —— 客户端传 1024x1024、上游实际给 1295x1214（duck 实测），
		// 不回就是让客户端以为尺寸精确生效了。URL 引用没有字节，不填。
		if width, height, ok := imagePixelSize(item.B64JSON); ok {
			item.Extras["size"] = fmt.Sprintf("%dx%d", width, height)
		}
		result.Data = append(result.Data, item)
	}
	return result, nil
}

// looksLikeRemoteReference 判定一个「本该是 base64」的值是否其实是地址。
//
// 唯一已知的来源是通用响应改写器（service.RewriteResponseArtifacts）：它在
// DoResponse 之前把上游的 base64 换成了存储链接。图片协议渠道已不再让那个
// 改写器介入（RelayInfo.ArtifactsHandledByAdaptor），这里保留判定是为了不让
// 遗留配置或将来新增的改写点把一次成功的生成打成 502。
func looksLikeRemoteReference(value string) bool {
	lowered := strings.ToLower(value)
	return strings.HasPrefix(lowered, "http://") || strings.HasPrefix(lowered, "https://")
}

// referenceForItem 合成客户端可消费的图片引用。
// url 与 b64_json 互斥；后者转成 data URI，裸 b64 会被当成链接而 400。
func (i imageResponseItem) referenceForItem() (string, error) {
	if i.B64JSON != "" {
		// 字段装错位（地址进了 base64 字段）时当成链接用：宁可少一层封装，
		// 也不让整条 chat 响应因为一个错位的值失败。
		if looksLikeRemoteReference(i.B64JSON) {
			return i.B64JSON, nil
		}
		decoded, err := base64.StdEncoding.DecodeString(i.B64JSON)
		if err != nil {
			return "", fmt.Errorf("invalid b64_json: %w", err)
		}
		mimeType, err := detectImageMimeType(decoded, "")
		if err != nil {
			mimeType = "image/png"
		}
		return "data:" + mimeType + ";base64," + i.B64JSON, nil
	}
	if strings.TrimSpace(i.URL) != "" {
		return i.URL, nil
	}
	return "", errors.New("image item has neither url nor b64_json")
}

// finishImageNativeResponse 接管 images / edits 的响应，只为把 base64 换成
// 存储地址。任何一步失败都退回原样写回 —— 存储是增值，不是前置条件。
//
// 调用方已经用 Content-Length 卡过上限，所以这里的读取不会超。
func finishImageNativeResponse(
	c *gin.Context,
	resp *http.Response,
	info *common.RelayInfo,
	a *Adaptor,
) (any, *types.NewAPIError) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, imageLargeResponseMaxBytes+1))
	_ = resp.Body.Close()
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}
	if len(raw) > imageLargeResponseMaxBytes {
		return nil, types.NewOpenAIError(errors.New("upstream image response is too large"), types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}
	upstream := ""
	if info != nil {
		upstream = info.ChannelOtherSettings.ImageProtocol.Upstream
	}
	decoded, err := decodeImageForUpstream(raw, upstream, time.Now().Unix())
	if err != nil {
		var blocked *geminiImageBlockedError
		if errors.As(err, &blocked) {
			return nil, types.NewOpenAIError(err, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}
	decoded = deliverableImages(decoded, upstream)
	decoded = decoded.withEstimatedUsage(info)
	alignImageCarriers(c.Request.Context(), info.GetChannelID(), decoded, a.imageWantURL)
	usage := decoded.billedUsageWith(info)
	out, err := encodeImageResponse(decoded, usage)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}
	writeImageJSON(c, out)
	return usage, nil
}

// usageFromUpstream 取上游声明的 token 用量。两种形状：
//   - Gemini：usageMetadata（解码时已折进 imageResponse.Usage）
//   - OpenAI 形状：envelope 的 usage（ark 回 output_tokens/total_tokens；
//     多数 b64 类接口不回，此时 ok=false）
//
// 拿不到时由 billedUsage 回退到本地下界估算（relay 层挂在 RelayInfo 上），
// 没有估算才落 {1,0,1}。不要在这里按图片张数猜 token：这张表属于计费口径，
// 不属于解码器。
func (r *imageResponse) usageFromUpstream() (*dto.Usage, bool) {
	if r == nil {
		return nil, false
	}
	if u := r.Usage; u != nil && u.TotalTokens > 0 {
		prompt := max(u.PromptTokens, 0)
		// 上游的 total 含 thoughts（Gemini 按输出计费思考），所以 completion 取
		// total-prompt，而不是 candidatesTokenCount：后者不含 thoughts，用它
		// 会让每个请求少收一截。
		completion := max(u.TotalTokens-prompt, 0)
		usage := &dto.Usage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: u.TotalTokens}
		// 输出侧按模态拆开。引擎只在表达式引用 img_o 时才从 c 里扣，
		// 所以老配置（ModelRatio/CompletionRatio）行为不变。
		usage.CompletionTokenDetails.ImageTokens = u.ImageOutputTokens
		return usage, true
	}
	raw, ok := r.Extras["usage"].(map[string]any)
	if !ok {
		return nil, false
	}
	prompt := intField(raw, "prompt_tokens", "input_tokens")
	completion := intField(raw, "completion_tokens", "output_tokens")
	total := intField(raw, "total_tokens")
	if total == 0 {
		total = prompt + completion
	}
	if total == 0 {
		return nil, false
	}
	usage := &dto.Usage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total}
	if details, ok := raw["completion_tokens_details"].(map[string]any); ok {
		usage.CompletionTokenDetails.ImageTokens = intField(details, "image_tokens")
	}
	return usage, true
}

// billedUsage 是账本口径：上游给了真实 token 就按 token；没给就用本地下界
// 估算；两者都没有才退回 {1,0,1}。{1,0,1} 是按次计价的口径（UsePrice 时
// token 不参与价格），也是 hasBillableUsage 的下限。按 token 计价的模型
// 必须拿到真实用量或估算，否则结算 0。
func (r *imageResponse) billedUsage() *dto.Usage {
	return r.billedUsageWith(nil)
}

// billedUsageWith also marks the request when the local estimate is what the
// ledger will use, so admin logs can separate estimates from reported usage.
func (r *imageResponse) billedUsageWith(info *common.RelayInfo) *dto.Usage {
	if usage, ok := r.usageFromUpstream(); ok {
		return usage
	}
	if r.fallbackUsage != nil {
		if info == nil {
			return r.fallbackUsage
		}
		info.ImageUsageEstimated = true
		// Upstreams fold n: duck answers n=2 with one image. Charging the
		// requested count would over-estimate, so scale to what was delivered.
		requested, delivered := info.EstimatedImageCount, len(r.Data)
		if requested <= delivered || delivered == 0 {
			return r.fallbackUsage
		}
		scaled := *r.fallbackUsage
		scaled.CompletionTokens = r.fallbackUsage.CompletionTokens * delivered / requested
		scaled.CompletionTokenDetails = r.fallbackUsage.CompletionTokenDetails
		scaled.CompletionTokenDetails.ImageTokens = r.fallbackUsage.CompletionTokenDetails.ImageTokens * delivered / requested
		scaled.TotalTokens = scaled.PromptTokens + scaled.CompletionTokens
		return &scaled
	}
	return &dto.Usage{PromptTokens: 1, CompletionTokens: 0, TotalTokens: 1}
}

// displayUsage 是回包里给客户端看的三个数，与账本同源。
func (r *imageResponse) displayUsage() (prompt, completion, total int) {
	usage := r.billedUsage()
	return usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens
}

// intField 读 map 里的整数字段。JSON 解码后数字是 float64，个别上游给字符串。
func intField(raw map[string]any, keys ...string) int {
	for _, key := range keys {
		switch value := raw[key].(type) {
		case float64:
			return int(value)
		case int:
			return value
		case string:
			var parsed int
			if _, err := fmt.Sscanf(value, "%d", &parsed); err == nil {
				return parsed
			}
		}
	}
	return 0
}

// deliverableImages 只保留「本次交付」的图。
//
// Gemini 的图片模型会先出一张草稿再出成品——实测
// Gemini 类上游一次调用可能回多帧 inlineData（前面的是草稿、最后是成品），
// “refining the concept”，最后那张才是成品），加 candidateCount=1 也拦不住。
// OpenAI 形状的入口里 n 就是交付张数：多给会与 n、与账本（按 n 预扣）对不上，
// 所以只取最后一张。其它上游原样返回——ark / openai 的 n 是真扇出，不能裁。
//
// Gemini 原生入口不经过这里：那个入口按 Gemini 语义回包，两个 part 都保留。
func deliverableImages(response *imageResponse, upstream string) *imageResponse {
	if response == nil || len(response.Data) <= 1 {
		return response
	}
	if dto.NormalizeImageUpstream(upstream) != dto.ImageUpstreamGemini {
		return response
	}
	response.Data = response.Data[len(response.Data)-1:]
	return response
}

// decodeImageForUpstream 按上游形状选解码器：ark / openai 回 data[]，
// gemini 回 candidates[].inlineData。这是 5×3 矩阵里唯一按上游分叉的地方，
// 放错位置的后果是 images 入口 × gemini 上游全部 502。
func decodeImageForUpstream(raw []byte, upstream string, fallbackCreated int64) (*imageResponse, error) {
	if dto.NormalizeImageUpstream(upstream) == dto.ImageUpstreamGemini {
		return decodeGeminiImageResponse(raw, fallbackCreated)
	}
	return decodeImageResponse(raw)
}

// encodeImageResponse 把 canonical 还原成 images 形状。Extras 原样带回，
// usage 是计费依据，丢一次就等于丢一次收入。
func encodeImageResponse(response *imageResponse, usage *dto.Usage) ([]byte, error) {
	envelope := make(map[string]any, len(response.Extras)+2)
	maps.Copy(envelope, response.Extras)
	envelope["created"] = response.Created
	items := make([]any, 0, len(response.Data))
	for _, item := range response.Data {
		entry := make(map[string]any, len(item.Extras)+2)
		maps.Copy(entry, item.Extras)
		if item.B64JSON != "" {
			entry["b64_json"] = item.B64JSON
		}
		if item.URL != "" {
			entry["url"] = item.URL
		}
		items = append(items, entry)
	}
	envelope["data"] = items
	envelope["usage"] = imageUsageEnvelope(envelope["usage"], usage)
	return json.Marshal(envelope)
}

// imageUsageEnvelope 返回 OpenAI Images 形状的 usage。上游自己给了 token 就
// 原样保留它的对象；只报了张数（ark 的 {generated_images: 1}）或只给了
// Gemini 的 usageMetadata 时，用本次结算的 token 补齐，让回包与账本说同一组
// 数字。
func imageUsageEnvelope(existing any, usage *dto.Usage) map[string]any {
	reported, _ := existing.(map[string]any)
	merged := make(map[string]any, len(reported)+6)
	maps.Copy(merged, reported)
	reportedTokens := false
	for _, key := range []string{"input_tokens", "output_tokens", "total_tokens", "prompt_tokens", "completion_tokens"} {
		if _, ok := reported[key]; ok {
			reportedTokens = true
			break
		}
	}
	if usage == nil || reportedTokens {
		return merged
	}
	// The detail split must add up to the totals; an upstream that reported one
	// modality only (Gemini gives no input-image breakdown) leaves the rest on
	// the text side, which is also where thoughts are billed.
	inputImage := usage.PromptTokensDetails.ImageTokens
	inputText := usage.PromptTokens - inputImage
	outputImage := usage.CompletionTokenDetails.ImageTokens
	outputText := usage.CompletionTokens - outputImage
	merged["input_tokens"] = usage.PromptTokens
	merged["output_tokens"] = usage.CompletionTokens
	merged["total_tokens"] = usage.TotalTokens
	merged["input_tokens_details"] = map[string]any{
		"text_tokens":  inputText,
		"image_tokens": inputImage,
	}
	merged["output_tokens_details"] = map[string]any{
		"text_tokens":  outputText,
		"image_tokens": outputImage,
	}
	return merged
}

// alignImageCarriers 让 data[] 的载体与客户端要的一致。
//
// 两个方向都做，不只是「把 base64 换成链接」：
//
//	客户端要 url  而上游给 b64  ⇒ 落存储换链接（需要配置；失败保留 b64）
//	客户端要 url  而上游给 url  ⇒ 下载后落存储换自己的链接（上游链接会过期，
//	                              且「开了存储」不该只覆盖回 b64 的那一半上游）
//	客户端要 b64  而上游给 url  ⇒ 下载后重新编码（不依赖任何配置）
//
// 任何一步失败都保留上游原样 —— 配置缺口和网络抖动都不该把一次成功的生成
// 变成错误响应。响应侧的 `url` 与 `b64_json` 互斥：换掉一个就清掉另一个。
func alignImageCarriers(ctx context.Context, channelID int, response *imageResponse, wantURL bool) bool {
	if response == nil || len(response.Data) == 0 {
		return false
	}
	changed := false
	for idx := range response.Data {
		item := &response.Data[idx]
		if wantURL {
			// 错位形态（地址进了 base64 字段）先归位，再按链接统一处理。
			if looksLikeRemoteReference(item.B64JSON) {
				item.URL = item.B64JSON
				item.B64JSON = ""
				changed = true
			}
			if item.B64JSON != "" {
				raw, err := base64.StdEncoding.DecodeString(item.B64JSON)
				if err != nil || len(raw) == 0 {
					continue
				}
				mime, err := detectImageMimeType(raw, "")
				if err != nil {
					mime = "image/png"
				}
				stored, err := service.PersistArtifactBytes(ctx, channelID, imageObjectKey(raw, mime), mime, raw)
				if err != nil {
					continue
				}
				item.URL = stored.URL
				item.B64JSON = ""
				changed = true
				continue
			}
			// 上游给的是链接。artifact storage 开着就意味着「本渠道的产物留在
			// 自己的桶里」——回 base64 和回链接是同一个产物的两种形状，后者
			// 不是豁免。已是本存储地址（或渠道根本没配存储）时跳过：前者不必
			// 自己下载自己，后者没有地方落盘。
			if item.URL == "" || service.ArtifactStorageCovers(channelID, item.URL) {
				continue
			}
			raw, mime, fetchErr := service.FetchArtifactBytes(ctx, item.URL)
			if fetchErr != nil || len(raw) == 0 {
				continue
			}
			if mime == "" {
				mime = "image/png"
			}
			stored, storeErr := service.PersistArtifactBytes(ctx, channelID, imageObjectKey(raw, mime), mime, raw)
			if storeErr != nil || stored == nil || stored.URL == "" {
				continue
			}
			item.URL = stored.URL
			changed = true
			continue
		}
		// 客户端点名要 base64：上游给的是链接，只能自己取回来。
		if item.URL == "" || item.B64JSON != "" {
			continue
		}
		raw, _, err := service.FetchArtifactBytes(ctx, item.URL)
		if err != nil || len(raw) == 0 {
			continue
		}
		item.B64JSON = base64.StdEncoding.EncodeToString(raw)
		item.URL = ""
		changed = true
	}
	return changed
}

// imageObjectKey 按内容寻址：同一张图重复生成只占一份存储，写两次也是同一个地址。
func imageObjectKey(raw []byte, mime string) string {
	ext := strings.TrimPrefix(mime, "image/")
	if ext == "jpeg" {
		ext = "jpg"
	}
	sum := sha256.Sum256(raw)
	return "images/" + hex.EncodeToString(sum[:16]) + "." + ext
}

// chatImageMarkdown 把 canonical 的图片列表拼成 Markdown。
// 需求口径：**只有流式** chat 用 md 包装（大量 SSE 客户端只认 string delta.content）；
// 非流式与流式共用同一份 markdown：chat 的 message.content 在 OpenAI 规范里
// 只能是字符串，数组形式只存在于「请求」的多模态消息里。同一个入口在
// stream 两边给两种形状，客户端就得写两套解析。
func chatImageMarkdown(response *imageResponse) (string, error) {
	var builder strings.Builder
	for _, item := range response.Data {
		ref, err := item.referenceForItem()
		if err != nil {
			return "", err
		}
		if builder.Len() > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString("![image](")
		builder.WriteString(ref)
		builder.WriteByte(')')
	}
	return builder.String(), nil
}

func chatEnvelopeBase(response *imageResponse, model string, object string) map[string]any {
	promptTokens, completionTokens, totalTokens := response.displayUsage()
	return map[string]any{
		"id":      fmt.Sprintf("chatcmpl-apiduo-%d", time.Now().UnixNano()),
		"object":  object,
		"created": response.Created,
		"model":   model,
		"usage": map[string]any{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      totalTokens,
		},
	}
}

// carryOverResponseExtras 保留上游未被消费的键（usage / revised_prompt …）。
// 服务端计费用的是 finishImageChatResponse 返回的另一份 *dto.Usage，
// 与响应体里的 usage 互不影响。
func carryOverResponseExtras(envelope map[string]any, response *imageResponse, includeItemExtras bool) {
	for key, value := range response.Extras {
		if key == "id" || key == "object" || key == "choices" || key == "model" {
			continue
		}
		envelope[key] = value
	}
	if !includeItemExtras {
		return
	}
	for _, item := range response.Data {
		for key, value := range item.Extras {
			// size 属于 images 形状的 data[]，chat chunk 的顶层没有这个位置：
			// 抬上去就是 choices 旁边多一个非标准键，违反 §5.1 的白名单。
			if key == "size" {
				continue
			}
			if _, exists := envelope[key]; !exists {
				envelope[key] = value
			}
		}
	}
}

// wrapChatCompletion 把 canonical 响应包成非流式 chat.completion。
// 需求口径：非流式不用 md，content 是 image_url parts 数组（skill §2.2 形状）。
func wrapChatCompletion(response *imageResponse, model string) ([]byte, error) {
	markdown, err := chatImageMarkdown(response)
	if err != nil {
		return nil, err
	}
	envelope := chatEnvelopeBase(response, model, "chat.completion")
	envelope["choices"] = []any{map[string]any{
		"index": 0,
		"message": map[string]any{
			"role":    "assistant",
			"content": markdown,
		},
		"finish_reason": "stop",
	}}
	carryOverResponseExtras(envelope, response, false)
	return json.Marshal(envelope)
}

// wrapChatCompletionStream 把同一份 Markdown 包成 SSE 帧序列。
// 上游是同步返回完整图片的，所以这里只有「一帧内容 + 一帧结束 + [DONE]」，
// 不涉及 partial images（skill §6 的范围外）。
func wrapChatCompletionStream(response *imageResponse, model string) ([]byte, error) {
	markdown, err := chatImageMarkdown(response)
	if err != nil {
		return nil, err
	}
	base := chatEnvelopeBase(response, model, "chat.completion.chunk")

	first := make(map[string]any, len(base)+1)
	maps.Copy(first, base)
	first["choices"] = []any{map[string]any{
		"index":         0,
		"delta":         map[string]any{"role": "assistant", "content": markdown},
		"finish_reason": nil,
	}}
	carryOverResponseExtras(first, response, true)

	last := make(map[string]any, len(base)+1)
	maps.Copy(last, base)
	last["choices"] = []any{map[string]any{
		"index":         0,
		"delta":         map[string]any{},
		"finish_reason": "stop",
	}}

	var buf bytes.Buffer
	for _, chunk := range []map[string]any{first, last} {
		data, err := json.Marshal(chunk)
		if err != nil {
			return nil, err
		}
		buf.WriteString("data: ")
		buf.Write(data)
		buf.WriteString("\n\n")
	}
	buf.WriteString("data: [DONE]\n\n")
	return buf.Bytes(), nil
}

// finishImageChatResponse 是 chat 入口的 DoResponse 实现。
// 只有 image protocol 启用且入站是 chat 时才调用；其它情况一概不介入。
func finishImageChatResponse(
	c *gin.Context,
	resp *http.Response,
	info *common.RelayInfo,
	model string,
	requestedCount int,
	stream bool,
	wantURL bool,
) (any, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(
			errors.New("upstream returned no response body"),
			types.ErrorCodeInvalidRequest,
			http.StatusBadGateway,
			types.ErrOptionWithSkipRetry(),
		)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, imageLargeResponseMaxBytes+1))
	_ = resp.Body.Close()
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeInvalidRequest, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
	}
	if len(raw) > imageLargeResponseMaxBytes {
		return nil, types.NewOpenAIError(
			errors.New("upstream image response is too large"),
			types.ErrorCodeInvalidRequest,
			http.StatusBadGateway,
			types.ErrOptionWithSkipRetry(),
		)
	}

	// 防御分支：当前 relay 层（relay/compatible_handler.go:163）会在
	// DoResponse 之前用 service.RelayErrorHandler 重铸所有非 200 响应，
	// 所以这里实际上收不到 4xx/5xx。错误信封重铸（含 401→502、
	// 429 保留 Retry-After）属破坏性变更，skill §3.9 标为待定，这一期不做。
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 不把上游错误页透给客户端：错误信封按入口重建，原文只进管理端日志。
		return nil, types.NewOpenAIError(
			fmt.Errorf("upstream returned status %d", resp.StatusCode),
			types.ErrorCodeBadResponseStatusCode,
			http.StatusBadGateway,
			types.ErrOptionWithSkipRetry(),
		)
	}

	upstream := ""
	if info != nil {
		upstream = info.ChannelOtherSettings.ImageProtocol.Upstream
	}
	decoded, err := decodeImageForUpstream(raw, upstream, time.Now().Unix())
	if err != nil {
		var blocked *geminiImageBlockedError
		if errors.As(err, &blocked) {
			return nil, types.NewOpenAIError(err, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		return nil, types.NewOpenAIError(err, types.ErrorCodeInvalidRequest, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
	}
	decoded = deliverableImages(decoded, upstream)
	decoded = decoded.withEstimatedUsage(info)
	// 客户端要链接、上游只给 base64：先落存储换成地址，再走折形。
	// 失败就保留 base64 —— 那正是「没配存储就透传」的行为。
	if info != nil {
		alignImageCarriers(c.Request.Context(), info.GetChannelID(), decoded, wantURL)
	}
	// 空 data[] 不在这里升格成 5xx：入口的契约是「回包形状完整」
	// （chat 要有 choices/message/content），不是替上游判定业务成败。
	// 空数组会包成 content: []，字段仍在，客户端按入口自行处理。

	isStream := stream || (info != nil && info.IsStream)
	var output []byte
	if isStream {
		output, err = wrapChatCompletionStream(decoded, model)
	} else {
		output, err = wrapChatCompletion(decoded, model)
	}
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeInvalidRequest, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
	}
	if isStream {
		// 不能用 writeImageJSON：它会写 application/json，SSE 客户端会按
		// JSON 解析而拿不到事件流。
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Content-Length", strconv.Itoa(len(output)))
		c.Writer.WriteHeader(http.StatusOK)
		_, _ = c.Writer.Write(output)
		c.Writer.Flush()
		// 直写 ResponseWriter 不会经过 writeImageJSON，trace 采集器也就看不到
		// 响应体；不显式挂一次，抽屉里每条流式 chat 出图都是空 body。
		trace.CaptureResponseBody(c, output)
	} else {
		// 与其它入口共用同一个出口：不复制上游响应头（Server / X-Goog-* /
		// Set-Cookie 都是身份），并统一挂 trace。
		writeImageJSON(c, output)
	}

	// 计费口径必须与 ImageHelper 完全一致，否则同一个模型从不同入口调用
	// 会扣不同的钱。两条实测教训：
	//  1. usage 要 prompt=1 / completion=0；给 1/1 会按 token 计价翻倍
	//     （实测 images 38、chat 75）。TotalTokens>0 仍满足 hasBillableUsage。
	//  2. 乘子用「请求 n」，不用响应 data 长度 —— skill §2.2 明确
	//     data 长度不保证等于 n。且非 UsePrice 时 PrepareImageBillingForRequest
	//     把 quantity 固定为 1，所以这里也不该无条件 AddOtherRatio。
	//     UpdateImageCount 内部已按 UsePrice / tiered 分支处理。
	count := requestedCount
	if count < 1 {
		count = 1
	}
	if info != nil {
		info.ImageRequestCount = count
		info.UpdateImageCount(int64(count))
	}
	return decoded.billedUsageWith(info), nil
}

const imageLargeResponseMaxBytes = 64 << 20

// geminiImageBlockedError 区分「上游拒画」与「网关读不懂」：前者 400 且不重试，
// 后者 502。两者都不允许把上游原文透给客户端。
//
// 错误码保持 invalid_request：它是 OpenAI 信封里的标准值（type 与 code 同源）。
// prompt_blocked 虽然语义更准，但目录里它映射成 content_filter 只有走
// SetClientFacing 才生效，而那条路会把 message 换成固定文案，客户端反而看不出
// 是提示词被拒。要单独给内容策略一个码，得先让 content_filter 保留可操作细节
// （clientErrorKeepsActionableDetail），属独立评估项。
type geminiImageBlockedError struct{ reason string }

func (e *geminiImageBlockedError) Error() string {
	return "upstream blocked this image: " + e.reason
}

// decodeGeminiImageResponse 只认 candidates[].content.parts[].inlineData。
// 不填 Extras：上游的 modelVersion / promptFeedback / safetyRatings 一旦进入
// canonical，回包就会带出去。
func decodeGeminiImageResponse(raw []byte, fallbackCreated int64) (*imageResponse, error) {
	var envelope struct {
		Candidates []struct {
			Content struct {
				Parts []dto.GeminiPart `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		PromptFeedback *struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
		UsageMetadata *struct {
			PromptTokenCount        int `json:"promptTokenCount"`
			CandidatesTokenCount    int `json:"candidatesTokenCount"`
			TotalTokenCount         int `json:"totalTokenCount"`
			ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
			CandidatesTokensDetails []struct {
				Modality   string `json:"modality"`
				TokenCount int    `json:"tokenCount"`
			} `json:"candidatesTokensDetails"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("upstream image response is not JSON: %w", err)
	}
	if envelope.PromptFeedback != nil && strings.TrimSpace(envelope.PromptFeedback.BlockReason) != "" {
		return nil, &geminiImageBlockedError{reason: envelope.PromptFeedback.BlockReason}
	}

	items := make([]imageResponseItem, 0, 1)
	for _, candidate := range envelope.Candidates {
		// 内容级拒绝统一走 400：客户端的请求本身不会被满足，重试也一样。
		// RECITATION / IMAGE_RECITATION 是「可能复现受版权保护的内容」，
		// 在图片场景里和 SAFETY 同一类；漏掉它只会变成 502「上游没图」，
		// 让客户端误以为上游故障。
		switch strings.ToUpper(strings.TrimSpace(candidate.FinishReason)) {
		case "SAFETY", "IMAGE_SAFETY", "PROHIBITED_CONTENT", "IMAGE_PROHIBITED_CONTENT",
			"BLOCKLIST", "RECITATION", "IMAGE_RECITATION", "SPII":
			return nil, &geminiImageBlockedError{reason: candidate.FinishReason}
		}
		for _, part := range candidate.Content.Parts {
			if part.InlineData == nil || part.InlineData.Data == "" {
				continue
			}
			if !strings.HasPrefix(strings.ToLower(part.InlineData.MimeType), "image/") {
				continue
			}
			item := imageResponseItem{B64JSON: part.InlineData.Data}
			// size 是入站契约的另一半：客户端传 size=2048x2048，上游实际给
			// 2752x1536 时必须能看到真实像素，否则「size <=> 档位」只有去的方向。
			if width, height, ok := imagePixelSize(part.InlineData.Data); ok {
				item.Extras = map[string]any{"size": fmt.Sprintf("%dx%d", width, height)}
			}
			items = append(items, item)
		}
	}
	if len(items) == 0 {
		// 零图且不是上面那些已知理由时，把 finishReason 带进错误里：否则
		// 管理端日志只有「no image」，运维没有任何线索判断是上游抽风还是
		// 被挡了。STOP/OTHER 这类也照实记，它们同样是有效信息。
		for _, candidate := range envelope.Candidates {
			if reason := strings.TrimSpace(candidate.FinishReason); reason != "" {
				return nil, fmt.Errorf("upstream returned no image (finishReason=%s)", reason)
			}
		}
		return nil, errors.New("upstream returned no image")
	}
	created := fallbackCreated
	if created == 0 {
		created = time.Now().Unix()
	}
	result := &imageResponse{Created: created, Data: items}
	if usage := envelope.UsageMetadata; usage != nil && usage.TotalTokenCount > 0 {
		imageOutput := 0
		for _, detail := range usage.CandidatesTokensDetails {
			if strings.EqualFold(strings.TrimSpace(detail.Modality), "IMAGE") {
				imageOutput += detail.TokenCount
			}
		}
		result.Usage = &imageUsageSummary{
			PromptTokens:      usage.PromptTokenCount,
			CompletionTokens:  usage.CandidatesTokenCount,
			TotalTokens:       usage.TotalTokenCount,
			ThoughtsTokens:    usage.ThoughtsTokenCount,
			ImageOutputTokens: imageOutput,
		}
	}
	return result, nil
}

// wrapGeminiResponse 构造 Gemini 形状的最小完整回包。finishReason 与 index
// 缺一不可：官方 SDK 会用它们判断是否完成。
func wrapGeminiResponse(response *imageResponse, model string) ([]byte, error) {
	parts := make([]any, 0, len(response.Data))
	for _, item := range response.Data {
		if item.B64JSON == "" {
			continue
		}
		mimeType := sniffBase64ImageMimeType(item.B64JSON)
		parts = append(parts, map[string]any{
			"inlineData": map[string]any{"mimeType": mimeType, "data": item.B64JSON},
		})
	}
	if len(parts) == 0 {
		return nil, errors.New("no inline image to return")
	}
	// usageMetadata 是给客户端看的用量：上游给了真实 token 就用真实的，
	// 没给（部分代理会省略）才退回与账本同口径的 1/0/1。
	usageMetadata := map[string]any{
		"promptTokenCount":     1,
		"candidatesTokenCount": 0,
		"totalTokenCount":      1,
	}
	if usage := response.Usage; usage != nil && usage.TotalTokens > 0 {
		usageMetadata["promptTokenCount"] = usage.PromptTokens
		usageMetadata["candidatesTokenCount"] = usage.CompletionTokens
		usageMetadata["totalTokenCount"] = usage.TotalTokens
		if usage.ThoughtsTokens > 0 {
			usageMetadata["thoughtsTokenCount"] = usage.ThoughtsTokens
		}
	}
	return json.Marshal(map[string]any{
		"candidates": []any{map[string]any{
			"content":      map[string]any{"role": "model", "parts": parts},
			"finishReason": "STOP",
			"index":        0,
		}},
		"modelVersion":  model,
		"usageMetadata": usageMetadata,
	})
}

// sniffBase64ImageMimeType 只解前 512 字节判定格式。整串 Decode 会为一张
// 4K 图多分配 20 MB 以上，而这里只需要魔数。
func sniffBase64ImageMimeType(encoded string) string {
	const sniffBytes = 512
	prefixLen := min(len(encoded), (sniffBytes+2)/3*4)
	prefixLen -= prefixLen % 4
	if prefixLen <= 0 {
		return "image/png"
	}
	head := make([]byte, base64.StdEncoding.DecodedLen(prefixLen))
	n, err := base64.StdEncoding.Decode(head, []byte(encoded[:prefixLen]))
	if n == 0 || (err != nil && n < 12) {
		return "image/png"
	}
	if sniffed, sniffErr := detectImageMimeType(head[:n], ""); sniffErr == nil {
		return sniffed
	}
	return "image/png"
}

// wrapResponsesResponse 构造 Responses 形状的回包。图片写成 output_text 的
// markdown，与 chat 入口同一口径。
func wrapResponsesResponse(response *imageResponse, model string) ([]byte, error) {
	promptTokens, completionTokens, totalTokens := response.displayUsage()
	markdown, err := chatImageMarkdown(response)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	return json.Marshal(map[string]any{
		"id":         fmt.Sprintf("resp_apiduo_%d", now.UnixNano()),
		"object":     "response",
		"created_at": now.Unix(),
		"status":     "completed",
		"model":      model,
		"output": []any{map[string]any{
			"type":   "message",
			"id":     fmt.Sprintf("msg_apiduo_%d", now.UnixNano()),
			"status": "completed",
			"role":   "assistant",
			"content": []any{map[string]any{
				"type":        "output_text",
				"text":        markdown,
				"annotations": []any{},
			}},
		}},
		"usage": map[string]any{
			"input_tokens":  promptTokens,
			"output_tokens": completionTokens,
			"total_tokens":  totalTokens,
		},
	})
}

// finishImageGeminiResponse 是 Gemini 原生入口的回包：只产出 candidates 形状，
// 失败时显式报错，绝不把上游报文透传。
func finishImageGeminiResponse(c *gin.Context, resp *http.Response, info *common.RelayInfo, a *Adaptor) (any, *types.NewAPIError) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, imageLargeResponseMaxBytes+1))
	_ = resp.Body.Close()
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}
	if len(raw) > imageLargeResponseMaxBytes {
		return nil, types.NewOpenAIError(errors.New("upstream image response is too large"), types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}

	model := ""
	if info != nil {
		model = info.OriginModelName
	}
	// 入口是 Gemini，上游不一定是：回包形状由入口决定，解码器由上游决定。
	// 写死 decodeGeminiImageResponse 会让「非 Gemini 上游 × Gemini 入口」
	// 这一整行全部 502（上游回 data[]，解码器找 candidates[]）。
	upstream := ""
	if info != nil {
		upstream = info.ChannelOtherSettings.ImageProtocol.Upstream
	}
	decoded, decodeErr := decodeImageForUpstream(raw, upstream, time.Now().Unix())
	if decodeErr != nil {
		var blocked *geminiImageBlockedError
		if errors.As(decodeErr, &blocked) {
			return nil, types.NewOpenAIError(decodeErr, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		return nil, types.NewOpenAIError(decodeErr, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}
	decoded = decoded.withEstimatedUsage(info)
	if info != nil {
		// Gemini 只认 inlineData：这里强制 b64，不换链接。
		alignImageCarriers(c.Request.Context(), info.GetChannelID(), decoded, false)
	}
	out, err := wrapGeminiResponse(decoded, model)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}
	// 上游始终走非流式：图片是完整产物，分片 base64 对客户端没有价值。
	// 客户端若请求 :streamGenerateContent，就把完整响应包成一帧 SSE——
	// Google SDK 的流式解析只认 data: 行，一帧完整 GenerateContentResponse 可读。
	if geminiStreamRequest(c) {
		writeGeminiStreamResponse(c, out)
	} else {
		writeImageJSON(c, out)
	}
	markImageRequest(info, 1)
	return decoded.billedUsageWith(info), nil
}

// markImageRequest 让 BillingSession 把这条请求当图片请求处理。不设时，
// 授信额度用户可以绕过可用额度检查（billing_session.reserve 的
// requireAvailableQuota 依赖 *dto.ImageRequest 或 ImageRequestCount）。
func markImageRequest(info *common.RelayInfo, count int) {
	if info == nil || count < 1 {
		return
	}
	info.ImageRequestCount = count
	info.UpdateImageCount(int64(count))
}

func geminiStreamRequest(c *gin.Context) bool {
	return c != nil && c.Request != nil && strings.Contains(c.Request.URL.Path, ":streamGenerateContent")
}

// writeGeminiStreamResponse 按 alt 选载体。官方约定是：带 ?alt=sse 回 SSE，
// 不带回 JSON 数组流。Google SDK 默认带 alt=sse，但 curl 与自研客户端不一定，
// 一刀切回 SSE 会让后者按 JSON 数组解析失败。
// 两种载体都只有一帧：上游是非流式的，这里不伪造渐进输出。
func writeGeminiStreamResponse(c *gin.Context, body []byte) {
	if strings.EqualFold(strings.TrimSpace(c.Query("alt")), "sse") {
		writeGeminiSSE(c, body)
		return
	}
	payload := make([]byte, 0, len(body)+2)
	payload = append(payload, '[')
	payload = append(payload, body...)
	payload = append(payload, ']')

	c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.Writer.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	c.Writer.WriteHeader(http.StatusOK)
	_, _ = c.Writer.Write(payload)
	trace.CaptureResponseBody(c, payload)
}

// writeGeminiSSE 写一帧完整响应，供 alt=sse 使用。
func writeGeminiSSE(c *gin.Context, body []byte) {
	payload := make([]byte, 0, len(body)+8)
	payload = append(payload, "data: "...)
	payload = append(payload, body...)
	payload = append(payload, '\n', '\n')

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	c.Writer.WriteHeader(http.StatusOK)
	_, _ = c.Writer.Write(payload)
	c.Writer.Flush()
	trace.CaptureResponseBody(c, payload)
}

// finishImageResponsesResponse 是 responses 入口的回包。流式暂不支持：
// 图片是一次性产出的，硬拼 SSE 事件只会给客户端半份数据，不如明确拒绝。
func finishImageResponsesResponse(c *gin.Context, resp *http.Response, info *common.RelayInfo, a *Adaptor) (any, *types.NewAPIError) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, imageLargeResponseMaxBytes+1))
	_ = resp.Body.Close()
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}
	if len(raw) > imageLargeResponseMaxBytes {
		return nil, types.NewOpenAIError(errors.New("upstream image response is too large"), types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}
	upstream := ""
	if info != nil {
		upstream = info.ChannelOtherSettings.ImageProtocol.Upstream
	}
	decoded, decodeErr := decodeImageForUpstream(raw, upstream, time.Now().Unix())
	if decodeErr != nil {
		var blocked *geminiImageBlockedError
		if errors.As(decodeErr, &blocked) {
			return nil, types.NewOpenAIError(decodeErr, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		return nil, types.NewOpenAIError(decodeErr, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}
	model := ""
	decoded = deliverableImages(decoded, upstream)
	decoded = decoded.withEstimatedUsage(info)
	if info != nil {
		model = info.OriginModelName
		alignImageCarriers(c.Request.Context(), info.GetChannelID(), decoded, a.imageWantURL)
	}
	if a.imageStream {
		markdown, markdownErr := chatImageMarkdown(decoded)
		if markdownErr != nil {
			return nil, types.NewOpenAIError(markdownErr, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
		}
		if streamErr := writeResponsesImageStream(c, info, model, markdown, decoded.billedUsageWith(info)); streamErr != nil {
			return nil, streamErr
		}
		markImageRequest(info, 1)
		return decoded.billedUsageWith(info), nil
	}
	out, err := wrapResponsesResponse(decoded, model)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}
	writeImageJSON(c, out)
	markImageRequest(info, 1)
	return decoded.billedUsageWith(info), nil
}

// writeResponsesImageStream 把完整 markdown 包成 Responses 的 SSE 事件序列。
// 上游本来是非流式，所以这不是渐进输出，只是让 stream=true 的客户端拿到
// 合法事件流。事件由 relaykit 的 chat→responses 转换器生成：手写九个事件
// 会在协议变更时漂移，而 chat 的两帧格式不能直接冒充 responses。
func writeResponsesImageStream(c *gin.Context, info *common.RelayInfo, model string, markdown string, usage *dto.Usage) *types.NewAPIError {
	state, err := relayconvert.NewResponseStreamState(
		types.RelayFormatOpenAI,
		types.RelayFormatOpenAIResponses,
		relayconvert.ResponseStreamOptions{
			ID:                 helper.GetResponseID(c),
			Model:              model,
			EmitSequenceNumber: true,
		},
	)
	if err != nil {
		return types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}

	content := markdown
	chunk := &dto.ChatCompletionsStreamResponse{
		Id:      fmt.Sprintf("chatcmpl-apiduo-%d", time.Now().UnixNano()),
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []dto.ChatCompletionsStreamResponseChoice{{
			Index: 0,
			Delta: dto.ChatCompletionsStreamResponseChoiceDelta{
				Role:    "assistant",
				Content: &content,
			},
		}},
	}
	results, err := service.ConvertStreamResponseChunk(c, info, state, chunk)
	if err != nil {
		return types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}

	// 头要在第一个事件之前设：ResponseChunkData 只写 event/data。
	helper.SetEventStreamHeaders(c)
	if streamErr := writeResponsesStreamResults(c, results); streamErr != nil {
		return streamErr
	}

	// completed 事件的 usage 与非流式同口径：一张图 = 1 个 input token。
	state.SetUsage(usage)
	finalResults, err := service.FinalizeStreamResponse(c, info, state)
	if err != nil {
		return types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	return writeResponsesStreamResults(c, finalResults)
}

func writeResponsesStreamResults(c *gin.Context, results []relayconvert.ResponseResult) *types.NewAPIError {
	for _, result := range results {
		event, ok := result.Value.(relayconvert.ChatToResponsesStreamEvent)
		if !ok {
			return types.NewOpenAIError(
				fmt.Errorf("expected responses stream event, got %T", result.Value),
				types.ErrorCodeBadResponse,
				http.StatusInternalServerError,
			)
		}
		data, err := json.Marshal(event.Payload)
		if err != nil {
			return types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
		}
		if err := helper.ResponseChunkData(c, dto.ResponsesStreamResponse{Type: event.Type}, string(data)); err != nil {
			if c.Request.Context().Err() != nil {
				// 客户端断开：图已生成，按正常结算，不算服务端失败。
				return nil
			}
			return types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
		}
	}
	return nil
}

// writeImageJSON 是图片回包的唯一出口。它不复制上游响应头：Server /
// X-Goog-* / Set-Cookie 都是上游身份，而回包本身已经是按入口重建的。
func writeImageJSON(c *gin.Context, body []byte) {
	c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.Writer.Header().Set("Content-Length", strconv.Itoa(len(body)))
	c.Writer.WriteHeader(http.StatusOK)
	_, _ = c.Writer.Write(body)
	trace.CaptureResponseBody(c, body)
}

// imageSizeHeaderWindow 是解码头部窗口的 base64 字符数（约 3KB 原始字节）：
// SOF 在 EXIF 之后也能落在窗口内，而 4K 图的完整 b64 有 2700 万字符，
// 全解一遍只为读 24 字节不值。
const imageSizeHeaderWindow = 4096

// imagePixelSize 从 base64 图片头读出交付像素。
//
// 这是 size 映射的回程：入站把 size 折成 (aspectRatio, imageSize)，出站必须
// 把上游真实给的像素写回 size。客户端传 2048x2048、上游按最近档给 2752x1536
// 时，回包说 2048x2048 就是撒谎，不说又只能靠客户端自己解 PNG。
//
// 只认 PNG 与 JPEG：它们覆盖 Gemini 与 OpenAI 形状上游的全部回包；认不出就
// 不填，少一个便利字段不影响正确性。
func imagePixelSize(data string) (int, int, bool) {
	window := data
	if len(window) > imageSizeHeaderWindow {
		window = window[:imageSizeHeaderWindow]
	}
	// 截断到 4 的整数倍：base64 尾部残缺会直接解码失败。
	window = window[:len(window)/4*4]
	blob, err := base64.StdEncoding.DecodeString(window)
	if err != nil || len(blob) < 24 {
		return 0, 0, false
	}
	if bytes.HasPrefix(blob, []byte("\x89PNG\r\n\x1a\n")) && string(blob[12:16]) == "IHDR" {
		width := int(binary.BigEndian.Uint32(blob[16:20]))
		height := int(binary.BigEndian.Uint32(blob[20:24]))
		return width, height, width > 0 && height > 0
	}
	if blob[0] == 0xFF && blob[1] == 0xD8 {
		return jpegPixelSize(blob)
	}
	return 0, 0, false
}

// jpegPixelSize 扫段直到 SOF：段长只覆盖头部，熵编码数据无法按长度跳过，
// 所以遇到 SOS 还没 SOF 就放弃，不猜。
func jpegPixelSize(blob []byte) (int, int, bool) {
	position := 2
	for position+9 <= len(blob) {
		if blob[position] != 0xFF {
			return 0, 0, false
		}
		marker := blob[position+1]
		if marker == 0xDA {
			return 0, 0, false
		}
		// SOF0..SOF15：C4(DHT) / C8(JPG) / CC(DAC) 不是帧头。
		if marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC {
			height := int(binary.BigEndian.Uint16(blob[position+5 : position+7]))
			width := int(binary.BigEndian.Uint16(blob[position+7 : position+9]))
			return width, height, width > 0 && height > 0
		}
		segment := int(binary.BigEndian.Uint16(blob[position+2 : position+4]))
		if segment < 2 {
			return 0, 0, false
		}
		position += 2 + segment
	}
	return 0, 0, false
}
