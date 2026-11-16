package controller

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// GetOpsSeries 返回错误率与延迟的时间线。
func GetOpsSeries(c *gin.Context) {
	from, to := opsRange(c)
	bucket, _ := strconv.ParseInt(c.Query("bucket"), 10, 64)
	if bucket <= 0 {
		bucket = 600
	}
	points, err := service.CachedOpsSeries(c.Request.Context(), from, to, bucket, c.Query("model"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{
		"from":   from,
		"to":     to,
		"bucket": bucket,
		"points": points,
		// 时间窗对齐到 30 分钟后，数据最多滞后这么久。
		// 显式告诉调用方，免得有人拿它当实时监控用。
		"aligned_to": opsAlignGranularity,
		// 时间精度说明随数据一起返回：use_time 是秒级，
		// 拿它当首字延迟会得出错误结论。
		"note": "latency uses use_time_ms; older rows fall back to seconds x1000",
	})
}

// GetOpsBreakdown 按渠道或模型下钻。
func GetOpsBreakdown(c *gin.Context) {
	from, to := opsRange(c)
	dim := c.DefaultQuery("dim", "model")
	rows, err := service.CachedOpsBreakdown(c.Request.Context(), from, to, dim, c.Query("model"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"dim": dim, "rows": rows})
}

// GetOpsChanges 返回时间窗内的变更事件，用于在时间线上标注竖线。
func GetOpsChanges(c *gin.Context) {
	from, to := opsRange(c)
	limit, _ := strconv.Atoi(c.Query("limit"))
	evs, err := model.ListChangeEvents(from, to, limit)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"events": evs})
}

// CreateOpsChange 手工登记一次变更（发布、配置修改等无法自动捕获的动作）。
func CreateOpsChange(c *gin.Context) {
	var req struct {
		Kind       string `json:"kind"`
		Title      string `json:"title"`
		Detail     string `json:"detail"`
		OccurredAt int64  `json:"occurred_at"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiError(c, err)
		return
	}
	if req.Title == "" {
		common.ApiErrorMsg(c, "title is required")
		return
	}
	model.RecordChange(model.ChangeEvent{
		Kind:       req.Kind,
		Title:      req.Title,
		Detail:     req.Detail,
		OccurredAt: req.OccurredAt,
		Actor:      c.GetString("username"),
	})
	common.ApiSuccess(c, nil)
}

// GetOpsIncidents 返回值班事项列表。
func GetOpsIncidents(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	items, err := service.CachedOpsIncidents(c.Query("all") == "1", limit)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"items": items})
}

// CreateOpsIncident 手工开单。
func CreateOpsIncident(c *gin.Context) {
	var req struct {
		Title    string `json:"title"`
		Severity int    `json:"severity"`
		Detail   string `json:"detail"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiError(c, err)
		return
	}
	if req.Title == "" {
		common.ApiErrorMsg(c, "title is required")
		return
	}
	if req.Severity <= 0 {
		req.Severity = 2
	}
	inc, err := model.CreateIncident(model.OpsIncident{
		Title:     req.Title,
		Severity:  req.Severity,
		Detail:    req.Detail,
		CreatedBy: c.GetString("username"),
	})
	if err != nil {
		common.ApiError(c, err)
		return
	}
	service.InvalidateOpsCache("incidents:")
	common.ApiSuccess(c, inc)
}

// ClaimOpsIncident 认领一条事项。冲突时返回 409，
// 让前端能明确提示「已被他人认领」而不是笼统失败。
func ClaimOpsIncident(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	err := model.ClaimIncident(id, c.GetInt("id"), c.GetString("username"))
	if errors.Is(err, model.ErrIncidentNotClaimable) {
		c.JSON(http.StatusConflict, gin.H{
			"success": false,
			"message": common.NewMessage("Already claimed or closed").Error(),
		})
		return
	}
	if err != nil {
		common.ApiError(c, err)
		return
	}
	service.InvalidateOpsCache("incidents:")
	common.ApiSuccess(c, nil)
}

