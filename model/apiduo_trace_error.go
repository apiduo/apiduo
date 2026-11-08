package model

import (
	"github.com/QuantumNous/new-api/pkg/apiduo/trace"
	"github.com/gin-gonic/gin"
)

// Bridge for the error-log write path. apiduo-only file; the marker block in
// log.go stays a single call.

// apiduoTraceErrorLog pins the request's sampling and stamps the trace id
// into the error log's admin-only metadata, so the console's "open in
// Langfuse" link resolves for failed requests too.
//
// Error logs carry the id that the consume path cannot: a failed request
// never reaches settlement, so RecordConsumeLog never runs for it. Without
// this hook the trace exists in Langfuse but the log row that explains the
// failure has no way to reach it.
func apiduoTraceErrorLog(c *gin.Context, requestId string, other *LogOther) {
	if c == nil || other == nil || !trace.Enabled() || !trace.HasCollector(c) {
		return
	}
	if tid := trace.ForceSample(c); tid != "" {
		other.SetAdmin("trace_id", tid)
	}
}
