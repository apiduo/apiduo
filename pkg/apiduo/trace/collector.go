package trace

import (
	"bytes"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// contextKey is the gin.Context key holding the per-request Collector.
// Mirrors the upstream KeyBodyStorage pattern: state lives exactly as long
// as the request.
const contextKey = "apiduo_trace_collector"

// ginKeyBodyStorage duplicates common.KeyBodyStorage's literal so this
// package stays free of internal imports. bodyBytes is satisfied
// structurally by both memoryStorage and diskStorage.
const ginKeyBodyStorage = "key_body_storage"

type bodyBytes interface {
	Bytes() ([]byte, error)
}

// Usage is the settlement-time data filled by service/text_quota.go. Plain
// fields because the bridge translates from unexported service types.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	Quota            int
	UseTimeSec       int
	TokenName        string
}

// RelayMeta is the request-boundary data passed by controller/relay.go's
// defer. Built by the controller bridge from *relaycommon.RelayInfo, which
// this package must not import.
type RelayMeta struct {
	UserId        int
	Model         string
	Group         string
	TokenId       int
	TokenName     string
	ChannelId     int
	IsStream      bool
	Retry         int
	Format        string
	Operation     string
	StartUnixNano int64
	FirstByteNano int64 // 0 when the request never produced a first byte
	Success       bool
	ErrorStatus   int
	ErrorText     string // masked failure reason; truncated by FinalizeRelay
}

// RecordConsumeInput is the fallback-path payload built by the model bridge
// for log writes that happen outside any relay request (async task
// settlement, MJ, violation fees).
type RecordConsumeInput struct {
	RequestID        string
	UserId           int
	Model            string
	Group            string
	TokenName        string
	ChannelId        int
	PromptTokens     int
	CompletionTokens int
	Quota            int
	UseTimeSec       int
	IsStream         bool
	Operation        string
	UnixNano         int64
}

// Collector is the per-request state machine. All hooks write into it; only
// the relay defer reads it back (once) to build the Snapshot. Not
// goroutine-safe beyond the guarantees documented per method: stream frames
// arrive sequentially under the scanner's write mutex, usage is filled once
// before the defer runs, and finalize runs exactly once.
type Collector struct {
	traceID      string
	spanID       string
	parentSpanID string
	requestID    string

	frames     []byte     // head window, whole frames only
	framesEnd  *frameTail // tail window, whole frames only
	frameHead  int        // bytes stored in the head window
	frameTail  int        // tail window budget in bytes
	headSealed bool       // first frame that missed the head window seals it
	frameFull  []byte
	frameMode  int8 // 0 off, 1 headtail, 2 full

	respBody []byte // non-stream response captured via IOCopyBytesGracefully

	// inlineMedia holds pictures lifted out of respBody to keep the payload
	// under the full-mode cap without losing the image. See liftInlineImages.
	inlineMedia []InlineMediaBlob

	totalFrameBytes int
	framesTruncated bool

	usage      Usage
	usageSet   bool
	finalized  bool
	hasContent bool

	// Two independent verdicts, decided once the model is known (BindModel
	// from the stream path, or settlement/finalize). metaSampled decides
	// whether a span exists at all — and therefore whether the console gets
	// a trace_id; contentSampled only decides whether that span carries
	// prompt/answer. Keeping them apart is what lets the log page link every
	// row while still cutting content volume.
	model          string
	decided        bool
	metaSampled    bool
	contentSampled bool
	excluded       bool
	// parentSampled is the W3C traceparent sampled bit when the client sent
	// one. nil means "no opinion", which is not the same as false.
	parentSampled *bool
}

// Begin creates the per-request Collector and stores it on the context.
// Reads the W3C traceparent header first so agent-side traces and gateway
// spans land on the same trace tree. No-op when disabled.
func Begin(c *gin.Context) {
	if !Enabled() || c == nil || c.Request == nil {
		return
	}
	conf := current()
	if !conf.Enabled || conf.Host == "" {
		return
	}
	// The request id is the log↔trace join key and the trace id source.
	// Without one the span would carry an empty trace id and no log row
	// could ever link to it — skip rather than emit garbage. In production
	// middleware.RequestId always sets it before Relay runs.
	requestID := c.GetString("X-Oneapi-Request-Id")
	if requestID == "" {
		return
	}
	col := &Collector{
		requestID: requestID,
		traceID:   TraceID(requestID),
		spanID:    SpanID(requestID),
	}
	if tp, ok := ParseTraceparent(c.Request.Header.Get("traceparent")); ok && tp.TraceID != "" {
		sampled := tp.Sampled
		col.parentSampled = &sampled
		// Adopt the client's trace id so the agent framework's spans and
		// this gateway span share one tree. parentSpanId must be set, or
		// the span becomes the root of a new trace and the tree breaks.
		col.traceID = tp.TraceID
		col.parentSpanID = tp.ParentSpanID
	}
	switch conf.BodyMode {
	case BodyOff:
		col.frameMode = 0
	case BodyFull:
		col.frameMode = 2
		col.frameFull = make([]byte, 0, 8192)
	default:
		col.frameMode = 1
	}
	c.Set(contextKey, col)
}