// CloseOpsIncident 结单。
func CloseOpsIncident(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var req struct {
		Status     string `json:"status"`
		Resolution string `json:"resolution"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiError(c, err)
		return
	}
	err := model.CloseIncident(id, req.Status, req.Resolution)
	if errors.Is(err, model.ErrIncidentNotClaimable) {
		c.JSON(http.StatusConflict, gin.H{
			"success": false,
			"message": common.NewMessage("Already claimed or closed").Error(),
		})
		return
	}
	if err != nil {
		common.ApiError(c, err)
		return
	}
	service.InvalidateOpsCache("incidents:")
	common.ApiSuccess(c, nil)
}

// GetOpsClusters 返回按根因聚类的错误分组。
func GetOpsClusters(c *gin.Context) {
	from, to := opsRange(c)
	limit, _ := strconv.Atoi(c.Query("limit"))
	clusters, err := service.CachedErrorClusters(c.Request.Context(), from, to, limit, c.Query("model"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"clusters": clusters})
}

// ReassignOpsIncident 把事项转交给当前值班的人。
// 转交给自己不算错——「认领」本身就是把 owner 设成自己。
func ReassignOpsIncident(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	err := model.ReassignIncident(id, c.GetInt("id"), c.GetString("username"))
	if errors.Is(err, model.ErrIncidentNotClaimable) {
		c.JSON(http.StatusConflict, gin.H{
			"success": false,
			"message": common.NewMessage("Already closed").Error(),
		})
		return
	}
	if err != nil {
		common.ApiError(c, err)
		return
	}
	service.InvalidateOpsCache("incidents:")
	common.ApiSuccess(c, nil)
}

// GetOpsRateLimits 返回各渠道最近一次观测到的上游额度水位。
// 数据在内存里，不查库，所以这个接口始终是快的。
func GetOpsRateLimits(c *gin.Context) {
	common.ApiSuccess(c, gin.H{"channels": service.RateLimitCacheForAPI()})
}

// GetOpsMargins 返回按渠道的收支与毛利。
func GetOpsMargins(c *gin.Context) {
	from, to := opsRange(c)
	rows, err := service.CachedChannelMargins(c.Request.Context(), from, to)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"channels": rows})
}

// GetOpsChannelCosts 返回单价配置。
func GetOpsChannelCosts(c *gin.Context) {
	costs, err := model.ListChannelCosts()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"costs": costs})
}

// UpsertOpsChannelCost 写入或更新某个渠道的单价。
func UpsertOpsChannelCost(c *gin.Context) {
	var req struct {
		ChannelId        int     `json:"channel_id"`
		InputPerMillion  float64 `json:"input_per_million"`
		OutputPerMillion float64 `json:"output_per_million"`
		Note             string  `json:"note"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiError(c, err)
		return
	}
	if req.ChannelId <= 0 {
		common.ApiErrorMsg(c, "channel_id is required")
		return
	}
	if err := model.UpsertChannelCost(model.ChannelCost{
		ChannelId:        req.ChannelId,
		InputPerMillion:  req.InputPerMillion,
		OutputPerMillion: req.OutputPerMillion,
		Note:             req.Note,
	}); err != nil {
		common.ApiError(c, err)
		return
	}
	// 单价改了，已经算好并缓存的毛利必须重算。
	service.InvalidateOpsCache("margins:")
	common.ApiSuccess(c, nil)
}

// GetOpsShift 返回当前值班与最近班次。
func GetOpsShift(c *gin.Context) {
	current, shifts, err := service.CachedOpsShifts(10)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"current": current, "shifts": shifts})
}

// StartOpsShift 接班：结束上一班并开启本班。
func StartOpsShift(c *gin.Context) {
	var req struct {
		Note string `json:"note"`
	}
	_ = c.ShouldBindJSON(&req)
	shift, err := model.StartShift(c.GetInt("id"), c.GetString("username"), req.Note)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	service.InvalidateOpsCache("shift:")
	common.ApiSuccess(c, shift)
}
