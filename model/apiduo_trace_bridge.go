package model

import (
	"github.com/QuantumNous/new-api/pkg/apiduo/trace"
	"github.com/gin-gonic/gin"
)

// Bridge from RecordConsumeLogParams (a model type the trace package must
// not import) to the trace package's plain input. apiduo-only file; the
// marker block in log.go stays one statement.

// apiduoTraceConsume emits a fallback snapshot for log writes that happen
// outside a relay request (async task settlement, MJ, violation fees) and
// returns the derived trace id for admin-only log metadata. Requests that
// still carry a live relay collector are skipped — their relay defer
// already emits, and double emission would corrupt counts.
func apiduoTraceConsume(c *gin.Context, requestId string, userId int, params RecordConsumeLogParams) string {
	if c == nil || !trace.Enabled() {
		return ""
	}
	// With a live relay collector the defer path owns this request's span;
	// report only the id (TraceIDForLog mirrors what that span reports,
	// which honors a client traceparent and returns "" when sampling
	// dropped the request).
	if trace.HasCollector(c) {
		return trace.TraceIDForLog(c, requestId)
	}
	// Without one (async task settlement, MJ, violation fees) the fallback
	// metadata span is the only chance to emit, and its return value is the
	// id to persist: "" means the span was dropped, so the log carries no
	// link that would 404.
	return trace.RecordConsume(trace.RecordConsumeInput{
		RequestID:        requestId,
		UserId:           userId,
		Model:            params.ModelName,
		Group:            params.Group,
		TokenName:        params.TokenName,
		ChannelId:        params.ChannelId,
		PromptTokens:     params.PromptTokens,
		CompletionTokens: params.CompletionTokens,
		Quota:            params.Quota,
		UseTimeSec:       params.UseTimeSeconds,
		IsStream:         params.IsStream,
	})
}
