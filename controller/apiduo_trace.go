package controller

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/apiduo/trace"
	"github.com/gin-gonic/gin"
)

// GetApiduoTraceConfig exposes the console jump-link fields to admins.
// Secrets never leave: only what the log page needs to build
// {host}/project/{projectId}/traces/{traceId}. This is a dedicated
// AdminAuth endpoint because /api/status is public-by-design and must not
// leak internal hostnames.
func GetApiduoTraceConfig(c *gin.Context) {
	// The link exposes the full conversation. Only root — or a user the
	// settings explicitly list — may learn the console URL at all.
	if c.GetInt("role") < common.RoleRootUser && !trace.TraceViewerAllowed(c.GetInt("id")) {
		c.JSON(http.StatusForbidden, gin.H{
			"success": false,
			"message": "Unauthorized, insufficient privileges",
		})
		return
	}
	conf := trace.CurrentConfig()
	metadataRate, contentRate := 100, 100
	if conf.Policy != nil {
		metadataRate, contentRate = conf.Policy.Defaults.Metadata, conf.Policy.Defaults.Content
	}
	// The zone and the policy clock are part of the payload on purpose: a
	// time-window rule read in the container's UTC looks like a bug when the
	// operator configured 09:00 in Asia/Shanghai.
	zone, now := trace.ServerZone()
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"enabled":               conf.Enabled,
			"host":                  conf.ConsoleBase(),
			"project_id":            conf.ProjectID,
			"body_mode":             conf.BodyModeName(),
			"body_head_bytes":       conf.HeadBytes,
			"body_tail_bytes":       conf.TailBytes,
			"error_sample_rate":     conf.ErrorSampleRate,
			"redact":                conf.Redact,
			"environment":           conf.Environment,
			"metadata_sample_rate":  metadataRate,
			"content_sample_rate":   contentRate,
			"parent_based_sampling": conf.ParentBased,
			"server_zone":           zone,
			"server_time":           now.Format(time.RFC3339),
		},
	})
}

// GetApiduoTraceDetail proxies one trace from Langfuse for the log drawer.
//
// The browser must never hold the Langfuse secret key, so the read goes
// through the gateway. Authorization is the same three-lock rule as the
// config endpoint: root always, other admins only when the settings list
// them, and the log API already stripped admin_info from user-scope rows so
// a normal user has no trace id to ask for in the first place.
//
// "No trace" is not an HTTP error. A request that was sampled out, dropped
// from a full queue, or is older than retention is a normal state the drawer
// explains in words — a 404 would surface as a generic fetch failure and send
// the operator hunting for a bug.
func GetApiduoTraceDetail(c *gin.Context) {
	if c.GetInt("role") < common.RoleRootUser && !trace.TraceViewerAllowed(c.GetInt("id")) {
		c.JSON(http.StatusForbidden, gin.H{
			"success": false,
			"message": "Unauthorized, insufficient privileges",
		})
		return
	}

	id := strings.ToLower(strings.TrimSpace(c.Param("trace_id")))
	if !isTraceID(id) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid trace id"})
		return
	}

	obs, err := trace.FetchObservation(id)
	switch {
	case errors.Is(err, trace.ErrTraceDisabled):
		c.JSON(http.StatusOK, gin.H{"success": true, "data": nil, "reason": "disabled"})
	case errors.Is(err, trace.ErrObservationNotFound):
		c.JSON(http.StatusOK, gin.H{"success": true, "data": nil, "reason": "not_found"})
	case err != nil:
		// Backend unreachable / bad credentials live in the server log; the
		// console gets one neutral sentence and keeps working.
		c.JSON(http.StatusBadGateway, gin.H{
			"success": false,
			"message": "Trace backend unavailable",
		})
	default:
		c.JSON(http.StatusOK, gin.H{"success": true, "data": obs})
	}
}

// isTraceID pins the shape before the value reaches a URL. The id comes from
// our own log row, but validating it here keeps a hand-crafted request from
// turning this endpoint into a query-string smuggler.
func isTraceID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
