package trace

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// sink is a fake Langfuse OTLP endpoint. Records the last request body and
// the auth headers it saw.
type sink struct {
	mu      sync.Mutex
	bodies  [][]byte
	headers []http.Header
	server  *httptest.Server
}

func newSink(t *testing.T) *sink {
	t.Helper()
	s := &sink{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.bodies = append(s.bodies, body)
		s.headers = append(s.headers, r.Header.Clone())
		s.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *sink) waitSpans(t *testing.T, n int, timeout time.Duration) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		total := 0
		var spans []map[string]any
		for _, b := range s.bodies {
			spans = append(spans, decodeSpans(t, b)...)
			total = len(spans)
		}
		s.mu.Unlock()
		if total >= n {
			return spans
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d spans", n)
	return nil
}

func (s *sink) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bodies)
}

// TestEnabledEndToEnd is the minimal enabled-state e2e required by the
// decoupling rules: with LANGFUSE_ENABLED semantics active, one full relay
// lifecycle (begin -> stream frames -> usage -> finalize) must produce one
// span on the OTLP endpoint whose trace id matches TraceID(request_id) and
// whose input/output carry the captured content.
func TestEnabledEndToEnd(t *testing.T) {
	s := newSink(t)
	conf := testConfig(s.server.URL)
	conf.FlushIntervalMS = 30
	setConfig(conf)

	c := newTestContext(t)
	c.Request.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	Begin(c)

	// Stream frames through the wrapped handler.
	h := func(data string, sr any) {}
	wrapped := WrapStreamHandler(c, h)
	wrapped(`{"choices":[{"delta":{"content":"你"}}]}`, nil)
	wrapped(`{"choices":[{"delta":{"content":"好"}}]}`, nil)

	FillUsage(c, Usage{PromptTokens: 10, CompletionTokens: 2, Quota: 300, TokenName: "prod-key-01"})
	FinalizeRelay(c, RelayMeta{
		UserId: 7, Model: "gpt-4o-mini", Group: "default",
		TokenId: 3, ChannelId: 12, IsStream: true, Format: "openai",
		Operation: "chat", Success: true,
		StartUnixNano: time.Now().Add(-2 * time.Second).UnixNano(),
		FirstByteNano: time.Now().Add(-1800 * time.Millisecond).UnixNano(),
	})

	spans := s.waitSpans(t, 1, 3*time.Second)
	sp := spans[0]

	// Client traceparent wins over the derived id, and parentSpanId keeps
	// the span attached to the client's tree.
	if sp["traceId"] != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("client trace id must be adopted: %v", sp["traceId"])
	}
	if sp["parentSpanId"] != "00f067aa0ba902b7" {
		t.Fatalf("parent span id: %v", sp["parentSpanId"])
	}
	out, _ := attrValue(t, sp, "langfuse.observation.output").(string)
	if out == "" || !strings.Contains(out, "你") || !strings.Contains(out, "好") {
		t.Fatalf("output must carry stream frames: %q", out)
	}
	if attrValue(t, sp, "apiduo.token_name") != "prod-key-01" {
		t.Fatalf("token name: %v", attrValue(t, sp, "apiduo.token_name"))
	}
	if !hasAttr(t, sp, "apiduo.ttft_ms") {
		t.Fatal("ttft must be recorded")
	}

	// Auth headers: Basic pk:sk + ingestion version 4.
	s.mu.Lock()
	hdrs := s.headers
	s.mu.Unlock()
	if len(hdrs) == 0 {
		t.Fatal("no requests hit the sink")
	}
	auth := hdrs[0].Get("Authorization")
	if auth == "" || !strings.Contains(auth, "Basic ") {
		t.Fatalf("basic auth missing: %q", auth)
	}
	if hdrs[0].Get("x-langfuse-ingestion-version") != "4" {
		t.Fatal("ingestion version header missing")
	}
}

