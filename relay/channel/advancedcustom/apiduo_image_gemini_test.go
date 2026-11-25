package advancedcustom

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
)

func decodeGeminiPlanBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("plan body is not JSON: %v", err)
	}
	return parsed
}

func geminiPlanParts(t *testing.T, body []byte) []any {
	t.Helper()
	parsed := decodeGeminiPlanBody(t, body)
	contents, ok := parsed["contents"].([]any)
	if !ok || len(contents) != 1 {
		t.Fatalf("contents = %#v", parsed["contents"])
	}
	parts, ok := contents[0].(map[string]any)["parts"].([]any)
	if !ok {
		t.Fatalf("parts missing: %#v", contents[0])
	}
	return parts
}

func TestPlanGeminiTextToImage(t *testing.T) {
	canonical := newImageCanonical()
	canonical.fields["prompt"] = "a cat"
	canonical.fields["size"] = "1024x1024"

	plan, err := planGemini(context.Background(), canonical, "gemini-2.5-flash-image")
	if err != nil {
		t.Fatalf("planGemini: %v", err)
	}
	if want := "/v1beta/models/gemini-2.5-flash-image:generateContent"; plan.UpstreamPath != want {
		t.Fatalf("path = %q, want %q", plan.UpstreamPath, want)
	}
	parts := geminiPlanParts(t, plan.Body)
	if len(parts) != 1 {
		t.Fatalf("parts = %d, want 1 (text only)", len(parts))
	}
	if parts[0].(map[string]any)["text"] != "a cat" {
		t.Fatalf("text part = %#v", parts[0])
	}

	parsed := decodeGeminiPlanBody(t, plan.Body)
	generationConfig := parsed["generationConfig"].(map[string]any)
	imageConfig := generationConfig["imageConfig"].(map[string]any)
	if imageConfig["aspectRatio"] != "1:1" {
		t.Fatalf("aspectRatio = %v", imageConfig["aspectRatio"])
	}
	modalities := generationConfig["responseModalities"].([]any)
	if len(modalities) != 1 || modalities[0] != "IMAGE" {
		t.Fatalf("responseModalities = %#v", modalities)
	}
}

func TestPlanGeminiImageToImageKeepsOrder(t *testing.T) {
	canonical := newImageCanonical()
	canonical.fields["prompt"] = "repaint"
	canonical.fields["image"] = []string{
		"data:image/png;base64,iVBORw0KGgo=",
		"data:image/webp;base64,UklGRg==",
	}
	canonical.fields["aspect_ratio"] = "16:9"
	canonical.fields["image_size"] = "2k"

	plan, err := planGemini(context.Background(), canonical, "gemini-3-pro-image-preview")
	if err != nil {
		t.Fatalf("planGemini: %v", err)
	}
	parts := geminiPlanParts(t, plan.Body)
	if len(parts) != 3 {
		t.Fatalf("parts = %d, want text + two inline images", len(parts))
	}
	first := parts[1].(map[string]any)["inlineData"].(map[string]any)
	second := parts[2].(map[string]any)["inlineData"].(map[string]any)
	if first["mimeType"] != "image/png" || second["mimeType"] != "image/webp" {
		t.Fatalf("inline order/mime = %#v %#v", first, second)
	}
	parsed := decodeGeminiPlanBody(t, plan.Body)
	imageConfig := parsed["generationConfig"].(map[string]any)["imageConfig"].(map[string]any)
	if imageConfig["aspectRatio"] != "16:9" {
		t.Fatalf("explicit aspect_ratio should win, got %v", imageConfig["aspectRatio"])
	}
	if imageConfig["imageSize"] != "2K" {
		t.Fatalf("imageSize = %v, want 2K", imageConfig["imageSize"])
	}
}

func TestPlanGeminiRejectsUnknownImageSize(t *testing.T) {
	canonical := newImageCanonical()
	canonical.fields["prompt"] = "a cat"
	canonical.fields["image_size"] = "8K"

	if _, err := planGemini(context.Background(), canonical, "gemini-2.5-flash-image"); err == nil {
		t.Fatal("image_size 8K must be rejected")
	}
}

