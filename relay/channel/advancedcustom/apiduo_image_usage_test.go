package advancedcustom

import "testing"

// Gemini 的 totalTokenCount 含 thoughts（思考按输出计费），所以 completion 必须取
// total-prompt。用 candidatesTokenCount 会漏掉 526 个思考 token。
func TestBilledUsageFromGeminiMetadata(t *testing.T) {
	body := `{"candidates":[{"content":{"parts":[
		{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}
	]},"finishReason":"STOP"}],
	"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1120,"totalTokenCount":1649,"thoughtsTokenCount":526}}`

	decoded, err := decodeGeminiImageResponse([]byte(body), 0)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	usage := decoded.billedUsage()
	if usage.PromptTokens != 3 || usage.CompletionTokens != 1646 || usage.TotalTokens != 1649 {
		t.Fatalf("usage = %+v, want prompt 3 / completion 1646 / total 1649", usage)
	}
}

// ark 的图片接口回 {generated_images, output_tokens, total_tokens}：prompt 由
// total-output 推出来。拿不到总用量时不能瞎猜。
func TestBilledUsageFromOpenAIShape(t *testing.T) {
	body := `{"created":1,"data":[{"b64_json":"aGVsbG8="}],
	"usage":{"generated_images":1,"output_tokens":12600,"total_tokens":12600}}`

	decoded, err := decodeImageResponse([]byte(body))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	usage := decoded.billedUsage()
	if usage.PromptTokens != 0 || usage.CompletionTokens != 12600 || usage.TotalTokens != 12600 {
		t.Fatalf("usage = %+v", usage)
	}
}

// 没有任何用量时退回 {1,0,1}：它满足 hasBillableUsage，也是按次计价的口径。
// 按 token 计价的模型拿不到用量会结算 0，所以这条回退必须显式可测。
func TestBilledUsageFallsBackToSingleUnit(t *testing.T) {
	body := `{"created":1,"data":[{"b64_json":"aGVsbG8="}]}`
	decoded, err := decodeImageResponse([]byte(body))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	usage := decoded.billedUsage()
	if usage.PromptTokens != 1 || usage.CompletionTokens != 0 || usage.TotalTokens != 1 {
		t.Fatalf("fallback usage = %+v", usage)
	}
}

// 回包里给客户端看的数字必须与账本同源，否则会出现「响应说 1 个 token、账单按 1100 收」。
func TestDisplayUsageMatchesBilling(t *testing.T) {
	body := `{"candidates":[{"content":{"parts":[
		{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}
	]},"finishReason":"STOP"}],
	"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":1290,"totalTokenCount":1297}}`

	decoded, err := decodeGeminiImageResponse([]byte(body), 0)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	prompt, completion, total := decoded.displayUsage()
	billed := decoded.billedUsage()
	if prompt != billed.PromptTokens || completion != billed.CompletionTokens || total != billed.TotalTokens {
		t.Fatalf("display %d/%d/%d != billed %+v", prompt, completion, total, billed)
	}
}