// TestFallbackEmitsWithoutCollector covers the model/log.go path: no relay
// collector (async task settlement), so the fallback emits a metadata span
// derived from the request id.
func TestFallbackEmitsWithoutCollector(t *testing.T) {
	s := newSink(t)
	conf := testConfig(s.server.URL)
	conf.FlushIntervalMS = 30
	setConfig(conf)

	RecordConsume(RecordConsumeInput{
		RequestID: "task-req-1", UserId: 7, Model: "seedance-1",
		Group: "default", TokenName: "k", ChannelId: 5,
		PromptTokens: 100, CompletionTokens: 0, Quota: 500,
		UseTimeSec: 120, IsStream: false, Operation: "task",
	})

	spans := s.waitSpans(t, 1, 3*time.Second)
	sp := spans[0]
	if sp["traceId"] != TraceID("task-req-1") {
		t.Fatalf("fallback trace id must derive from request id: %v", sp["traceId"])
	}
	if _, ok := sp["parentSpanId"]; ok {
		t.Fatal("fallback spans have no parent")
	}
	if attrValue(t, sp, "gen_ai.operation.name") != "task" {
		t.Fatalf("operation: %v", attrValue(t, sp, "gen_ai.operation.name"))
	}
}

// TestQueueFullDropsFast proves the no-pushback contract: with a tiny
// content queue and no consumer drain (sink that blocks), enqueueing must
// not block and drops must be counted.
func TestQueueFullDropsFast(t *testing.T) {
	release := make(chan struct{})
	var served sync.WaitGroup
	served.Add(1)
	blocking := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served.Wait() // hold the worker's HTTP call until released
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(func() {
		close(release)
		blocking.Close()
	})
	go func() { <-release; served.Done() }()

	conf := testConfig(blocking.URL)
	conf.FlushIntervalMS = 1
	conf.QueueMeta = 2
	conf.QueueContent = 2
	setConfig(conf)

	start := time.Now()
	for range 50 {
		RecordConsume(RecordConsumeInput{RequestID: "r", Model: "m", Quota: 1})
	}
	elapsed := time.Since(start)
	// 50 non-blocking sends must finish far under any network timeout.
	if elapsed > time.Second {
		t.Fatalf("enqueue blocked: %v", elapsed)
	}
	meta, _ := DroppedCounts()
	if meta == 0 {
		t.Fatal("drops must be counted when the queue is full")
	}
}

// TestUnsampledLogHasNoTraceID pins the log↔trace invariant: when sampling
// drops a request, the log must not carry a trace id. Writing one anyway
// (the pre-fix behaviour) left the console rendering "View trace" links
// that 404 for every unsampled request — at a 10% rate, nine out of ten.
// Failures are the exception: the error rate may still promote them, and a
// missing link beats a missing trace.
func TestUnsampledLogHasNoTraceID(t *testing.T) {
	s := newSink(t)
	conf := testConfig(s.server.URL)
	conf.SampleRate = 0
	conf.ErrorSampleRate = 0
	conf.FlushIntervalMS = 30
	setConfig(conf)

	c := newTestContext(t)
	Begin(c)
	if id := FillUsage(c, Usage{PromptTokens: 1}); id != "" {
		t.Fatalf("unsampled request must not report a trace id, got %q", id)
	}
	FinalizeRelay(c, RelayMeta{Model: "m", Success: true, Operation: "chat"})
	time.Sleep(120 * time.Millisecond)
	if got := s.requestCount(); got != 0 {
		t.Fatalf("unsampled request must not be exported, got %d requests", got)
	}

	// The same verdict must hold for the fallback (consume-without-collector)
	// path: the return value is what the log stores.
	if id := RecordConsume(RecordConsumeInput{RequestID: "r-unsampled", Model: "m"}); id != "" {
		t.Fatalf("unsampled consume must not report a trace id, got %q", id)
	}

	// A failure may be promoted by the error rate even when the regular rate
	// dropped it — the span exists, the log link legitimately does not.
	conf.ErrorSampleRate = 100
	setConfig(conf)
	c2 := newTestContext(t)
	Begin(c2)
	if id := FillUsage(c2, Usage{PromptTokens: 1}); id != "" {
		t.Fatalf("unsampled request must not report a trace id, got %q", id)
	}
	FinalizeRelay(c2, RelayMeta{Model: "m", Success: false, Operation: "chat", ErrorStatus: 500})
	spans := s.waitSpans(t, 1, 2*time.Second)
	if len(spans) != 1 {
		t.Fatalf("failed request must still be exported, got %d spans", len(spans))
	}
}