func TestPlanGeminiUnknownSizeIsLeftToUpstream(t *testing.T) {
	canonical := newImageCanonical()
	canonical.fields["prompt"] = "a cat"
	canonical.fields["size"] = "999x999"

	plan, err := planGemini(context.Background(), canonical, "gemini-2.5-flash-image")
	if err != nil {
		t.Fatalf("planGemini: %v", err)
	}
	parsed := decodeGeminiPlanBody(t, plan.Body)
	generationConfig := parsed["generationConfig"].(map[string]any)
	if _, exists := generationConfig["imageConfig"]; exists {
		t.Fatalf("unknown size must not invent an aspect ratio: %#v", generationConfig["imageConfig"])
	}
}

func TestCanonicalFromGeminiTakesLastUserTurn(t *testing.T) {
	request := &dto.GeminiChatRequest{
		Contents: []dto.GeminiChatContent{
			{Role: "user", Parts: []dto.GeminiPart{{Text: "old turn"}}},
			{Role: "model", Parts: []dto.GeminiPart{{Text: "answer"}}},
			{Role: "user", Parts: []dto.GeminiPart{
				{Text: "new turn"},
				{InlineData: &dto.GeminiInlineData{MimeType: "image/png", Data: "iVBORw0KGgo="}},
			}},
		},
	}

	canonical, err := canonicalFromGemini(request, "gemini-2.5-flash-image")
	if err != nil {
		t.Fatalf("canonicalFromGemini: %v", err)
	}
	if got := canonical.fields["prompt"]; got != "new turn" {
		t.Fatalf("prompt = %v", got)
	}
	refs := canonical.imageRefs()
	if len(refs) != 1 || !strings.HasPrefix(refs[0], "data:image/png;base64,") {
		t.Fatalf("image refs = %#v", refs)
	}
}

func TestDecodeGeminiImageResponse(t *testing.T) {
	raw := []byte(`{
		"candidates": [{
			"content": {"role": "model", "parts": [
				{"text": "here"},
				{"inlineData": {"mimeType": "image/png", "data": "iVBORw0KGgo="}}
			]},
			"finishReason": "STOP",
			"index": 0
		}],
		"modelVersion": "gemini-2.5-flash-image"
	}`)

	decoded, err := decodeGeminiImageResponse(raw, 1700000000)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Created != 1700000000 {
		t.Fatalf("created = %d", decoded.Created)
	}
	if len(decoded.Data) != 1 || decoded.Data[0].B64JSON != "iVBORw0KGgo=" {
		t.Fatalf("data = %#v", decoded.Data)
	}
	if len(decoded.Extras) != 0 {
		t.Fatalf("extras must stay empty, got %#v", decoded.Extras)
	}
}

func TestDecodeGeminiImageResponseBlocked(t *testing.T) {
	raw := []byte(`{"candidates":[{"content":{"parts":[]},"finishReason":"SAFETY"}]}`)
	_, err := decodeGeminiImageResponse(raw, 1)
	if err == nil {
		t.Fatal("SAFETY must be an error")
	}
	var blocked *geminiImageBlockedError
	if !asBlocked(err, &blocked) {
		t.Fatalf("err = %T, want geminiImageBlockedError", err)
	}
}

func TestDecodeGeminiImageResponseWithoutImage(t *testing.T) {
	raw := []byte(`{"candidates":[{"content":{"parts":[{"text":"I cannot draw that"}]},"finishReason":"STOP"}]}`)
	if _, err := decodeGeminiImageResponse(raw, 1); err == nil {
		t.Fatal("text-only response must not be treated as an image")
	}
}

func asBlocked(err error, target **geminiImageBlockedError) bool {
	blocked, ok := err.(*geminiImageBlockedError)
	if ok {
		*target = blocked
	}
	return ok
}

