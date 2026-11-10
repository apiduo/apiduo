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
// instead: one request, one transaction, one audit event.
//
// Atomicity is the point, not a side effect: the per-key path commits each key
// as it goes, so a rejected value in the middle of a form leaves the rest of
// that form applied. Here every key is validated before the transaction opens.
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
		// 不认识就拒。此前这里对任何键名都回 200 并落库 ——
		// 改代码时手滑拼错一个键名，界面和接口都显示「保存成功、已生效」，
		// 实际什么都没发生，而垃圾键会一直留在 options 表里。
		if !model.OptionKeyWritable(key) {
			common.ApiErrorMsg(c, "Unknown option key: "+key)
			return
		}
	}

	values := make(map[string]string, len(keys))
	for _, key := range keys {
		values[key] = common.Interface2String(request.Options[key])
	}
	// 跨键校验要看合并后的视图（例如「启用存储」是否真有目标），
	// 逐键校验看不到同一批里另一个键的新值。
	if err := model.ValidateApiduoOptionSet(values); err != nil {
		common.ApiErrorMsg(c, err.Error())
		return
	}
	// One transaction for the whole save. A settings form that submits six
	// fields is one operator action, and a half-applied policy (new rates,
	// old rules) is worse than a refusal — UpdateOptionsBulk validates every
	// key first, then writes them in a single transaction.
	if err := model.UpdateOptionsBulk(values); err != nil {
		common.ApiErrorMsg(c, err.Error())
		return
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