// TestForceSampleKeepsTheSpanItAdvertised pins the error-log path: the id is
// written before the request finalizes, so ForceSample must lock the sample.
// Relying on LANGFUSE_ERROR_SAMPLE_RATE alone would let an operator lower the
// rate and quietly reintroduce dead links on exactly the rows that matter.
func TestForceSampleKeepsTheSpanItAdvertised(t *testing.T) {
	s := newSink(t)
	conf := testConfig(s.server.URL)
	conf.SampleRate = 0
	conf.ErrorSampleRate = 0
	conf.FlushIntervalMS = 30
	setConfig(conf)

	c := newTestContext(t)
	Begin(c)
	id := ForceSample(c)
	if id == "" {
		t.Fatal("ForceSample must return the trace id it locked")
	}
	// Even a request that ends successfully must now be exported: the id is
	// already on a log row, so the span has to exist.
	FinalizeRelay(c, RelayMeta{Model: "m", Success: true, Operation: "chat"})
	spans := s.waitSpans(t, 1, 2*time.Second)
	if got, _ := spans[0]["traceId"].(string); got != id {
		t.Fatalf("exported span must carry the advertised id: want %q got %q", id, got)
	}
	if id2 := ForceSample(c); id2 != "" {
		t.Fatalf("ForceSample must not resurrect a finalized request, got %q", id2)
	}
}

// TestSamplingRates pins the counter-based sampling decision.
func TestSamplingRates(t *testing.T) {
	conf := &Config{SampleRate: 0, ErrorSampleRate: 100}
	sampled := 0
	for range 100 {
		if shouldSample(conf, true) {
			sampled++
		}
	}
	if sampled != 0 {
		t.Fatalf("rate 0 must never sample, got %d", sampled)
	}
	sampled = 0
	for range 100 {
		if shouldSample(conf, false) {
			sampled++
		}
	}
	if sampled != 100 {
		t.Fatalf("error rate 100 must always sample, got %d", sampled)
	}

	conf.SampleRate = 10
	sampled = 0
	for range 1000 {
		if shouldSample(conf, true) {
			sampled++
		}
	}
	if sampled < 50 || sampled > 150 {
		t.Fatalf("rate 10 should sample ~100/1000, got %d", sampled)
	}
}

// TestDisabledEndToEnd proves behavior equivalence: with the feature off,
// a full lifecycle produces zero OTLP requests.
func TestDisabledEndToEnd(t *testing.T) {
	s := newSink(t)
	setConfig(&Config{Enabled: false, Host: s.server.URL})

	c := newTestContext(t)
	Begin(c)
	h := func(data string, sr any) {}
	WrapStreamHandler(c, h)("frame", nil)
	FillUsage(c, Usage{PromptTokens: 1})
	FinalizeRelay(c, RelayMeta{Model: "m", Success: true})
	RecordConsume(RecordConsumeInput{RequestID: "r", Model: "m"})

	time.Sleep(150 * time.Millisecond)
	if s.requestCount() != 0 {
		t.Fatalf("disabled path must send nothing, sent %d", s.requestCount())
	}
}

// TestBatchingUsesOneRequest asserts spans accumulate into a single OTLP
// request while the window is short and spans are few.
func TestBatchingUsesOneRequest(t *testing.T) {
	s := newSink(t)
	conf := testConfig(s.server.URL)
	conf.FlushIntervalMS = 80
	setConfig(conf)

	for range 5 {
		RecordConsume(RecordConsumeInput{RequestID: "batch", Model: "m"})
	}
	spans := s.waitSpans(t, 5, 3*time.Second)
	if len(spans) != 5 {
		t.Fatalf("want 5 spans, got %d", len(spans))
	}
	s.mu.Lock()
	requests := len(s.bodies)
	s.mu.Unlock()
	if requests == 0 {
		t.Fatal("at least one request expected")
	}
}

