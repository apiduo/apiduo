package helper

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func decodeObject(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal(payload, &obj); err != nil {
		t.Fatalf("decode rewritten payload: %v", err)
	}
	return obj
}

func TestRewriteUpstreamIdentityRewritesModelPerProtocol(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		assert  func(t *testing.T, obj map[string]any)
	}{
		{
			name:    "openai chat completion",
			payload: `{"id":"chatcmpl-1","object":"chat.completion","model":"deepseek-v3-250101","choices":[]}`,
			assert: func(t *testing.T, obj map[string]any) {
				if got := obj["model"]; got != "acme-pro" {
					t.Fatalf("model = %v, want acme-pro", got)
				}
			},
		},
		{
			name:    "claude message start",
			payload: `{"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-5-20250929","content":[]}}`,
			assert: func(t *testing.T, obj map[string]any) {
				message, _ := obj["message"].(map[string]any)
				if got := message["model"]; got != "acme-pro" {
					t.Fatalf("message.model = %v, want acme-pro", got)
				}
			},
		},
		{
			name:    "gemini model version",
			payload: `{"candidates":[],"modelVersion":"gemini-2.5-pro"}`,
			assert: func(t *testing.T, obj map[string]any) {
				if got := obj["modelVersion"]; got != "acme-pro" {
					t.Fatalf("modelVersion = %v, want acme-pro", got)
				}
			},
		},
		{
			name:    "responses object",
			payload: `{"type":"response.created","response":{"id":"resp_1","model":"gpt-5-2025-08-07"}}`,
			assert: func(t *testing.T, obj map[string]any) {
				response, _ := obj["response"].(map[string]any)
				if got := response["model"]; got != "acme-pro" {
					t.Fatalf("response.model = %v, want acme-pro", got)
				}
			},
		},
		{
			name:    "fingerprint is nulled",
			payload: `{"model":"deepseek-v3","system_fingerprint":"fp_abc123"}`,
			assert: func(t *testing.T, obj map[string]any) {
				if value, exists := obj["system_fingerprint"]; !exists || value != nil {
					t.Fatalf("system_fingerprint = %v (exists=%v), want nil", value, exists)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rewritten, changed := rewriteUpstreamIdentity([]byte(tc.payload), "acme-pro")
			if !changed {
				t.Fatalf("payload was not rewritten")
			}
			tc.assert(t, decodeObject(t, rewritten))
		})
	}
}

func TestRewriteUpstreamIdentityLeavesContentAlone(t *testing.T) {
	payload := `{"model":"deepseek-v3","choices":[{"message":{"content":"the model deepseek-v3 said hi"}}]}`
	rewritten, changed := rewriteUpstreamIdentity([]byte(payload), "acme-pro")
	if !changed {
		t.Fatalf("model field should have been rewritten")
	}
	obj := decodeObject(t, rewritten)
	choices, _ := obj["choices"].([]any)
	first, _ := choices[0].(map[string]any)
	message, _ := first["message"].(map[string]any)
	if got := message["content"]; got != "the model deepseek-v3 said hi" {
		t.Fatalf("content changed to %v", got)
	}
}

func TestRewriteUpstreamIdentityNoopCases(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{name: "no model field", payload: `{"choices":[]}`},
		{name: "model already public", payload: `{"model":"acme-pro"}`},
		{name: "not json", payload: `data: [DONE]`},
		{name: "model is not a string", payload: `{"model":null}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rewritten, changed := rewriteUpstreamIdentity([]byte(tc.payload), "acme-pro")
			if changed {
				t.Fatalf("payload should not be rewritten, got %s", rewritten)
			}
			if string(rewritten) != tc.payload {
				t.Fatalf("payload mutated: %s", rewritten)
			}
		})
	}
}

func TestRewriteSSELinesKeepsEnvelope(t *testing.T) {
	src := "event: message_start\n" +
		`data: {"type":"message_start","message":{"model":"deepseek-v3"}}` + "\n\n" +
		"data: [DONE]\n\n"
	out := string(rewriteSSELines([]byte(src), "acme-pro"))

	for _, want := range []string{"event: message_start\n", `"model":"acme-pro"`, "data: [DONE]\n\n"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q: %q", want, out)
		}
	}
	if strings.Contains(out, "deepseek-v3") {
		t.Fatalf("upstream model leaked: %q", out)
	}
}

func newHardeningContext(t *testing.T, hardening, mapped bool) (*RemapHardeningWriter, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	writer := NewRemapHardeningWriter(c.Writer, func() (string, bool) {
		return "acme-pro", hardening && mapped
	})
	c.Writer = writer
	return writer, rec
}

func TestRemapHardeningWriterTransparentWhenDisabled(t *testing.T) {
	for _, tc := range []struct {
		name      string
		hardening bool
		mapped    bool
	}{
		{name: "flag off", hardening: false, mapped: true},
		{name: "mapping missed", hardening: true, mapped: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writer, rec := newHardeningContext(t, tc.hardening, tc.mapped)
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"model":"deepseek-v3-250101"}`))
			writer.Finalize()
			if !strings.Contains(rec.Body.String(), "deepseek-v3-250101") {
				t.Fatalf("expected untouched body, got %q", rec.Body.String())
			}
		})
	}
}

