package service

import (
	"github.com/QuantumNous/new-api/pkg/apiduo/trace"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
)

// Bridge from settlement-time values (unexported service types) to the
// trace collector's plain Usage struct. apiduo-only file; the marker block
// in text_quota.go stays one statement.

// apiduoTraceUsage fills usage into the request's collector and returns
// the trace id so the caller can persist it under the log's admin-only
// metadata. Returns "" when tracing is off or the request has no collector
// (then the relay defer still emits metadata with zero usage).
func apiduoTraceUsage(ctx *gin.Context, summary textQuotaSummary, relayInfo *relaycommon.RelayInfo) string {
	if relayInfo == nil || ctx == nil {
		return ""
	}
	// Settlement is the first point where the model is always known, so it
	// also fixes the sampling verdict for non-stream requests.
	trace.BindModel(ctx, relayInfo.OriginModelName)
	u := trace.Usage{
		PromptTokens:     summary.PromptTokens,
		CompletionTokens: summary.CompletionTokens,
		Quota:            summary.Quota,
		TokenName:        summary.TokenName,
	}
	if summary.UseTimeSeconds > 0 {
		u.UseTimeSec = int(summary.UseTimeSeconds)
	}
	return trace.FillUsage(ctx, u)
}
