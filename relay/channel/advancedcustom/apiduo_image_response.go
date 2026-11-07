package advancedcustom

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
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

const imageResponseMaxBytes = 16 << 20

type imageResponseItem struct {
	URL     string
	B64JSON string
	Extras  map[string]any
}

type imageResponse struct {
	Created int64
	Data    []imageResponseItem
	Extras  map[string]any
}

// decodeImageResponse 解析上游 {created, data:[{url|b64_json, …}]}。
// 附加字段（revised_prompt / usage / size …）一律保留，usage 是计费依据不许丢。
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
		if key == "created" || key == "data" {
			continue
		}
		result.Extras[key] = value
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
		for key, value := range object {
			if key != "url" && key != "b64_json" {
				item.Extras[key] = value
			}
		}
		result.Data = append(result.Data, item)
	}
	return result, nil
}

// referenceForItem 合成客户端可消费的图片引用。
// url 与 b64_json 互斥；后者转成 data URI，裸 b64 会被当成链接而 400。
func (i imageResponseItem) referenceForItem() (string, error) {
	if i.B64JSON != "" {
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

// chatImageMarkdown 把 canonical 的图片列表拼成 Markdown。
// 需求口径：**只有流式** chat 用 md 包装（大量 SSE 客户端只认 string delta.content）；
// 非流式用结构化的 image_url parts，见 chatImageParts。
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

// chatImageParts 生成非流式 chat 的 content：结构化的 image_url parts。
// 每张图的附加字段（revised_prompt / size …）跟着各自的 part 走。
func chatImageParts(response *imageResponse) ([]map[string]any, error) {
	parts := make([]map[string]any, 0, len(response.Data))
	for _, item := range response.Data {
		ref, err := item.referenceForItem()
		if err != nil {
			return nil, err
		}
		part := map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": ref},
		}
		for key, value := range item.Extras {
			if key != "type" && key != "image_url" {
				part[key] = value
			}
		}
		parts = append(parts, part)
	}
	return parts, nil
}

func chatEnvelopeBase(response *imageResponse, model string, object string) map[string]any {
	return map[string]any{
		"id":      fmt.Sprintf("chatcmpl-apiduo-%d", time.Now().UnixNano()),
		"object":  object,
		"created": response.Created,
		"model":   model,
		"usage": map[string]any{
			"prompt_tokens":     1,
			"completion_tokens": 0,
			"total_tokens":      1,
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
			if _, exists := envelope[key]; !exists {
				envelope[key] = value
			}
		}
	}
}

// wrapChatCompletion 把 canonical 响应包成非流式 chat.completion。
// 需求口径：非流式不用 md，content 是 image_url parts 数组（skill §2.2 形状）。
func wrapChatCompletion(response *imageResponse, model string) ([]byte, error) {
	parts, err := chatImageParts(response)
	if err != nil {
		return nil, err
	}
	envelope := chatEnvelopeBase(response, model, "chat.completion")
	envelope["choices"] = []any{map[string]any{
		"index": 0,
		"message": map[string]any{
			"role":    "assistant",
			"content": parts,
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
) (any, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(
			errors.New("upstream returned no response body"),
			types.ErrorCodeInvalidRequest,
			http.StatusBadGateway,
			types.ErrOptionWithSkipRetry(),
		)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, imageResponseMaxBytes+1))
	_ = resp.Body.Close()
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeInvalidRequest, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
	}
	if len(raw) > imageResponseMaxBytes {
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
		service.IOCopyBytesGracefully(c, resp, raw)
		return &dto.Usage{}, nil
	}

	decoded, err := decodeImageResponse(raw)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeInvalidRequest, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
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
		// 不能用 IOCopyBytesGracefully：它会把上游的 application/json
		// 覆盖回来，SSE 客户端会按 JSON 解析而拿不到事件流。
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Content-Length", strconv.Itoa(len(output)))
		c.Writer.WriteHeader(http.StatusOK)
		_, _ = c.Writer.Write(output)
		c.Writer.Flush()
	} else {
		service.IOCopyBytesGracefully(c, resp, output)
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
	return &dto.Usage{PromptTokens: 1, CompletionTokens: 0, TotalTokens: 1}, nil
}
