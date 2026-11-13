package trace

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
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

// The cap the console edits must be the one truncation applies — both to a
// one-shot body (image responses go through CaptureResponseBody) and to the
// streaming frame buffer. A regression here is silent: the trace just gets
// smaller than the operator asked for.
func TestFullCapComesFromConfig(t *testing.T) {
	conf := testConfig("http://unused")
	conf.BodyMode = BodyFull
	// The floor applies to the effective cap, so this is also the smallest cap
	// that can be configured at all.
	conf.FullMaxBytes = MinBodyFullBytes

	data := []byte(strings.Repeat("a", 2*MinBodyFullBytes))
	if got := truncateCopy(data, conf); len(got) != MinBodyFullBytes {
		t.Fatalf("full mode must cut at the configured cap, got %d bytes", len(got))
	}

	// Zero is "no opinion" (an old hand-built config), not "keep nothing".
	conf.FullMaxBytes = 0
	if got := truncateCopy(data, conf); len(got) != len(data) {
		t.Fatalf("zero cap must fall back to the default, got %d bytes", len(got))
	}

	// The streaming path uses the same number, and the separator counts against
	// it: the buffer may end at the cap, never one byte past it.
	conf.FullMaxBytes = MinBodyFullBytes
	setConfig(conf)
	c := newTestContext(t)
	Begin(c)
	col := fromContext(c)
	col.frameMode = 2
	// 600 KB frames against a 1 MB cap: the second one crosses it.
	big := []byte(strings.Repeat("b", 600*1024))
	for i := 0; i < 3; i++ {
		col.appendFrame(big, conf)
	}
	if got := len(col.frameFull); got > conf.BodyMaxBytes() {
		t.Fatalf("stream buffer must respect the cap, got %d bytes", got)
	}
	if !col.framesTruncated {
		t.Fatal("crossing the cap must be recorded, not silently dropped")
	}
}

// An image response that blows past the full-mode cap used to be cut in half,
// which broke the JSON and left the externalizer nothing to match — the trace
// held megabytes of dead base64 and the drawer showed no picture at all.
func TestCaptureFullBodyLiftsPictureBeforeTheCap(t *testing.T) {
	png := noisePNG(t, 1024, 1024)
	body := []byte(`{"created":1,"data":[{"b64_json":"` +
		base64.StdEncoding.EncodeToString(png) + `"}]}`)
	conf := &Config{BodyMode: BodyFull, FullMaxBytes: MinBodyFullBytes}
	if len(body) <= conf.BodyMaxBytes() {
		t.Fatalf("fixture must exceed the cap: %d bytes", len(body))
	}

	col := &Collector{frameMode: 2}
	got := col.cacheFullBody(body, conf)

	if len(got) > conf.BodyMaxBytes() {
		t.Fatalf("kept %d bytes, over the %d-byte cap", len(got), conf.BodyMaxBytes())
	}
	if len(col.inlineMedia) != 1 {
		t.Fatalf("lifted %d pictures, want 1", len(col.inlineMedia))
	}
	if !bytes.Equal(col.inlineMedia[0].Raw, png) {
		t.Error("lifted bytes are not the original picture")
	}
	// The whole point: what stays behind is still parseable.
	if !json.Valid(got) {
		t.Errorf("payload is not valid JSON after lifting: %s", clipForLog(got))
	}
	if !bytes.Contains(got, []byte(fmt.Sprintf(inlineMediaPlaceholder, 0))) {
		t.Error("placeholder missing from the payload")
	}
}

func TestCaptureFullBodyCutsTextWithoutPictures(t *testing.T) {
	conf := &Config{BodyMode: BodyFull, FullMaxBytes: MinBodyFullBytes}
	body := bytes.Repeat([]byte("x"), conf.BodyMaxBytes()+4096)

	col := &Collector{frameMode: 2}
	got := col.cacheFullBody(body, conf)

	if len(got) != conf.BodyMaxBytes() {
		t.Fatalf("len = %d, want the cap %d", len(got), conf.BodyMaxBytes())
	}
	if len(col.inlineMedia) != 0 {
		t.Error("nothing should have been lifted out of plain text")
	}
}

func TestLiftInlineImagesIgnoresSmallOrForeignValues(t *testing.T) {
	small := base64.StdEncoding.EncodeToString(noisePNG(t, 8, 8))
	// Large enough to qualify by size, but it decodes to text, not a picture.
	notAPicture := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("hello "), 64<<10))
	body := []byte(`{"a":"` + small + `","b":"` + notAPicture + `"}`)

	slim, blobs := liftInlineImages(body)
	if len(blobs) != 0 {
		t.Fatalf("lifted %d values, want 0", len(blobs))
	}
	if !bytes.Equal(slim, body) {
		t.Error("payload rewritten although nothing qualified")
	}
}

func clipForLog(b []byte) string {
	if len(b) > 120 {
		return string(b[:120]) + "..."
	}
	return string(b)
}

func TestLiftInlineImagesCapsAtTheUploadLimit(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString(noisePNG(t, 320, 320))
	items := make([]string, mediaMaxPerSpan+2)
	for i := range items {
		items[i] = `{"b64_json":"` + b64 + `"}`
	}
	body := []byte(`{"data":[` + strings.Join(items, ",") + `]}`)

	slim, blobs := liftInlineImages(body)

	if len(blobs) != mediaMaxPerSpan {
		t.Fatalf("lifted %d pictures, want the upload cap of %d", len(blobs), mediaMaxPerSpan)
	}
	if !json.Valid(slim) {
		t.Error("payload must stay valid JSON, overflow entries included")
	}
	if !bytes.Contains(slim, []byte("over the per-span upload cap")) {
		t.Error("overflow marker missing — the extra pictures vanished silently")
	}
}

// A trace id written into the usage log is a promise the console renders a
// link against. When the exporter is off there is no span to link to, so the
// fallback must stay silent — otherwise every row written before LANGFUSE_*
// was configured keeps a dead id forever.
func TestTraceIDForLogStaysEmptyWhenExporterIsOff(t *testing.T) {
	disabled := testConfig("http://collector.test")
	disabled.Enabled = false
	noHost := testConfig("")
	for _, tc := range []struct {
		name string
		conf *Config
	}{
		{name: "disabled", conf: disabled},
		{name: "no host", conf: noHost},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setConfig(tc.conf)
			t.Cleanup(func() { setConfig(testConfig("http://collector.test")) })
			if got := TraceIDForLog(newTestContext(t), "req-1"); got != "" {
				t.Fatalf("trace id handed out while exporter is off: %q", got)
			}
		})
	}
}

// With the exporter on, the collector-less fallback (RecordConsume paths that
// never entered Relay) still reports the same id the span will carry.
func TestTraceIDForLogKeepsIDWhenExporterIsOn(t *testing.T) {
	setConfig(testConfig("http://collector.test"))
	t.Cleanup(func() { setConfig(testConfig("http://collector.test")) })
	if got, want := TraceIDForLog(newTestContext(t), "req-1"), TraceID("req-1"); got != want {
		t.Fatalf("fallback id: got %q want %q", got, want)
	}
}