func TestRemapHardeningWriterRewritesBufferedBody(t *testing.T) {
	writer, rec := newHardeningContext(t, true, true)
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte(`{"object":"chat.completion","model":"deepseek-v3-250101"}`))
	if rec.Body.Len() != 0 {
		t.Fatalf("body reached the client before Finalize: %q", rec.Body.String())
	}
	writer.Finalize()
	body := rec.Body.String()
	if !strings.Contains(body, `"model":"acme-pro"`) {
		t.Fatalf("model not rewritten: %q", body)
	}
	if strings.Contains(body, "deepseek-v3-250101") {
		t.Fatalf("upstream model leaked: %q", body)
	}
}

func TestRemapHardeningWriterRewritesSplitSSE(t *testing.T) {
	writer, rec := newHardeningContext(t, true, true)
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.WriteHeader(http.StatusOK)

	_, _ = writer.Write([]byte(`data: {"model":"deep`))
	if rec.Body.Len() != 0 {
		t.Fatalf("partial event was flushed early: %q", rec.Body.String())
	}
	_, _ = writer.Write([]byte("seek-v3\"}\n\n"))
	writer.Flush()
	writer.Finalize()

	body := rec.Body.String()
	if !strings.Contains(body, `"model":"acme-pro"`) {
		t.Fatalf("model not rewritten: %q", body)
	}
	if strings.Contains(body, "deepseek-v3") {
		t.Fatalf("upstream model leaked: %q", body)
	}
}

func TestRemapHardeningWriterRejectsOversizedBody(t *testing.T) {
	writer, rec := newHardeningContext(t, true, true)
	writer.Header().Set("Content-Type", "application/json")
	writer.status = http.StatusOK
	writer.body.Write(make([]byte, maxBufferedRemapBody))
	_, _ = writer.Write([]byte("x"))
	writer.Finalize()
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "acme-pro") {
		t.Fatalf("oversized path must not forward buffered bytes: %q", rec.Body.String())
	}
}

func TestRemapHardeningWriterCoversGinJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	writer := NewRemapHardeningWriter(c.Writer, func() (string, bool) {
		return "acme-pro", true
	})
	c.Writer = writer

	c.JSON(http.StatusOK, gin.H{"object": "chat.completion", "model": "deepseek-v3-250101"})
	if rec.Body.Len() != 0 {
		t.Fatalf("gin.JSON reached the client before Finalize: %q", rec.Body.String())
	}
	writer.Finalize()

	body := rec.Body.String()
	if !strings.Contains(body, `"model":"acme-pro"`) {
		t.Fatalf("model not rewritten: %q", body)
	}
	if strings.Contains(body, "deepseek-v3-250101") {
		t.Fatalf("upstream model leaked: %q", body)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestRemapHardeningWriterPassesKeepAliveThrough(t *testing.T) {
	writer, rec := newHardeningContext(t, true, true)
	// The ping arrives before any handler set a content type.
	if _, err := writer.Write([]byte(": PING\n\n")); err != nil {
		t.Fatalf("ping write failed: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "PING") {
		t.Fatalf("keep-alive was buffered: %q", rec.Body.String())
	}
	writer.Flush()

	_, _ = writer.Write([]byte("data: {\"model\":\"deepseek-v3\"}\n\n"))
	writer.Finalize()
	body := rec.Body.String()
	if !strings.Contains(body, `"model":"acme-pro"`) {
		t.Fatalf("model not rewritten after ping: %q", body)
	}
	if strings.Contains(body, "deepseek-v3") {
		t.Fatalf("upstream model leaked: %q", body)
	}
}

// newHardeningContextB mirrors newHardeningContext for benchmarks, which take
// *testing.B and cannot call a *testing.T helper.
func newHardeningContextB(b *testing.B, hardening, mapped bool) (*RemapHardeningWriter, *httptest.ResponseRecorder) {
	b.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	writer := NewRemapHardeningWriter(c.Writer, func() (string, bool) {
		return "acme-pro", hardening && mapped
	})
	c.Writer = writer
	return writer, rec
}

func TestRewriteUpstreamIdentityClearsBillingUsage(t *testing.T) {
	// The Claude/Gemini conversion records which dialect produced the usage.
	// It is an object, so nulling has to balance braces rather than quotes.
	payload := []byte(`{"type":"message","model":"deepseek-v3","usage":{"input_tokens":1,"billing_usage":{"source":"oai_chat","semantic":"openai","openai_usage":{"prompt_tokens":1}}}}`)
	out, changed := rewriteUpstreamIdentity(payload, "acme-pro")
	if !changed {
		t.Fatal("expected a rewrite")
	}
	if strings.Contains(string(out), "oai_chat") || strings.Contains(string(out), "openai_usage") {
		t.Fatalf("conversion metadata leaked: %s", out)
	}
	if !strings.Contains(string(out), `"billing_usage":null`) {
		t.Fatalf("billing_usage not nulled: %s", out)
	}
	var probe map[string]any
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatalf("rewritten payload is not valid JSON: %v", err)
	}
	if tokens := probe["usage"].(map[string]any)["input_tokens"]; tokens != float64(1) {
		t.Fatalf("usage member damaged: %v", tokens)
	}
}

func TestRewriteUpstreamIdentityIgnoresKeyNamesInContent(t *testing.T) {
	// A message that merely mentions the key must survive untouched.
	payload := []byte(`{"model":"deepseek-v3","choices":[{"message":{"content":"we log billing_usage: nowhere"}}]}`)
	out, _ := rewriteUpstreamIdentity(payload, "acme-pro")
	if !strings.Contains(string(out), "billing_usage: nowhere") {
		t.Fatalf("content was damaged: %s", out)
	}
}

func TestRewriteUpstreamIdentityClearsUsageProvenance(t *testing.T) {
	// dto.Usage carries three internal markers besides the fingerprint. None is
	// a vendor field; all of them name the dialect behind the numbers.
	payload := []byte(`{"model":"deepseek-v3","usage":{"prompt_tokens":7,"usage_semantic":"anthropic","usage_source":"anthropic","billing_usage":{"source":"claude_messages","semantic":"anthropic"}}}`)
	out, changed := rewriteUpstreamIdentity(payload, "acme-pro")
	if !changed {
		t.Fatal("expected a rewrite")
	}
	text := string(out)
	for _, leaked := range []string{"anthropic", "claude_messages"} {
		if strings.Contains(text, leaked) {
			t.Fatalf("%s leaked: %s", leaked, text)
		}
	}
	if !strings.Contains(text, `"prompt_tokens":7`) {
		t.Fatalf("token count damaged: %s", text)
	}
	var probe map[string]any
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	usage := probe["usage"].(map[string]any)
	for _, key := range []string{"usage_semantic", "usage_source", "billing_usage"} {
		if usage[key] != nil {
			t.Fatalf("%s should be null, got %v", key, usage[key])
		}
	}
}

func TestSendHeaderDropsUpstreamIdentityHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	// Set before the upstream is contacted: the gateway's own headers.
	c.Writer.Header().Set("X-Oneapi-Request-Id", "local-1")
	c.Writer.Header().Set("X-New-Api-Version", "v1")
	c.Writer.Header().Set("X-Ratelimit-Limit-Requests", "10000")

	w := NewRemapHardeningWriter(c.Writer, func() (string, bool) {
		return "acme-pro", true
	})

	// Set by IOCopyBytesGracefully once the vendor replied.
	c.Writer.Header().Set("Server", "BaseHTTP/0.6 Python/3.12.16")
	c.Writer.Header().Set("Openai-Organization", "org-abc123")
	c.Writer.Header().Set("Openai-Processing-Ms", "42")
	c.Writer.Header().Set("X-Request-Id", "req_upstream")
	c.Writer.Header().Set("Set-Cookie", "session=leak")
	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.Header().Set("X-Accel-Buffering", "no")

	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(`{"model":"deepseek-v3"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	w.Finalize()

	headers := rec.Result().Header
	for _, leaked := range []string{"Server", "Openai-Organization", "Openai-Processing-Ms", "X-Request-Id", "Set-Cookie"} {
		if got := headers.Get(leaked); got != "" {
			t.Fatalf("%s leaked to the client: %q", leaked, got)
		}
	}
	if got := headers.Get("X-Oneapi-Request-Id"); got != "local-1" {
		t.Fatalf("gateway request id lost: %q", got)
	}
	if got := headers.Get("X-New-Api-Version"); got != "" {
		t.Fatalf("gateway build string must not be advertised: %q", got)
	}
	if got := headers.Get("X-Ratelimit-Limit-Requests"); got != "10000" {
		t.Fatalf("gateway rate limit header lost: %q", got)
	}
	if got := headers.Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type lost: %q", got)
	}
	if got := headers.Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("stream buffering hint lost: %q", got)
	}
}
