package trace

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func testConfig(host string) *Config {
	return &Config{
		Enabled:         true,
		Host:            host,
		PublicKey:       "pk-lf-test",
		SecretKey:       "sk-lf-test",
		ProjectID:       "proj-test",
		Policy:          &Policy{Defaults: Rates{Metadata: 100, Content: 100}},
		Location:        time.Local,
		ParentBased:     true,
		ErrorSampleRate: 100,
		BodyMode:        BodyHeadTail,
		HeadBytes:       64,
		TailBytes:       32,
		TimeoutSeconds:  2,
		QueueMeta:       64,
		QueueContent:    16,
		Redact:          true,
	}
}

func newTestContext(t *testing.T) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c := &gin.Context{}
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{}`))
	c.Request.Header.Set("X-Oneapi-Request-Id", "req-1")
	// Begin reads the gin context key (set by middleware.RequestId), not
	// the header; without this the collector is never created.
	c.Set("X-Oneapi-Request-Id", "req-1")
	return c
}

func TestFrameTailKeepsLastWholeFrames(t *testing.T) {
	ft := newFrameTail(10)
	ft.add("aaaa") // 5 bytes
	ft.add("bbbb") // 10 bytes, fits
	if got := string(ft.bytes()); got != "aaaa\nbbbb\n" {
		t.Fatalf("exact fill: %q", got)
	}
	ft.add("cccc") // 15 > 10 -> evict oldest whole frame
	if got := string(ft.bytes()); got != "bbbb\ncccc\n" {
		t.Fatalf("evicted: %q", got)
	}
	if ft.sizeBytes() != 10 {
		t.Fatalf("size: %d", ft.sizeBytes())
	}
	// A single oversized frame is kept whole rather than sliced.
	ft.reset(4)
	ft.add("0123456789")
	if got := string(ft.bytes()); got != "0123456789\n" {
		t.Fatalf("oversized frame must stay whole: %q", got)
	}
}

func TestHeadTailTruncation(t *testing.T) {
	// head(64) fits one 41-byte frame; the second seals the head and moves
	// to the tail window; the third evicts the second (tail budget 32, so
	// only the newest whole frame survives).
	h := func(data string, sr any) {}
	conf := testConfig("http://unused")
	setConfig(conf)
	c := newTestContext(t)
	Begin(c)
	wrapped := WrapStreamHandler(c, h)
	wrapped(strings.Repeat("A", 40), nil)
	wrapped(strings.Repeat("B", 40), nil)
	wrapped(strings.Repeat("C", 40), nil)

	col := fromContext(c)
	out := string(col.outputBytes())
	if !strings.HasPrefix(out, strings.Repeat("A", 40)+"\n") {
		t.Fatalf("head window must keep the first whole frame: %q", out)
	}
	if !strings.Contains(out, strings.Repeat("C", 40)+"\n") {
		t.Fatalf("tail window must keep the last whole frame: %q", out)
	}
	if strings.Contains(out, "B") {
		t.Fatalf("middle frame should have been evicted: %q", out)
	}
	dropped, truncated := col.frameStats(conf)
	if !truncated {
		t.Fatal("must be marked truncated")
	}
	// 3 frames x 41 bytes = 123; kept = head 41 + tail 41 = 82 -> 41 dropped.
	if dropped != 41 {
		t.Fatalf("dropped bytes: %d", dropped)
	}
}

// A frame that alone exceeds the head window must be kept whole in the tail
// rather than sliced — half a JSON object is useless to every parser.
func TestHeadTailKeepsOversizedFrameWhole(t *testing.T) {
	h := func(data string, sr any) {}
	setConfig(testConfig("http://unused"))
	c := newTestContext(t)
	Begin(c)
	wrapped := WrapStreamHandler(c, h)
	big := strings.Repeat("X", 200)
	wrapped(big, nil)

	col := fromContext(c)
	out := string(col.outputBytes())
	if out != big+"\n" {
		t.Fatalf("oversized frame must survive whole, got %d bytes", len(out))
	}
}

func TestWrapResetsOnRetry(t *testing.T) {
	setConfig(testConfig("http://unused"))
	c := newTestContext(t)
	Begin(c)

	h := func(data string, sr any) {}
	w1 := WrapStreamHandler(c, h)
	w1("attempt-one", nil)
	// Second StreamScannerHandler invocation = retry; only the last
	// attempt survives (docs §3.8).
	w2 := WrapStreamHandler(c, h)
	w2("attempt-two", nil)

	col := fromContext(c)
	out := string(col.outputBytes())
	if strings.Contains(out, "attempt-one") {
		t.Fatalf("retry must reset frames, got %q", out)
	}
	if !strings.Contains(out, "attempt-two") {
		t.Fatalf("last attempt missing: %q", out)
	}
}

func TestTruncateCopyHeadTail(t *testing.T) {
	conf := testConfig("http://unused")
	data := []byte(strings.Repeat("a", 100))
	got := truncateCopy(data, conf)
	if len(got) != conf.HeadBytes+conf.TailBytes {
		t.Fatalf("truncated size: %d", len(got))
	}
	if string(got[:conf.HeadBytes]) != strings.Repeat("a", conf.HeadBytes) {
		t.Fatal("head bytes wrong")
	}
	small := []byte("short")
	if got := truncateCopy(small, conf); string(got) != "short" {
		t.Fatal("small payloads must pass through unchanged")
	}
}

func TestRequestBodyClosedDegradesToMetadata(t *testing.T) {
	// The relay defer runs before the middleware cleanup (defer LIFO), so
	// the body is normally alive; if some path closed it first, finalize
	// must degrade to metadata instead of failing.
	setConfig(testConfig("http://unused"))
	c := newTestContext(t)
	Begin(c)

	// No body storage registered at all -> input is nil, no panic.
	FinalizeRelay(c, RelayMeta{Model: "m", Success: true, StartUnixNano: 1})
	if col := fromContext(c); !col.finalized {
		t.Fatal("finalize must run")
	}
}

func TestDisabledPathIsInert(t *testing.T) {
	setConfig(&Config{Enabled: false})
	c := newTestContext(t)
	Begin(c)
	if fromContext(c) != nil {
		t.Fatal("disabled Begin must not create a collector")
	}
	calls := 0
	h := func(data string, sr any) { calls++ }
	wrapped := WrapStreamHandler(c, h)
	wrapped("frame", nil)
	if calls != 1 {
		t.Fatal("wrapper must delegate when disabled")
	}
	FillUsage(c, Usage{PromptTokens: 1})
	FinalizeRelay(c, RelayMeta{})
	CaptureResponseBody(c, []byte("x"))
	RecordConsume(RecordConsumeInput{RequestID: "r"})
	if meta, content := DroppedCounts(); meta != 0 || content != 0 {
		t.Fatalf("disabled path must not enqueue: %d/%d", meta, content)
	}
}

func TestBinaryPlaceholder(t *testing.T) {
	if got := binaryPlaceholder([]byte(`{"a":"中文 ok"}`)); got != `{"a":"中文 ok"}` {
		t.Fatalf("utf-8 json must survive: %q", got)
	}
	if got := binaryPlaceholder([]byte{0x00, 0x01, 0xff}); !strings.HasPrefix(got, "[binary 3 bytes]") {
		t.Fatalf("control bytes must be replaced: %q", got)
	}
}

// Propagate must stay inert while the feature is off — the outbound request
// has to remain byte-identical to upstream new-api in the default state.
func TestPropagateGated(t *testing.T) {
	c := newTestContext(t)
	c.Request.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	c.Request.Header.Set("tracestate", "vendor=1")

	setConfig(&Config{Enabled: false})
	dst := http.Header{}
	Propagate(c, &dst)
	if len(dst) != 0 {
		t.Fatalf("disabled must not touch outbound headers: %v", dst)
	}

	setConfig(testConfig("http://unused"))
	Propagate(c, &dst)
	if dst.Get("traceparent") == "" || dst.Get("tracestate") != "vendor=1" {
		t.Fatalf("enabled must forward W3C headers: %v", dst)
	}
}

// A request without a request id cannot be joined to any log row; emitting a
// span with an empty trace id would poison Langfuse with unlinkable entries.
func TestBeginSkipsWithoutRequestId(t *testing.T) {
	setConfig(testConfig("http://unused"))
	gin.SetMode(gin.TestMode)
	c := &gin.Context{}
	c.Request = httptest.NewRequest("POST", "/", nil)
	Begin(c)
	if fromContext(c) != nil {
		t.Fatal("no request id means no collector")
	}
	if id := TraceIDForLog(c, ""); id != "" {
		t.Fatalf("empty request id must yield empty trace id, got %q", id)
	}
}