func TestCanonicalFromResponsesStringInput(t *testing.T) {
	request := &dto.OpenAIResponsesRequest{Input: json.RawMessage(`"a cat"`)}
	canonical, err := canonicalFromResponses(request, "banana")
	if err != nil {
		t.Fatalf("canonicalFromResponses: %v", err)
	}
	if got := canonical.fields["prompt"]; got != "a cat" {
		t.Fatalf("prompt = %v", got)
	}
	if canonical.hasImage() {
		t.Fatal("string input must not invent an image")
	}
}

func TestCanonicalFromResponsesTakesLastUserTurn(t *testing.T) {
	input := `[
		{"role":"user","content":[{"type":"input_text","text":"old turn"}]},
		{"role":"assistant","content":[{"type":"output_text","text":"answer"}]},
		{"role":"user","content":[
			{"type":"input_text","text":"new turn"},
			{"type":"input_image","image_url":{"url":"https://cdn.example.com/cat.png"}}
		]}
	]`
	request := &dto.OpenAIResponsesRequest{
		Input:        json.RawMessage(input),
		Instructions: json.RawMessage(`"be brief"`),
	}
	canonical, err := canonicalFromResponses(request, "banana")
	if err != nil {
		t.Fatalf("canonicalFromResponses: %v", err)
	}
	if got, want := canonical.fields["prompt"], "be brief\n\nnew turn"; got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
	refs := canonical.imageRefs()
	if len(refs) != 1 || refs[0] != "https://cdn.example.com/cat.png" {
		t.Fatalf("refs = %#v", refs)
	}
}

func TestCanonicalFromResponsesRejectsEmpty(t *testing.T) {
	if _, err := canonicalFromResponses(&dto.OpenAIResponsesRequest{}, "banana"); err == nil {
		t.Fatal("empty input must be rejected")
	}
}

func TestDecodeImageForUpstream(t *testing.T) {
	geminiBody := `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}]},"finishReason":"STOP"}]}`
	decoded, err := decodeImageForUpstream([]byte(geminiBody), "gemini", 0)
	if err != nil {
		t.Fatalf("gemini upstream: %v", err)
	}
	if len(decoded.Data) != 1 || decoded.Data[0].B64JSON != "aGVsbG8=" {
		t.Fatalf("gemini upstream decoded %#v", decoded.Data)
	}

	openAIBody := `{"created":7,"data":[{"b64_json":"aGVsbG8="}]}`
	for _, upstream := range []string{"ark", "openai", "seedream", ""} {
		decoded, err = decodeImageForUpstream([]byte(openAIBody), upstream, 0)
		if err != nil {
			t.Fatalf("upstream %q: %v", upstream, err)
		}
		if len(decoded.Data) != 1 || decoded.Data[0].B64JSON != "aGVsbG8=" {
			t.Fatalf("upstream %q decoded %#v", upstream, decoded.Data)
		}
	}

	// 上游形状与配置不一致时必须失败，不能把 candidates 当 data[] 静默读成空。
	if _, err := decodeImageForUpstream([]byte(geminiBody), "ark", 0); err == nil {
		t.Fatal("ark decoder must reject a candidates[] body")
	}
}

func TestWriteResponsesImageStream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	markdown := "![image](data:image/png;base64,aGVsbG8=)"
	usage := &dto.Usage{PromptTokens: 7, CompletionTokens: 1290, TotalTokens: 1297}
	if apiErr := writeResponsesImageStream(c, &common.RelayInfo{}, "banana", markdown, usage); apiErr != nil {
		t.Fatalf("writeResponsesImageStream: %v", apiErr)
	}

	if got := recorder.Header().Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Fatalf("content type = %q", got)
	}
	body := recorder.Body.String()
	for _, want := range []string{
		"event: response.created",
		"event: response.output_text.delta",
		"event: response.output_text.done",
		"event: response.completed",
		"aGVsbG8=",
		`"total_tokens":1297`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in stream:\n%s", want, body)
		}
	}
	// 终态事件必须在 delta 之后，否则客户端拿不到文本。
	if strings.Index(body, "response.output_text.delta") > strings.Index(body, "response.completed") {
		t.Fatal("response.completed emitted before the delta")
	}
}

