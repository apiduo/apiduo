package trace

import (
	"encoding/json"
	"strings"
	"testing"
)

// The OTLP body must be valid JSON with the fields Langfuse maps. Parse it
// back with encoding/json rather than string-matching, so formatting
// changes fail loudly only when structure changes.
func decodeSpans(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var root struct {
		ResourceSpans []struct {
			Resource struct {
				Attributes []struct {
					Key   string `json:"key"`
					Value struct {
						StringValue string `json:"stringValue"`
					} `json:"value"`
				} `json:"attributes"`
			} `json:"resource"`
			ScopeSpans []struct {
				Spans []map[string]any `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	}
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatalf("OTLP body is not valid JSON: %v\n%s", err, body)
	}
	if len(root.ResourceSpans) == 0 || len(root.ResourceSpans[0].ScopeSpans) == 0 {
		t.Fatal("missing resourceSpans/scopeSpans")
	}
	var spans []map[string]any
	for _, rs := range root.ResourceSpans {
		for _, ss := range rs.ScopeSpans {
			spans = append(spans, ss.Spans...)
		}
	}
	return spans
}

func attrValue(t *testing.T, span map[string]any, key string) any {
	t.Helper()
	attrs, _ := span["attributes"].([]any)
	for _, a := range attrs {
		m, _ := a.(map[string]any)
		if m["key"] == key {
			v, _ := m["value"].(map[string]any)
			return v["stringValue"]
		}
	}
	return nil
}

func TestBuildExportBodyStructure(t *testing.T) {
	sn := &Snapshot{
		TraceID:          "d60b7f3c843d6a7b9e3a12bf6a942c4d",
		SpanID:           "11d6a94a83f924a2",
		ParentSpanID:     "00f067aa0ba902b7",
		RequestID:        "req-1",
		Operation:        "chat",
		Name:             "chat gpt-4o-mini",
		Model:            "gpt-4o-mini",
		Group:            "default",
		TokenName:        "prod-key-01",
		Format:           "openai",
		UserId:           7,
		ChannelId:        12,
		Success:          true,
		StartUnixNano:    1731030000000000000,
		EndUnixNano:      1731030001830000000,
		TtftMs:           210,
		IsStream:         true,
		PromptTokens:     3240,
		CompletionTokens: 412,
		Quota:            8800,
		Retry:            1,
		HasContent:       true,
		Input:            []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
		Output:           []byte("data: {\"delta\":\"hello\"}"),
		OutputTruncated:  true,
		OutputDropped:    49000,
	}
	spans := decodeSpans(t, BuildExportBody(&Config{Redact: true}, []*Snapshot{sn}))
	if len(spans) != 1 {
		t.Fatalf("want 1 span, got %d", len(spans))
	}
	sp := spans[0]
	if sp["traceId"] != sn.TraceID || sp["spanId"] != sn.SpanID || sp["parentSpanId"] != sn.ParentSpanID {
		t.Fatalf("ids wrong: %v", sp)
	}
	if sp["name"] != "chat gpt-4o-mini" {
		t.Fatalf("name: %v", sp["name"])
	}
	if attrValue(t, sp, "langfuse.observation.type") != "generation" {
		t.Fatal("observation type must be generation")
	}
	if attrValue(t, sp, "gen_ai.request.model") != "gpt-4o-mini" {
		t.Fatal("model attr missing")
	}
	if attrValue(t, sp, "langfuse.user.id") != "7" {
		t.Fatalf("user id: %v", attrValue(t, sp, "langfuse.user.id"))
	}
	if attrValue(t, sp, "langfuse.observation.input") != `{"messages":[{"role":"user","content":"hi"}]}` {
		t.Fatalf("input: %v", attrValue(t, sp, "langfuse.observation.input"))
	}
	if attrValue(t, sp, "gen_ai.completion") != "data: {\"delta\":\"hello\"}" {
		t.Fatal("completion alias missing")
	}
	status, _ := sp["status"].(map[string]any)
	if status["code"] != float64(1) {
		t.Fatalf("success status must be OK(1): %v", status)
	}
}

func TestBuildExportBodyFailedStatusAndNoParent(t *testing.T) {
	sn := &Snapshot{
		TraceID: TraceID("req-f"),
		SpanID:  SpanID("req-f"),
		Name:    "chat gpt-4o",
		Model:   "gpt-4o",
		Success: false,
	}
	body := BuildExportBody(&Config{Redact: true}, []*Snapshot{sn})
	if strings.Contains(string(body), `"parentSpanId"`) {
		t.Fatal("no parentSpanId key when parent is empty")
	}
	spans := decodeSpans(t, body)
	status, _ := spans[0]["status"].(map[string]any)
	if status["code"] != float64(2) {
		t.Fatalf("failure status must be ERROR(2): %v", status)
	}
}

func TestPayloadEscapesControlChars(t *testing.T) {
	sn := &Snapshot{
		TraceID:    TraceID("req-c"),
		SpanID:     SpanID("req-c"),
		Name:       "chat m",
		Model:      "m",
		HasContent: true,
		Output:     []byte("line1\nline2\x02ctl"),
	}
	// \x02 must be escaped or the whole payload becomes invalid JSON.
	decodeSpans(t, BuildExportBody(&Config{Redact: true}, []*Snapshot{sn}))
}

// Only langfuse.{trace,observation}.metadata.* are filterable in Langfuse;
// bare keys end up in metadata.attributes and cannot be queried. Ops filters
// by channel/group/key, so those must be exported under the filterable
// prefix (plus the observation model name, which Langfuse prefers over the
// gen_ai.* fallbacks).
func TestFilterableMetadataAttributes(t *testing.T) {
	body := string(BuildExportBody(&Config{Redact: true}, []*Snapshot{{
		TraceID: "11111111111111111111111111111111", SpanID: "2222222222222222",
		RequestID: "req-1", Model: "gpt-4o-mini", Group: "vip", TokenName: "prod-key",
		Format: "openai", ChannelId: 7, Success: true,
		StartUnixNano: 1, EndUnixNano: 2,
	}}))
	for _, key := range []string{
		"langfuse.trace.metadata.request_id",
		"langfuse.trace.metadata.channel_id",
		"langfuse.trace.metadata.group",
		"langfuse.trace.metadata.token_name",
		"langfuse.observation.model.name",
	} {
		if !strings.Contains(body, key) {
			t.Fatalf("payload must carry %s for Langfuse filtering", key)
		}
	}
	if strings.Contains(body, `"langfuse.trace.metadata.channel_id","value":{"stringValue":""`) {
		t.Fatal("channel id must not be emitted empty")
	}
}