// hasAttr reports whether the span carries the key at all (int/bool/string
// values live under different value fields).
func hasAttr(t *testing.T, span map[string]any, key string) bool {
	t.Helper()
	attrs, _ := span["attributes"].([]any)
	for _, a := range attrs {
		m, _ := a.(map[string]any)
		if m["key"] == key {
			return true
		}
	}
	return false
}

// TestLogTraceIdMatchesEmittedSpan pins the front-back contract: the id
// written into the log's admin metadata (which the web console turns into a
// jump link) must equal the id of the span actually exported. A mismatch
// means every link 404s in Langfuse. Covers both traceparent-present (the
// client's trace id wins) and derived (sha256 of the request id) cases.
func TestLogTraceIdMatchesEmittedSpan(t *testing.T) {
	s := newSink(t)
	conf := testConfig(s.server.URL)
	conf.FlushIntervalMS = 30
	setConfig(conf)

	// Case 1: no traceparent — derived id.
	c1 := newTestContext(t)
	Begin(c1)
	logId1 := TraceIDForLog(c1, c1.GetString("X-Oneapi-Request-Id"))
	FinalizeRelay(c1, RelayMeta{Model: "m", Success: true, StartUnixNano: 1})

	// Case 2: client traceparent — the client's trace id must win, both in
	// the span and in the id the log would carry.
	c2 := newTestContext(t)
	c2.Request.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	Begin(c2)
	logId2 := TraceIDForLog(c2, c2.GetString("X-Oneapi-Request-Id"))
	FinalizeRelay(c2, RelayMeta{Model: "m", Success: true, StartUnixNano: 1})

	spans := s.waitSpans(t, 2, 3*time.Second)
	byId := map[string]map[string]any{}
	for _, sp := range spans {
		id, _ := sp["traceId"].(string)
		byId[id] = sp
	}
	if _, ok := byId[logId1]; !ok {
		t.Fatalf("derived log id %s has no matching span (have %v)", logId1, keysOf(byId))
	}
	if logId2 != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("traceparent must override the log id, got %s", logId2)
	}
	if _, ok := byId[logId2]; !ok {
		t.Fatalf("traceparent log id %s has no matching span (have %v)", logId2, keysOf(byId))
	}
}