func TestWriteGeminiStreamResponseAlt(t *testing.T) {
	gin.SetMode(gin.TestMode)

	sseRecorder := httptest.NewRecorder()
	sseCtx, _ := gin.CreateTestContext(sseRecorder)
	sseCtx.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/x:streamGenerateContent?alt=sse", nil)
	writeGeminiStreamResponse(sseCtx, []byte(`{"candidates":[]}`))
	if got := sseRecorder.Body.String(); !strings.HasPrefix(got, "data: ") {
		t.Fatalf("alt=sse body = %q", got)
	}

	jsonRecorder := httptest.NewRecorder()
	jsonCtx, _ := gin.CreateTestContext(jsonRecorder)
	jsonCtx.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/x:streamGenerateContent", nil)
	writeGeminiStreamResponse(jsonCtx, []byte(`{"candidates":[]}`))
	if got, want := jsonRecorder.Body.String(), `[{"candidates":[]}]`; got != want {
		t.Fatalf("default body = %q, want %q", got, want)
	}
	if got := jsonRecorder.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("content type = %q", got)
	}
}

func TestPlanGeminiKeepsNamespacedModelSegments(t *testing.T) {
	canonical := newImageCanonical()
	canonical.fields["model"] = "google/gemini-nano-banana-2.1"
	canonical.fields["prompt"] = "a cat"
	plan, err := planGemini(context.Background(), canonical, "google/gemini-nano-banana-2.1")
	if err != nil {
		t.Fatalf("planGemini: %v", err)
	}
	const want = "/v1beta/models/google/gemini-nano-banana-2.1:generateContent"
	if plan.UpstreamPath != want {
		t.Fatalf("upstream path = %q, want %q", plan.UpstreamPath, want)
	}
}

// 真实上游（zenmux / google/gemini-nano-banana-2.1）一次调用回两个 inlineData：
// 先一张草稿、再一张成品。OpenAI 形状的入口只交付最后一张，否则 data[] 长度
// 会大于 n，与按 n 预扣的账本对不上。
func TestDeliverableImagesKeepsFinalForGemini(t *testing.T) {
	body := `{"candidates":[{"content":{"parts":[
		{"text":"refining the concept","thought":true},
		{"inlineData":{"mimeType":"image/png","data":"ZHJhZnQ="}},
		{"text":"completed","thought":true},
		{"inlineData":{"mimeType":"image/png","data":"ZmluYWw="}}
	]},"finishReason":"STOP"}]}`

	decoded, err := decodeGeminiImageResponse([]byte(body), 0)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded.Data) != 2 {
		t.Fatalf("decoder must stay faithful, got %d images", len(decoded.Data))
	}

	trimmed := deliverableImages(decoded, "gemini")
	if len(trimmed.Data) != 1 {
		t.Fatalf("gemini must deliver 1 image, got %d", len(trimmed.Data))
	}
	if trimmed.Data[0].B64JSON != "ZmluYWw=" {
		t.Fatalf("must keep the final image, got %q", trimmed.Data[0].B64JSON)
	}
}

// ark / openai 的 n 是真扇出：两张就是两张，不能裁。
func TestDeliverableImagesLeavesFanoutUpstreams(t *testing.T) {
	response := &imageResponse{Data: []imageResponseItem{
		{B64JSON: "b25l"}, {B64JSON: "dHdv"},
	}}
	for _, upstream := range []string{"ark", "openai", "seedream", ""} {
		got := deliverableImages(response, upstream)
		if len(got.Data) != 2 {
			t.Fatalf("upstream %q must keep 2 images, got %d", upstream, len(got.Data))
		}
	}
	if got := deliverableImages(nil, "gemini"); got != nil {
		t.Fatal("nil response must stay nil")
	}
}

