package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/pkg/apiduo/trace"
	"github.com/gin-gonic/gin"
)

// GetApiduoTraceConfig exposes the console jump-link fields to admins.
// Secrets never leave: only what the log page needs to build
// {host}/project/{projectId}/traces/{traceId}. This is a dedicated
// AdminAuth endpoint because /api/status is public-by-design and must not
// leak internal hostnames.
func GetApiduoTraceConfig(c *gin.Context) {
	conf := trace.CurrentConfig()
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"enabled":     conf.Enabled,
			"host":        conf.Host,
			"project_id":  conf.ProjectID,
			"body_mode":   conf.BodyModeName(),
			"sample_rate": conf.SampleRate,
		},
	})
}