func keysOf(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// stubBody mimics common.BodyStorage for the request-body read. The trace
// package deliberately depends on the shape, not the type.
type stubBody struct{ data []byte }

func (s stubBody) Bytes() ([]byte, error) { return s.data, nil }

// TestFailedRequestKeepsInputAndLevel pins two things a failure span must
// carry, both of which were missing before local e2e caught them:
//
//  1. the request body — a failed request is exactly when "what did we send
//     upstream" matters, and it must not be gated on usage being settled;
//  2. level=ERROR + status_message, otherwise Langfuse renders the span as a
//     healthy one and the reason is invisible.
func TestFailedRequestKeepsInputAndLevel(t *testing.T) {
	s := newSink(t)
	conf := testConfig(s.server.URL)
	conf.FlushIntervalMS = 30
	setConfig(conf)

	c := newTestContext(t)
	c.Set(ginKeyBodyStorage, stubBody{data: []byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"fail please"}]}`)})
	Begin(c)
	// No FillUsage: a request that failed before settlement has no usage.
	FinalizeRelay(c, RelayMeta{
		UserId: 7, Model: "gpt-4o-mini", Group: "default",
		TokenId: 3, ChannelId: 12, IsStream: true, Format: "openai",
		Operation: "chat", Success: false, ErrorStatus: 500,
		ErrorText:     "upstream returned HTTP 500",
		StartUnixNano: time.Now().Add(-time.Second).UnixNano(),
	})

	spans := s.waitSpans(t, 1, 3*time.Second)
	sp := spans[0]

	in, _ := attrValue(t, sp, "langfuse.observation.input").(string)
	if !strings.Contains(in, "fail please") {
		t.Fatalf("failed request must keep the request body, got %q", in)
	}
	if lvl, _ := attrValue(t, sp, "langfuse.observation.level").(string); lvl != "ERROR" {
		t.Fatalf("failed span must be level ERROR, got %q", lvl)
	}
	msg, _ := attrValue(t, sp, "langfuse.observation.status_message").(string)
	if !strings.Contains(msg, "500") {
		t.Fatalf("status message must carry the reason, got %q", msg)
	}
	status, _ := sp["status"].(map[string]any)
	if status == nil || status["code"] != float64(2) {
		t.Fatalf("OTLP status must be error: %v", sp["status"])
	}
	if _, ok := status["message"].(string); !ok {
		t.Fatalf("OTLP status must carry a message: %v", status)
	}
	// A failed request has no usage; the queue must still treat it as
	// content (the body), not metadata.
	if !strings.Contains(in, `"model"`) {
		t.Fatal("input must be the raw request body")
	}
}

// Queue routing follows the bytes actually carried: a failed request with a
// body must land on the content queue even without usage. The metadata queue
// is made unusable (depth 0) so a span routed there would be dropped and the
// sink would stay empty.
func TestHasContentFollowsBytes(t *testing.T) {
	s := newSink(t)
	conf := testConfig(s.server.URL)
	conf.FlushIntervalMS = 30
	conf.QueueMeta = 0
	conf.QueueContent = 8
	setConfig(conf)

	c := newTestContext(t)
	c.Set(ginKeyBodyStorage, stubBody{data: []byte(`{"messages":[]}`)})
	Begin(c)
	FinalizeRelay(c, RelayMeta{
		Model: "m", Success: false, ErrorStatus: 502,
		StartUnixNano: time.Now().UnixNano(),
	})
	if spans := s.waitSpans(t, 1, 3*time.Second); len(spans) != 1 {
		t.Fatalf("expected one span on the content queue, got %d", len(spans))
	}
}

// TestNonStreamResponseBodyIsCaptured pins the defect a real-Langfuse run
// found: in headtail mode outputBytes only looked at the SSE frame window, so
// a non-stream completion (which lives in respBody) produced a span with
// input but no output — the trace looked like the model answered nothing.
func TestNonStreamResponseBodyIsCaptured(t *testing.T) {
	s := newSink(t)
	conf := testConfig(s.server.URL)
	conf.FlushIntervalMS = 30
	// Wide window: this test is about the carrier, not the truncation math.
	// testConfig defaults to head 64 / tail 32, which would cut this body in
	// half and make a correct capture look broken.
	conf.HeadBytes = 4096
	conf.TailBytes = 4096
	setConfig(conf)

	c := newTestContext(t)
	Begin(c)
	body := []byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"北京明天多云转晴。"}}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`)
	CaptureResponseBody(c, body)
	FinalizeRelay(c, RelayMeta{
		Model: "gpt-4o-mini", Success: true, Format: "openai", Operation: "chat",
		StartUnixNano: time.Now().UnixNano(),
	})

	spans := s.waitSpans(t, 1, 3*time.Second)
	out, _ := attrValue(t, spans[0], "langfuse.observation.output").(string)
	if !strings.Contains(out, "北京明天多云转晴") {
		t.Fatalf("non-stream body must reach the span output, got %q", out)
	}
	if strings.Contains(out, "tool_calls") {
		t.Fatalf("a completion must not be shaped like a parsed stream: %q", out)
	}
	if hasAttr(t, spans[0], "apiduo.output_truncated") {
		if v, _ := attrValue(t, spans[0], "apiduo.output_truncated").(bool); v {
			t.Fatal("wide window must not mark the body truncated")
		}
	}
}

// A stream that produced frames must NOT fall back to respBody, and a
// non-stream request that never wrote a body must stay empty rather than
// emitting something invented.
func TestOutputCarrierIsNotMixed(t *testing.T) {
	h := func(data string, sr any) {}
	setConfig(testConfig("http://unused"))
	c := newTestContext(t)
	Begin(c)
	wrapped := WrapStreamHandler(c, h)
	wrapped(`{"choices":[{"delta":{"content":"流"}}]}`, nil)
	CaptureResponseBody(c, []byte(`{"stray":"body"}`))

	col := fromContext(c)
	out := string(col.outputBytes())
	if strings.Contains(out, "stray") {
		t.Fatalf("respBody must not leak into a stream's output: %q", out)
	}
	if !strings.Contains(out, "流") {
		t.Fatalf("frames must win for streams: %q", out)
	}
}
