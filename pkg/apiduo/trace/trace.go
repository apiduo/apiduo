package trace

import (
	"time"

	"github.com/gin-gonic/gin"
)

// Public API. Every entry point is cheap and no-op-safe when disabled; the
// upstream call sites are one line each (docs §3.3), and type translation
// from repository types lives in bridge files next to those call sites.

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

	// The verdict is fixed as soon as the model is known (BindModel on the
	// stream path, settlement, or here) so the log row's trace_id can never
	// point at a span that was never sent. Failures may still be promoted by
	// the error rate — except for excluded models, which must stay invisible.
	col.decide(conf, m.Model)
	if col.excluded {
		return
	}
	if !col.metaSampled {
		if m.Success || !sampleByTrace(col.traceID, "error", conf.ErrorSampleRate) {
			return
		}
		// Promoted by the error rate: content still follows the policy so a
		// content=0 deployment never leaks prompts through the error path.
		rates, _ := conf.Policy.Resolve(m.Model, nowIn(conf))
		col.metaSampled = true
		col.contentSampled = sampleByTrace(col.traceID, "content", rates.Content)
	}

	endNano := time.Now().UnixNano()
	// A missing start (nil RelayInfo) or a zero time.Time — whose UnixNano()
	// is a large negative number, never 0 — would place the span decades
	// outside the time axis and stretch its latency to match, poisoning the
	// percentile of whichever bucket it lands in. Collapse it to a
	// zero-length span at end time; TTFT then stays 0 because v4 requires
	// completion_start_time to fall inside [start, end].
	startNano := m.StartUnixNano
	if startNano <= 0 {
		startNano = endNano
	}
	latencyMs := time.Duration(endNano - startNano).Milliseconds()
	ttftMs := int64(0)
	if m.FirstByteNano > startNano {
		ttftMs = time.Duration(m.FirstByteNano - startNano).Milliseconds()
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
		StartUnixNano: startNano,
		EndUnixNano:   endNano,
		TtftMs:        ttftMs,
		LatencyMs:     latencyMs,
		IsStream:      m.IsStream,
		Retry:         m.Retry,
	}
	// Interrupted is diagnostics, not content. Keeping it inside the
	// content-sampling branch erased every mid-stream failure whenever content
	// sampling was off — which is precisely when no retained body exists to
	// tell "upstream errored" from "stream was cut", so the flag cannot be
	// reconstructed later. BodyOff remains the one blind spot: with no frame
	// wrapping at all there is no byte count to test, and the ERROR level is
	// the only failure signal left.
	sn.Interrupted = !m.Success && col.totalFrameBytes > 0
	if col.usageSet {
		sn.PromptTokens = col.usage.PromptTokens
		sn.CompletionTokens = col.usage.CompletionTokens
		sn.Quota = col.usage.Quota
		if col.usage.TokenName != "" {
			sn.TokenName = col.usage.TokenName
		}
	}

	if conf.BodyMode != BodyOff && col.contentSampled {
		if input := col.requestBody(c); len(input) > 0 {
			sn.Input = truncateCopy(input, conf)
			if len(sn.Input) < len(input) {
				sn.InputTruncated = true
				sn.InputDropped = len(input) - len(sn.Input)
			}
		}
		if out := col.outputBytes(); len(out) > 0 {
			sn.Output = out
			sn.InlineMedia = col.inlineMedia
			dropped, truncated := col.frameStats(conf)
			sn.OutputDropped = dropped
			sn.OutputTruncated = truncated
		}
		// Queue routing follows the bytes we actually carry. Gating this on
		// usage hid the request body of failed requests — the one case where
		// "what did we send upstream" matters most.
		sn.HasContent = len(sn.Input) > 0 || len(sn.Output) > 0
		sn.ContentSampled = col.contentSampled
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
	rates, excluded := conf.Policy.Resolve(in.Model, nowIn(conf))
	if excluded || !sampleByTrace(TraceID(in.RequestID), "meta", rates.Metadata) {
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