// nowIn returns the wall clock in the policy's timezone. A nil location means
// the process local zone; container images often run in UTC, which is exactly
// why the console shows the resolved zone next to the schedule.
func nowIn(conf *Config) time.Time {
	if conf != nil && conf.Location != nil {
		return time.Now().In(conf.Location)
	}
	return time.Now()
}

// BindModel tells the collector which model this request is for, as early as
// the relay knows it. Needed because per-model rules cannot be applied without
// the model, and waiting until finalize would buffer SSE frames we are about
// to throw away.
func BindModel(c *gin.Context, model string) {
	col := fromContext(c)
	if col == nil || model == "" || col.decided {
		return
	}
	col.model = model
	col.decide(current(), model)
}

// decide fixes both verdicts once. Cheap enough to call defensively from every
// reader: after the first call it is a bool check.
func (col *Collector) decide(conf *Config, model string) {
	if col.decided {
		return
	}
	col.decided = true
	if model == "" {
		model = col.model
	}
	rates, excluded := conf.Policy.Resolve(model, nowIn(conf))
	if excluded {
		col.excluded = true
		return
	}
	// Parent-based sampling first: an agent that already decided keeps its
	// tree coherent, and one that declined is not overridden — otherwise its
	// spans would dangle under a child the gateway invented.
	if conf.ParentBased && col.parentSampled != nil {
		if !*col.parentSampled {
			return
		}
		col.metaSampled = true
	} else {
		col.metaSampled = sampleByTrace(col.traceID, "meta", rates.Metadata)
	}
	if col.metaSampled {
		col.contentSampled = sampleByTrace(col.traceID, "content", rates.Content)
	}
}

func fromContext(c *gin.Context) *Collector {
	if c == nil {
		return nil
	}
	v, ok := c.Get(contextKey)
	if !ok {
		return nil
	}
	col, _ := v.(*Collector)
	return col
}

// HasCollector reports whether this context carries a live trace collector.
// The model fallback hook uses it to avoid double-emitting snapshots for
// requests that the relay defer already covers.
func HasCollector(c *gin.Context) bool {
	return fromContext(c) != nil
}

// TraceIDForLog returns the trace id that the log's admin metadata should
// carry, or "" when this request will not produce a trace. The id is only
// handed out once sampling has kept the request, so every link the console
// renders resolves; the fallback path (RecordConsume without a collector)
// reports the same verdict through its return value.
func TraceIDForLog(c *gin.Context, requestID string) string {
	if col := fromContext(c); col != nil {
		col.decide(current(), col.model)
		if !col.metaSampled {
			return ""
		}
		return col.traceID
	}
	conf := current()
	rates, excluded := conf.Policy.Resolve("", nowIn(conf))
	if excluded || !sampleByTrace(TraceID(requestID), "meta", rates.Metadata) {
		return ""
	}
	return TraceID(requestID)
}

// FillUsage records settlement data and returns the trace id so the caller
// can persist it into the log's admin-only metadata. Returns "" when
// tracing is off, this request has no collector, or sampling dropped it —
// the usage is still recorded because a failed unsampled request may be
// promoted by the error rate at finalize.
func FillUsage(c *gin.Context, u Usage) string {
	col := fromContext(c)
	if col == nil || col.finalized {
		return ""
	}
	col.usage = u
	col.usageSet = true
	// Settlement is the first point where the model is always known, so it
	// fixes the verdict when the stream hook never ran (non-stream, early
	// failure).
	col.decide(current(), col.model)
	if !col.metaSampled {
		return ""
	}
	return col.traceID
}

// ForceSample pins this request as sampled and returns its trace id. The
// error-log path calls it right before persisting the id: once the id is
// written the span must exist, so the decision cannot stay a random draw
// that LANGFUSE_ERROR_SAMPLE_RATE might lose. Failures are rare (117 rows in
// seven days on the production gateway), so forcing the sample costs
// nothing, and they are exactly the rows an operator opens Langfuse for.
func ForceSample(c *gin.Context) string {
	col := fromContext(c)
	if col == nil || col.finalized {
		return ""
	}
	// Forced rows are the ones an operator opens Langfuse for, so they keep
	// their content too — otherwise the link would open an empty trace.
	col.decided = true
	col.metaSampled = true
	col.contentSampled = true
	return col.traceID
}

