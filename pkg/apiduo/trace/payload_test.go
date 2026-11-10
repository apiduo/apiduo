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

// attrRaw returns the whole OTLP value object. Array-typed attributes need
// it: attrValue only unwraps stringValue and would report nil for tags.
func attrRaw(t *testing.T, span map[string]any, key string) any {
	t.Helper()
	attrs, _ := span["attributes"].([]any)
	for _, a := range attrs {
		m, _ := a.(map[string]any)
		if m["key"] == key {
			return m["value"]
		}
	}
	return nil
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

// Langfuse v4 keeps trace-level context (release, environment) on every
// observation row, which is what makes them filter columns. They are not
// metadata: without these attributes the UI cannot slice by gateway build.
func TestV4TraceLevelAttributes(t *testing.T) {
	prev := buildRelease.Load()
	defer buildRelease.Store(prev)

	buildRelease.Store(nil)
	conf := &Config{Environment: "production"}
	body := BuildExportBody(conf, []*Snapshot{{TraceID: "aa", SpanID: "bb", Name: "chat m"}})
	if got := attrValue(t, decodeSpans(t, body)[0], "langfuse.environment"); got != "production" {
		t.Fatalf("environment = %v, want production", got)
	}
	if got := attrValue(t, decodeSpans(t, body)[0], "langfuse.release"); got != nil {
		t.Fatalf("release must be omitted when no build stamp and no env pin, got %v", got)
	}

	buildRelease.Store(ptrString("v0.0.42"))
	body = BuildExportBody(conf, []*Snapshot{{TraceID: "aa", SpanID: "bb", Name: "chat m"}})
	if got := attrValue(t, decodeSpans(t, body)[0], "langfuse.release"); got != "v0.0.42" {
		t.Fatalf("release = %v, want build stamp v0.0.42", got)
	}

	// An explicit pin outranks the build stamp: an operator who set
	// LANGFUSE_RELEASE means that value even if the binary says otherwise.
	pinned := &Config{Environment: "staging", Release: "canary-7"}
	body = BuildExportBody(pinned, []*Snapshot{{TraceID: "aa", SpanID: "bb", Name: "chat m"}})
	if got := attrValue(t, decodeSpans(t, body)[0], "langfuse.release"); got != "canary-7" {
		t.Fatalf("release = %v, want pinned canary-7", got)
	}
}

// Trace tags are the only groupable dimension Langfuse exposes for a span:
// langfuse.trace.metadata.* can be filtered but never grouped, so "p95 per
// channel" is not expressible without them. The OTLP value type matters —
// a langfuse.trace.tags sent as a plain string is accepted silently and the
// trace simply never groups.
func TestTraceTagsAreGroupable(t *testing.T) {
	tagsOf := func(t *testing.T, sn *Snapshot) []string {
		t.Helper()
		raw, _ := attrRaw(t, decodeSpans(t, BuildExportBody(&Config{Redact: true}, []*Snapshot{sn}))[0],
			"langfuse.trace.tags").(map[string]any)
		arr, _ := raw["arrayValue"].(map[string]any)
		values, _ := arr["values"].([]any)
		var out []string
		for _, v := range values {
			if m, ok := v.(map[string]any); ok {
				if s, ok := m["stringValue"].(string); ok {
					out = append(out, s)
				}
			}
		}
		return out
	}

	got := tagsOf(t, &Snapshot{
		TraceID: "aa", SpanID: "bb", Name: "chat m", Model: "m",
		ChannelId: 7, Group: "vip", IsStream: true,
	})
	for _, want := range []string{"channel:7", "group:vip", "stream"} {
		found := false
		for _, g := range got {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("tags %v must contain %q", got, want)
		}
	}

	// A request that never reached channel selection must not emit an empty
	// "channel:" label: every such request would collapse into one fake
	// channel bucket and pollute its percentiles.
	for _, tag := range tagsOf(t, &Snapshot{TraceID: "cc", SpanID: "dd", Name: "chat m"}) {
		if strings.HasPrefix(tag, "channel:") || strings.HasPrefix(tag, "group:") {
			t.Fatalf("unexpected channel/group tag %q on a bare snapshot", tag)
		}
	}
}

func ptrString(s string) *string { return &s }

// TTFT already exists as a custom apiduo.ttft_ms attribute, but only
// langfuse.observation.completion_start_time reaches the UI's time-to-first-
// token field. Sending just the custom key meant the number was collected and
// then never shown anywhere a human looks.
func TestCompletionStartTimeIsOfficialField(t *testing.T) {
	start := int64(1_700_000_000_000_000_000) // 2023-11-14T22:13:20Z
	sn := &Snapshot{
		TraceID:       "aa",
		SpanID:        "bb",
		Name:          "chat m",
		StartUnixNano: start,
		EndUnixNano:   start + int64(3e9),
		TtftMs:        1250,
	}
	span := decodeSpans(t, BuildExportBody(&Config{}, []*Snapshot{sn}))[0]
	got := attrValue(t, span, "langfuse.observation.completion_start_time")
	if got != "2023-11-14T22:13:21.250Z" {
		t.Fatalf("completion_start_time = %v, want 2023-11-14T22:13:21.250Z", got)
	}

	// No TTFT measured (non-stream, or failed before the first byte): the
	// attribute must be absent rather than "start time", or the UI reports a
	// zero TTFT that looks like a measurement.
	sn.TtftMs = 0
	span = decodeSpans(t, BuildExportBody(&Config{}, []*Snapshot{sn}))[0]
	if got := attrValue(t, span, "langfuse.observation.completion_start_time"); got != nil {
		t.Fatalf("completion_start_time must be omitted without TTFT, got %v", got)
	}
}

// The v4 defaults reach the exporter through loadFromEnv, so the console, the
// environment and the emitted attributes cannot disagree.
func TestEnvironmentDefaultsFromEnv(t *testing.T) {
	t.Setenv("LANGFUSE_ENVIRONMENT", "")
	if got := loadFromEnv().Environment; got != "production" {
		t.Fatalf("default environment = %q, want production", got)
	}
	t.Setenv("LANGFUSE_ENVIRONMENT", "e2e")
	if got := loadFromEnv().Environment; got != "e2e" {
		t.Fatalf("environment = %q, want e2e", got)
	}
	t.Setenv("LANGFUSE_RELEASE", "pinned")
	if got := loadFromEnv().Release; got != "pinned" {
		t.Fatalf("release = %q, want pinned", got)
	}
}
