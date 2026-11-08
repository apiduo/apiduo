package trace

import (
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// Public API. Every entry point is cheap and no-op-safe when disabled; the
// upstream call sites are one line each (docs §3.3), and type translation
// from repository types lives in bridge files next to those call sites.

// sampleCounter drives the lock-free sampling decision
// (atomic increment + modulo; math/rand's global source has a mutex).
var sampleCounter atomic.Uint64

// FinalizeRelay builds the snapshot at the request boundary and enqueues
// it. Called exactly once from controller/relay.go's defer, which covers
// success, failure, and panic paths — failed requests get traces too
// (docs §3.8), which is the whole reason finalize lives in the defer
// instead of the settlement point.
func FinalizeRelay(c *gin.Context, m RelayMeta) {
	col := fromContext(c)
	if col == nil || col.finalized {
		return
	}
	col.finalized = true
	conf := current()

	// The regular-rate verdict was fixed at Begin so the log row's trace_id
	// can never point at a span that was never sent. Failures may still be
	// promoted by the error rate: they are the sample an operator opens
	// Langfuse for, and a missing jump link is cheaper than a missing trace.
	if !col.sampled && (m.Success || !shouldSample(conf, false)) {
		return
	}

	endNano := time.Now().UnixNano()
	latencyMs := time.Duration(endNano - m.StartUnixNano).Milliseconds()
	if m.StartUnixNano == 0 {
		latencyMs = 0
	}
	ttftMs := int64(0)
	if m.FirstByteNano > m.StartUnixNano {
		ttftMs = time.Duration(m.FirstByteNano - m.StartUnixNano).Milliseconds()
	}

	sn := &Snapshot{
		TraceID:       col.traceID,
		SpanID:        col.spanID,
		ParentSpanID:  col.parentSpanID,
		RequestID:     col.requestID,
		Operation:     m.Operation,
		Name:          m.Operation + " " + m.Model,
		Model:         m.Model,
		Group:         m.Group,
		TokenName:     m.TokenName,
		Format:        m.Format,
		UserId:        m.UserId,
		ChannelId:     m.ChannelId,
		TokenId:       m.TokenId,
		Success:       m.Success,
		ErrorStatus:   m.ErrorStatus,
		ErrorText:     clipRunes(m.ErrorText, maxErrorTextRunes),
		StartUnixNano: m.StartUnixNano,
		EndUnixNano:   endNano,
		TtftMs:        ttftMs,
		LatencyMs:     latencyMs,
		IsStream:      m.IsStream,
		Retry:         m.Retry,
	}
	if col.usageSet {
		sn.PromptTokens = col.usage.PromptTokens
		sn.CompletionTokens = col.usage.CompletionTokens
		sn.Quota = col.usage.Quota
		if col.usage.TokenName != "" {
			sn.TokenName = col.usage.TokenName
		}
	}

	if conf.BodyMode != BodyOff {
		if input := col.requestBody(c); len(input) > 0 {
			sn.Input = truncateCopy(input, conf)
			if len(sn.Input) < len(input) {
				sn.InputTruncated = true
				sn.InputDropped = len(input) - len(sn.Input)
			}
		}
		if out := col.outputBytes(); len(out) > 0 {
			sn.Output = out
			dropped, truncated := col.frameStats(conf)
			sn.OutputDropped = dropped
			sn.OutputTruncated = truncated
		}
		// Queue routing follows the bytes we actually carry. Gating this on
		// usage hid the request body of failed requests — the one case where
		// "what did we send upstream" matters most.
		sn.HasContent = len(sn.Input) > 0 || len(sn.Output) > 0
		// A failed request that already produced frames was interrupted
		// mid-stream — the most valuable sample for stream diagnostics.
		sn.Interrupted = !m.Success && col.totalFrameBytes > 0
	}

	exporterFor(conf).enqueue(sn)
}

// RecordConsume emits a metadata-only snapshot for log writes that happen
// outside a relay request: async task settlement, MJ, violation fees. The
// model bridge skips it when a relay collector exists (the relay defer
// already covers that request) so sync requests never double-emit.
//
// Returns the trace id when a span was queued, "" when it was dropped —
// the caller writes the id into the log only in the first case, so a log
// link always resolves.
func RecordConsume(in RecordConsumeInput) string {
	if !Enabled() || in.RequestID == "" {
		return ""
	}
	conf := current()
	if !shouldSample(conf, true) {
		return ""
	}
	endNano := in.UnixNano
	if endNano == 0 {
		endNano = time.Now().UnixNano()
	}
	operation := in.Operation
	if operation == "" {
		operation = "consume"
	}
	sn := &Snapshot{
		TraceID:          TraceID(in.RequestID),
		SpanID:           SpanID(in.RequestID),
		RequestID:        in.RequestID,
		Operation:        operation,
		Name:             operation + " " + in.Model,
		Model:            in.Model,
		Group:            in.Group,
		TokenName:        in.TokenName,
		UserId:           in.UserId,
		ChannelId:        in.ChannelId,
		Success:          true,
		StartUnixNano:    endNano - int64(in.UseTimeSec)*int64(time.Second),
		EndUnixNano:      endNano,
		LatencyMs:        int64(in.UseTimeSec) * 1000,
		IsStream:         in.IsStream,
		PromptTokens:     in.PromptTokens,
		CompletionTokens: in.CompletionTokens,
		Quota:            in.Quota,
	}
	exporterFor(conf).enqueue(sn)
	return sn.TraceID
}

// shouldSample applies the independent regular/error rates. Failed
// requests default to 100%: they are rare and they are exactly what
// someone opening Langfuse wants to inspect.
func shouldSample(conf *Config, success bool) bool {
	rate := conf.SampleRate
	if !success {
		rate = conf.ErrorSampleRate
	}
	if rate >= 100 {
		return true
	}
	if rate <= 0 {
		return false
	}
	n := sampleCounter.Add(1)
	return int(n%100) < rate
}