// WrapStreamHandler returns a handler that mirrors SSE frames into the
// collector before delegating. Generic over the scanner's *StreamResult so
// this package needs no internal import; type inference makes the call site
// a single line. Each StreamScannerHandler invocation is one channel
// attempt, so wrapping resets the frame buffer: retries keep only the last
// attempt (docs §3.8). Returns h untouched when tracing is off.
func WrapStreamHandler[T any](c *gin.Context, h func(string, T)) func(string, T) {
	col := fromContext(c)
	if col == nil || col.frameMode == 0 {
		return h
	}
	conf := current()
	col.decide(conf, col.model)
	col.resetFrames(conf)
	return func(data string, sr T) {
		col.appendFrame([]byte(data), conf)
		h(data, sr)
	}
}

func (col *Collector) resetFrames(conf *Config) {
	if col.frameMode == 1 {
		if col.framesEnd == nil {
			col.framesEnd = newFrameTail(conf.TailBytes)
		} else {
			col.framesEnd.reset(conf.TailBytes)
		}
		col.frames = col.frames[:0]
		col.frameHead = 0
		col.frameTail = conf.TailBytes
		col.headSealed = false
	} else if col.frameMode == 2 {
		col.frameFull = col.frameFull[:0]
	}
	col.totalFrameBytes = 0
	col.framesTruncated = false
	col.hasContent = true
}

func (col *Collector) appendFrame(frame []byte, conf *Config) {
	sep := byte('\n')
	col.totalFrameBytes += len(frame) + 1
	// Content not sampled: still count bytes (interrupted-stream detection),
	// never buffer them. This is the whole point of a separate content rate.
	if col.decided && !col.contentSampled {
		return
	}
	switch col.frameMode {
	case 1:
		// Frames are semantic units: truncation must land on frame
		// boundaries, or the parser sees half a JSON object. A frame that
		// does not fit the head window goes to the tail window whole.
		if !col.headSealed && col.frameHead+len(frame)+1 <= conf.HeadBytes {
			col.frames = append(col.frames, frame...)
			col.frames = append(col.frames, sep)
			col.frameHead += len(frame) + 1
			return
		}
		// Seal on first miss: a frame too large for what is left of the
		// head window must not let a LATER small frame jump ahead of it in
		// the head window (that would reorder the conversation).
		col.headSealed = true
		col.framesTruncated = true
		if col.framesEnd != nil && col.frameTail > 0 {
			col.framesEnd.add(string(frame))
		}
	case 2:
		limit := conf.BodyMaxBytes()
		if len(col.frameFull)+len(frame)+1 > limit {
			col.framesTruncated = true
			// The separator counts against the cap too: leaving it out of the
			// arithmetic let the buffer end one byte over the number the
			// operator typed, which is the kind of off-by-one that makes a
			// documented limit untrustworthy.
			remaining := limit - len(col.frameFull) - 1
			if remaining > 0 {
				col.frameFull = append(col.frameFull, frame[:min(remaining, len(frame))]...)
				col.frameFull = append(col.frameFull, sep)
			}
			return
		}
		col.frameFull = append(col.frameFull, frame...)
		col.frameFull = append(col.frameFull, sep)
	}
}

// CaptureResponseBody records a complete non-stream response body. Called
// from service/http.go's IOCopyBytesGracefully, which every major channel
// uses to write buffered responses.
func CaptureResponseBody(c *gin.Context, data []byte) {
	col := fromContext(c)
	if col == nil || len(data) == 0 || col.frameMode == 0 {
		return
	}
	conf := current()
	// Do NOT decide here: the non-stream response is written before the
	// settlement point, so the model is still unknown and a verdict taken now
	// would freeze the default rates and silently ignore every per-model rule.
	// Buffer unconditionally and let finalize drop content it must not keep.
	if col.decided && !col.contentSampled {
		return
	}
	col.respBody = col.cacheFullBody(data, conf)
	col.hasContent = true
}

