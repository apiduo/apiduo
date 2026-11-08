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
	if c == nil {
		return ""
	}
	// With a live relay collector the defer path owns this request's span;
	// emit only the id (TraceIDForLog mirrors what that span reports, which
	// honors a client traceparent). Without one (async task settlement,
	// MJ, violation fees) emit the fallback metadata span.
	if trace.Enabled() && !trace.HasCollector(c) {
		trace.RecordConsume(trace.RecordConsumeInput{
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
	if !trace.Enabled() {
		return ""
	}
	return trace.TraceIDForLog(c, requestId)
}
