package controller

import (
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// maxTimezoneOffsetMinutes caps the shift at +-14 hours, the widest real-world
// offset, so a bad query can never move the day boundary into another date.
const maxTimezoneOffsetMinutes = 14 * 60

type SeriesResponse struct {
	Groups []*model.SeriesGroup `json:"groups"`
}

func parseTimezoneOffset(c *gin.Context) int {
	raw := c.Query("tz_offset_minutes")
	if raw == "" {
		return 0
	}
	offset, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return max(-maxTimezoneOffsetMinutes, min(offset, maxTimezoneOffsetMinutes))
}

// GetUsageSeries returns daily usage grouped by model or user.
func GetUsageSeries(c *gin.Context) {
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	startTimestamp, err := limitLogTimeRange(startTimestamp, endTimestamp)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	dimension := c.DefaultQuery("dimension", "model")
	if dimension != "model" && dimension != "user" && dimension != "channel" {
		common.ApiErrorMsg(c, "dimension must be model, user or channel")
		return
	}

	groups, err := model.GetUsageSeries(startTimestamp, endTimestamp, dimension, parseTimezoneOffset(c))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    SeriesResponse{Groups: groups},
	})
}

// GetSelfUsageSeries returns the caller's own daily usage.
func GetSelfUsageSeries(c *gin.Context) {
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	startTimestamp, err := limitLogTimeRange(startTimestamp, endTimestamp)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	group, err := model.GetSelfUsageSeries(c.GetInt("id"), startTimestamp, endTimestamp, parseTimezoneOffset(c))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    group,
	})
}

const (
	defaultBalanceDays = 14
	maxBalanceDays     = 90
)

type UserBalanceResponse struct {
	StartDate string               `json:"start_date"`
	Users     []*model.UserBalance `json:"users"`
}

// GetUserBalanceSeries returns every spending user with their balance and a
// dense daily series, so the client can project when each balance runs out.
func GetUserBalanceSeries(c *gin.Context) {
	days := defaultBalanceDays
	if raw := c.Query("days"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			common.ApiErrorMsg(c, "invalid days")
			return
		}
		days = min(parsed, maxBalanceDays)
	}

	endTimestamp := common.GetTimestamp()
	if raw := c.Query("end_timestamp"); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			endTimestamp = min(endTimestamp, parsed)
		}
	}
	startTimestamp := endTimestamp - int64(days)*86400

	users, startDate, err := model.GetUserBalanceSeries(startTimestamp, endTimestamp, days, parseTimezoneOffset(c))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    UserBalanceResponse{StartDate: startDate, Users: users},
	})
}
