package controller

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// apiduoBatchOptionLimit caps one request so a single save can never turn into
// an unbounded write loop. The settings forms send a few dozen keys at most.
const apiduoBatchOptionLimit = 64

type apiduoBatchOptionRequest struct {
	Options map[string]any `json:"options"`
}

// UpdateOptionsBatch applies several option values in one request and records
// the whole save as a single audit event.
//
// The per-key endpoint writes one audit row per key, so a form that submits ten
// fields leaves ten entries behind for what is really one operator action, and
// pays one round trip per field. Settings forms submit their dirty fields here
// instead: one request, one audit event, one list of changed keys.
func UpdateOptionsBatch(c *gin.Context) {
	var request apiduoBatchOptionRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	if len(request.Options) == 0 {
		common.ApiErrorMsg(c, "No options to update")
		return
	}
	if len(request.Options) > apiduoBatchOptionLimit {
		common.ApiErrorMsg(c, fmt.Sprintf("Too many options in one request (max %d)", apiduoBatchOptionLimit))
		return
	}

	keys := make([]string, 0, len(request.Options))
	for key := range request.Options {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	// Reject the whole batch before writing anything, so a single rejected key
	// cannot leave the form half applied.
	for _, key := range keys {
		if strings.TrimSpace(key) == "" {
			common.ApiErrorMsg(c, "Option key cannot be empty")
			return
		}
		if isPaymentComplianceOptionKey(key) {
			common.ApiErrorMsg(c, "Compliance confirmation fields cannot be changed through this API")
			return
		}
	}

	applied := make([]string, 0, len(keys))
	for _, key := range keys {
		value := common.Interface2String(request.Options[key])
		if err := model.UpdateOption(key, value); err != nil {
			// UpdateOption has no transaction across keys, so values written
			// before the failure stay applied. Report which ones landed instead
			// of pretending nothing changed.
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": err.Error(),
				"data":    gin.H{"applied": applied, "failed": key},
			})
			return
		}
		applied = append(applied, key)
	}

	model.RecordOperationAuditLog(
		c.GetInt("id"),
		c.GetInt("role"),
		apiduoBatchOptionAuditContent(keys),
		c.ClientIP(),
		"option.batch_update",
		map[string]any{"keys": keys, "count": len(keys)},
		auditOperatorInfo(c),
		nil,
		c,
	)
	markAuditLogged(c)

	common.ApiSuccess(c, gin.H{"saved": keys, "count": len(keys)})
}

// apiduoBatchOptionAuditContent renders one line for the audit list. Values are
// never included: they may hold secrets.
func apiduoBatchOptionAuditContent(keys []string) string {
	if len(keys) == 1 {
		return "Updated system setting " + keys[0]
	}
	return fmt.Sprintf("Updated %d system settings: %s", len(keys), strings.Join(keys, ", "))
}