// cacheFullBody stores a full-mode response, lifting large pictures out of the
// payload first when the cap would otherwise cut them in half.
//
// The cap and the externalizer run at opposite ends of the pipeline: the cap
// fires here, when the response arrives, while the upload only happens in the
// flush worker. Cutting first left the JSON unterminated, the externalizer's
// regex needs a closed string literal, so it skipped the truncation — and a
// 3 MB image became 2 MB of unusable base64 with no picture in the drawer.
// Extracting the bytes keeps Output intact and still lets the picture through.
//
// Only the over-limit path does any of this work; a response that already fits
// is returned untouched, exactly as before.
func (col *Collector) cacheFullBody(data []byte, conf *Config) []byte {
	if conf.BodyMode != BodyFull {
		return truncateCopy(data, conf)
	}
	limit := conf.BodyMaxBytes()
	if len(data) <= limit {
		return data
	}
	slim, blobs := liftInlineImages(data)
	if len(blobs) > 0 && len(slim) <= limit {
		col.inlineMedia = blobs
		return slim
	}
	// Nothing worth lifting, or still over after lifting (a long text body):
	// fall back to the plain cut, which is what this path always did.
	return truncateCopy(data, conf)
}

// inlineMediaKey is the JSON member whose value is a raw base64 picture. It
// covers every image model this gateway relays — OpenAI images, and the
// vendors that speak its shape (flux, doubao seedream) — because all of them
// answer with `data[].b64_json`.
const inlineMediaKey = `"b64_json":"`

// Placeholder left in the payload. Slot numbers keep it unique per picture;
// the text is not base64, so the externalizer's regex ignores it and the
// drawer shows it as a plain string until the flush worker swaps it back.
const inlineMediaPlaceholder = "__apiduo_inline_media_%d__"

// Placeholder for a picture beyond the per-span upload cap. Dropping it here
// is deliberate: the uploader stops at mediaMaxPerSpan, so lifting more would
// only mean restoring them all on the export path and blowing past the cap a
// second time — with the JSON broken again, the very failure this fixes.
const inlineMediaOverflow = "[image %d bytes omitted: over the per-span upload cap]"

// Below this a picture is not worth the bookkeeping: the payload is over the
// cap, so whatever is here is big, and anything smaller than this is already
// handled by the externalizer on the normal path.
const inlineMediaMinChars = 256 << 10

// liftInlineImages replaces every large b64_json value in the payload with a
// placeholder and returns the decoded bytes alongside it.
//
// Runs on the request path, but only for a response that already blew past the
// cap — image endpoints, not chat. The scan is a pair of bytes.Index calls over
// a buffer we are already holding.
func liftInlineImages(data []byte) ([]byte, []InlineMediaBlob) {
	var (
		blobs []InlineMediaBlob
		// The slimmed payload is never larger than its source, so one
		// allocation sized to the input covers every append below.
		out  = make([]byte, 0, len(data))
		done int
	)
	for cursor := 0; ; {
		rel := bytes.Index(data[cursor:], []byte(inlineMediaKey))
		if rel < 0 {
			break
		}
		start := cursor + rel + len(inlineMediaKey)
		// Base64 contains no quote, so the next one closes the value. A
		// truncated payload has no closing quote — leave it to the plain cut.
		end := bytes.IndexByte(data[start:], '"')
		if end < 0 {
			break
		}
		end += start
		cursor = end
		if end-start < inlineMediaMinChars {
			continue
		}
		if len(blobs) >= mediaMaxPerSpan {
			out = append(out, data[done:start]...)
			out = append(out, fmt.Sprintf(inlineMediaOverflow, end-start)...)
			done = end
			continue
		}
		raw, ok := decodeB64(string(data[start:end]))
		if !ok {
			continue
		}
		// Magic bytes, not the field name: a `b64_json` that decodes to
		// something other than a picture is somebody else's payload.
		if _, _, ok := sniffImage(raw); !ok {
			continue
		}
		slot := len(blobs)
		blobs = append(blobs, InlineMediaBlob{Slot: slot, Raw: raw})
		out = append(out, data[done:start]...)
		out = append(out, fmt.Sprintf(inlineMediaPlaceholder, slot)...)
		done = end
	}
	if len(blobs) == 0 {
		return data, nil
	}
	return append(out, data[done:]...), blobs
}

// truncateCopy applies the head/tail window (or full cap) to a one-shot
// payload such as a request body or a non-stream response.
func truncateCopy(data []byte, conf *Config) []byte {
	switch conf.BodyMode {
	case BodyFull:
		limit := conf.BodyMaxBytes()
		if len(data) <= limit {
			return data
		}
		out := make([]byte, limit)
		copy(out, data[:limit])
		return out
	default: // headtail (BodyOff callers never reach here)
		if len(data) <= conf.HeadBytes+conf.TailBytes {
			return data
		}
		out := make([]byte, 0, conf.HeadBytes+conf.TailBytes)
		out = append(out, data[:conf.HeadBytes]...)
		out = append(out, data[len(data)-conf.TailBytes:]...)
		return out
	}
}

