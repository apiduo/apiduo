package controller

import (
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

const (
	skQuotaDefaultLimit = 5000
	skQuotaMaxLimit     = 20000
)

// SkQuotaResponse carries the aggregated buckets plus the totals for the whole
// filter set, so the UI can distinguish "this is everything" from "this is the
// top N of a larger set".
type SkQuotaResponse struct {
	Items     []*model.SkQuotaData `json:"items"`
	Totals    *model.SkQuotaTotals `json:"totals"`
	Truncated bool                 `json:"truncated"`
	Limit     int                  `json:"limit"`
}

func parseSkQuotaLimit(c *gin.Context) (int, bool) {
	raw := c.Query("limit")
	if raw == "" {
		return skQuotaDefaultLimit, true
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		common.ApiErrorMsg(c, "invalid limit")
		return 0, false
	}
	return min(parsed, skQuotaMaxLimit), true
}

func respondSkQuota(c *gin.Context, items []*model.SkQuotaData, totals *model.SkQuotaTotals, limit int) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": SkQuotaResponse{
			Items:     items,
			Totals:    totals,
			Truncated: len(items) >= limit,
			Limit:     limit,
		},
	})
}

// GetSkQuotaDates returns usage grouped by token, user and model.
func GetSkQuotaDates(c *gin.Context) {
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	startTimestamp, err := limitLogTimeRange(startTimestamp, endTimestamp)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	limit, ok := parseSkQuotaLimit(c)
	if !ok {
		return
	}

	items, totals, err := model.GetSkQuotaData(startTimestamp, endTimestamp, c.Query("username"), c.Query("model"), limit)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	respondSkQuota(c, items, totals, limit)
}

// GetSelfSkQuotaDates returns the caller's own usage grouped by token and model.
//
// The owner boundary is taken from the session and nothing else: username and
// user_id query parameters are ignored on purpose, so a reseller can only ever
// see the keys under their own account. Downstream customers do not need their
// own accounts because the reseller distinguishes them by token.
func GetSelfSkQuotaDates(c *gin.Context) {
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	startTimestamp, err := limitLogTimeRange(startTimestamp, endTimestamp)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	limit, ok := parseSkQuotaLimit(c)
	if !ok {
		return
	}

	items, totals, err := model.GetSelfSkQuotaData(c.GetInt("id"), startTimestamp, endTimestamp, c.Query("model"), limit)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	respondSkQuota(c, items, totals, limit)
}