// usageMetadata 用上游真实 token（展示用）；账本仍按张，不能跟着变成 token 计费。
func TestWrapGeminiResponseUsesRealUsage(t *testing.T) {
	body := `{"candidates":[{"content":{"parts":[
		{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}
	]},"finishReason":"STOP"}],
	"usageMetadata":{"promptTokenCount":6,"candidatesTokenCount":1148,"totalTokenCount":2056,"thoughtsTokenCount":526,"trafficType":"ON_DEMAND"}}`

	decoded, err := decodeGeminiImageResponse([]byte(body), 0)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	raw, err := wrapGeminiResponse(decoded, "banana")
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	usage, _ := out["usageMetadata"].(map[string]any)
	if usage["promptTokenCount"] != float64(6) || usage["totalTokenCount"] != float64(2056) {
		t.Fatalf("usage must be the upstream one, got %#v", usage)
	}
	if _, leaked := usage["trafficType"]; leaked {
		t.Fatalf("trafficType is upstream scheduling info, must not leak: %#v", usage)
	}
	candidates, _ := out["candidates"].([]any)
	first, _ := candidates[0].(map[string]any)
	parts, _ := first["content"].(map[string]any)["parts"].([]any)
	if len(parts) != 1 {
		t.Fatalf("gemini entry keeps the source parts, got %d", len(parts))
	}
}

// Gemini 没有 mask 通道，静默忽略会产出「不是用户要的图」并照常计费。
func TestPlanGeminiRejectsMask(t *testing.T) {
	canonical := newImageCanonical()
	canonical.fields["model"] = "banana"
	canonical.fields["prompt"] = "make the sky blue"
	canonical.fields["image"] = "data:image/png;base64,aGVsbG8="
	canonical.fields["mask"] = "data:image/png;base64,bWFzaw=="

	_, err := planGemini(context.Background(), canonical, "banana")
	if err == nil {
		t.Fatal("mask must be rejected instead of silently dropped")
	}
	if !strings.Contains(err.Error(), "mask") {
		t.Fatalf("error must name the offending field, got %v", err)
	}
}

// 真实上游（zenmux / google/gemini-nano-banana-2.1）的报文形状，只把 base64 换成
// 1x1 PNG、thoughtSignature 换成短值。锁三件事：
//
//	① 未知字段（thought / thoughtSignature / createTime / responseId）不影响解码；
//	② usageMetadata 解析成展示用量，trafficType 不出网关；
//	③ 两道 thought 夹两张图时，OpenAI 形状的入口只交付最后一张。
func TestRealGeminiPairFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/gemini_nano_banana_pair.json")
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	decoded, err := decodeGeminiImageResponse(raw, 0)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded.Data) != 2 {
		t.Fatalf("decoder must stay faithful to the source, got %d", len(decoded.Data))
	}
	if decoded.Usage == nil ||
		decoded.Usage.PromptTokens != 3 ||
		decoded.Usage.CompletionTokens != 1120 ||
		decoded.Usage.TotalTokens != 1649 ||
		decoded.Usage.ThoughtsTokens != 526 {
		t.Fatalf("usage must match the captured payload, got %+v", decoded.Usage)
	}

	// 1x1 PNG 的两种字节；成品是第二张。
	final := decoded.Data[1].B64JSON
	trimmed := deliverableImages(decoded, "gemini")
	if len(trimmed.Data) != 1 || trimmed.Data[0].B64JSON != final {
		t.Fatalf("OpenAI entries must deliver the final image only, got %d", len(trimmed.Data))
	}

	out, err := wrapGeminiResponse(trimmed, "google/gemini-nano-banana-2.1")
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	var rebuilt map[string]any
	if err := json.Unmarshal(out, &rebuilt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, leaked := rebuilt["trafficType"]; leaked {
		t.Fatal("trafficType must not appear at the top level")
	}
	usage := rebuilt["usageMetadata"].(map[string]any)
	if _, leaked := usage["trafficType"]; leaked {
		t.Fatalf("trafficType is upstream scheduling info: %#v", usage)
	}
	if _, leaked := rebuilt["responseId"]; leaked {
		t.Fatalf("responseId is upstream identity: %#v", rebuilt)
	}
}
