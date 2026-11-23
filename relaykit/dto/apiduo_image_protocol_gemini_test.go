package dto

import "testing"

func TestImageUpstreamGeminiIsValid(t *testing.T) {
	cfg := &ImageProtocolConfig{Enabled: true, Upstream: ImageUpstreamGemini}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("gemini must be a valid upstream: %v", err)
	}
}

func TestIncomingPathLiteralAllowsNamespacedModel(t *testing.T) {
	// 带命名空间的模型名用字面量路径：`{model}` 只表示单段，不能放宽，
	// 否则异步提交路径的同名模板会把 `/a/b` 当成模型名。
	configured := "/v1beta/models/google/gemini-nano-banana-2.1:generateContent"
	if !matchAdvancedCustomIncomingPath(configured, configured) {
		t.Fatal("literal path must match itself")
	}
	if !matchAdvancedCustomIncomingPath(configured, "/v1beta/models/google/gemini-nano-banana-2.1:streamGenerateContent") {
		t.Fatal("stream variant must match via the action suffix")
	}
	if matchAdvancedCustomIncomingPath(configured, "/v1beta/models/google/other:generateContent") {
		t.Fatal("literal path must not match another model")
	}
}
