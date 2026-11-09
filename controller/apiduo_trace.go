package controller

import (
	"net/http"
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
			"metadata_sample_rate":  metadataRate,
			"content_sample_rate":   contentRate,
			"parent_based_sampling": conf.ParentBased,
			"server_zone":           zone,
			"server_time":           now.Format(time.RFC3339),
		},
	})
}
