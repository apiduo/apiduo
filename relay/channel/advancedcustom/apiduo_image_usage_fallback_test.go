package advancedcustom

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

// An upstream that reports no usage must still produce one set of numbers for
// the response body and the ledger: the local estimate the relay layer attached.
func TestBilledUsageFallsBackToEstimate(t *testing.T) {
	decoded, err := decodeImageResponse([]byte(`{"created":1,"data":[{"b64_json":"aGVsbG8="}]}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	estimate := &dto.Usage{
		PromptTokens:           20,
		CompletionTokens:       1056,
		TotalTokens:            1076,
		CompletionTokenDetails: dto.OutputTokenDetails{ImageTokens: 1056},
	}
	decoded.fallbackUsage = estimate

	if usage := decoded.billedUsage(); usage != estimate {
		t.Fatalf("billed usage = %+v, want the estimate", usage)
	}
	prompt, completion, total := decoded.displayUsage()
	if prompt != 20 || completion != 1056 || total != 1076 {
		t.Fatalf("display %d/%d/%d, want 20/1056/1076", prompt, completion, total)
	}
}

// Real upstream usage always wins over the estimate, even when the estimate is
// larger.
func TestUpstreamUsageWinsOverEstimate(t *testing.T) {
	decoded, err := decodeImageResponse([]byte(`{"created":1,"data":[{"b64_json":"aGVsbG8="}],
		"usage":{"prompt_tokens":5,"completion_tokens":900,"total_tokens":905}}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	decoded.fallbackUsage = &dto.Usage{PromptTokens: 20, CompletionTokens: 1056, TotalTokens: 1076}

	usage := decoded.billedUsage()
	if usage.PromptTokens != 5 || usage.CompletionTokens != 900 || usage.TotalTokens != 905 {
		t.Fatalf("usage = %+v, want the upstream numbers", usage)
	}
}

// Gemini's totalTokenCount includes thoughts; the estimate must not paper over
// a real usage object that only carries totals.
func TestUpstreamGeminiUsageWinsOverEstimate(t *testing.T) {
	body := `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}]},"finishReason":"STOP"}],
		"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1120,"totalTokenCount":1649,"thoughtsTokenCount":526}}`
	decoded, err := decodeGeminiImageResponse([]byte(body), 0)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	decoded.fallbackUsage = &dto.Usage{PromptTokens: 10, CompletionTokens: 1290, TotalTokens: 1300}

	usage := decoded.billedUsage()
	if usage.CompletionTokens != 1646 || usage.TotalTokens != 1649 {
		t.Fatalf("usage = %+v, want the upstream numbers", usage)
	}
}