// requestBody returns the stored request body bytes, or nil. At relay-defer
// time the storage is still alive (middleware cleanup runs after handler
// defers, LIFO), but a defensive error path keeps finalize silent if some
// path already closed it.
func (col *Collector) requestBody(c *gin.Context) []byte {
	v, ok := c.Get(ginKeyBodyStorage)
	if !ok {
		return nil
	}
	bs, ok := v.(bodyBytes)
	if !ok {
		return nil
	}
	data, err := bs.Bytes()
	if err != nil {
		return nil
	}
	return data
}

// outputBytes assembles the captured response for the snapshot.
//
// Two mutually exclusive carriers exist: SSE frames (stream) and respBody
// (non-stream, written by CaptureResponseBody). An empty frame window means
// the request was not a stream, so respBody is the answer — without this
// fallback every non-stream response was silently dropped in headtail mode.
func (col *Collector) outputBytes() []byte {
	switch col.frameMode {
	case 1:
		var tail []byte
		if col.framesEnd != nil && col.frameTail > 0 {
			tail = col.framesEnd.bytes()
		}
		if len(col.frames) == 0 {
			if len(tail) == 0 {
				return col.respBody
			}
			return tail
		}
		if col.frameHead == 0 {
			return tail
		}
		out := make([]byte, 0, len(col.frames)+len(tail))
		out = append(out, col.frames...)
		out = append(out, tail...)
		return out
	case 2:
		if len(col.frameFull) > 0 {
			return col.frameFull
		}
		return col.respBody
	default:
		return col.respBody
	}
}

// frameStats reports (droppedBytes, truncated) for the stream window.
func (col *Collector) frameStats(conf *Config) (int, bool) {
	if col.frameMode == 0 || col.totalFrameBytes == 0 {
		return 0, false
	}
	switch col.frameMode {
	case 2:
		return max(0, col.totalFrameBytes-len(col.frameFull)), col.framesTruncated
	default:
		kept := len(col.frames)
		if col.framesEnd != nil {
			kept += col.framesEnd.sizeBytes()
		}
		return max(0, col.totalFrameBytes-kept), col.framesTruncated
	}
}

// frameTail keeps the last N bytes of whole frames. A byte-level ring would
// slice a JSON frame in half, and every parser downstream would see broken
// input; eviction is therefore per frame, oldest first.
type frameTail struct {
	frames []string
	size   int
	budget int
}

func newFrameTail(budget int) *frameTail {
	if budget <= 0 {
		budget = 1
	}
	return &frameTail{budget: budget}
}

func (t *frameTail) reset(budget int) {
	if budget <= 0 {
		budget = 1
	}
	t.frames = t.frames[:0]
	t.size = 0
	t.budget = budget
}

func (t *frameTail) add(frame string) {
	t.frames = append(t.frames, frame)
	t.size += len(frame) + 1
	// Keep at least one frame even when it alone exceeds the budget: the
	// newest frame is the one carrying finish_reason and usage.
	for t.size > t.budget && len(t.frames) > 1 {
		t.size -= len(t.frames[0]) + 1
		t.frames = t.frames[1:]
	}
}

func (t *frameTail) bytes() []byte {
	if len(t.frames) == 0 {
		return nil
	}
	out := make([]byte, 0, t.size)
	for _, f := range t.frames {
		out = append(out, f...)
		out = append(out, '\n')
	}
	return out
}

func (t *frameTail) sizeBytes() int {
	return t.size
}

// Propagate forwards W3C Trace Context headers to an outbound request.
//
// Adopting the client's trace id (Begin) is what keeps the agent tree
// intact on the Langfuse side; forwarding additionally lets an upstream
// that speaks OTLP (self-hosted vLLM, an instrumented proxy) join the same
// trace instead of starting its own.
//
// Gated on Enabled() on purpose: with tracing off the outbound request must
// stay byte-identical to upstream new-api, per the decoupling rules.
func Propagate(c *gin.Context, dst *http.Header) {
	if !Enabled() || c == nil || c.Request == nil || dst == nil {
		return
	}
	if tp := c.Request.Header.Get("traceparent"); tp != "" {
		dst.Set("traceparent", tp)
	}
	if ts := c.Request.Header.Get("tracestate"); ts != "" {
		dst.Set("tracestate", ts)
	}
}
