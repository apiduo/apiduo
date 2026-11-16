package controller

/* APIDUO:growth:BEGIN */
// 增长运营的只读视图与人工动作接口。
// 全部挂在管理员鉴权下，且不参与任何转发路径。

import (
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// GetGrowthOverview 返回概览指标。
func GetGrowthOverview(c *gin.Context) {
	overview, err := service.BuildGrowthOverview()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, overview)
}

// GetGrowthUsers 分页返回账户 + 分层派生信息。
func GetGrowthUsers(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	rows, total, err := model.ListGrowthBoardPage(
		c.Query("tier"),
		c.Query("keyword"),
		c.Query("sort_by"),
		c.Query("sort_order") != "asc",
		pageInfo.GetStartIdx(),
		pageInfo.GetPageSize(),
	)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(rows)
	common.ApiSuccess(c, pageInfo)
}

// GetGrowthAlerts 返回预警队列。队列是实时算的，不落库；
// 「已处理/已忽略」保存在用户聚合行的 alert_state 上。
func GetGrowthAlerts(c *gin.Context) {
	all, err := service.BuildGrowthAlerts(true)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	wantClosed := c.Query("status") == "all"
	items := make([]model.GrowthAlertItem, 0, len(all))
	stats := map[string]gin.H{}
	for _, a := range all {
		s, ok := stats[a.Kind]
		if !ok {
			s = gin.H{"kind": a.Kind, "total": 0, "resolved": 0}
		}
		s["total"] = s["total"].(int) + 1
		if a.Status != model.GrowthAlertOpen {
			s["resolved"] = s["resolved"].(int) + 1
		}
		stats[a.Kind] = s
		if wantClosed || a.Status == model.GrowthAlertOpen {
			items = append(items, a)
		}
	}
	list := make([]gin.H, 0, len(stats))
	for _, s := range stats {
		list = append(list, s)
	}
	// 到期未跟进的承诺单独给出：它们不进预警队列，但同样是今天的待办，
	// 漏掉等于让运营口头答应的事消失。
	dueFollowUps, _ := model.ListDueFollowUps(time.Now().Unix(), 50)
	common.ApiSuccess(c, gin.H{"items": items, "stats": list, "follow_ups": dueFollowUps})
}

// UpdateGrowthAlertStatus 标记预警状态。预警不落库，所以用 user_id + kind 定位。
func UpdateGrowthAlertStatus(c *gin.Context) {
	userId, err := strconv.Atoi(c.Param("userId"))
	if err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	kind := c.Param("kind")
	var body struct {
		Status string `json:"status"`
	}
	if err := common.DecodeJson(c.Request.Body, &body); err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	if body.Status != model.GrowthAlertDone && body.Status != model.GrowthAlertIgnore && body.Status != model.GrowthAlertOpen {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	if err := model.SetAlertMark(userId, kind, body.Status); err != nil {
		common.ApiError(c, err)
		return
	}
	// 队列里点「已处理」必须落一条触达记录。
	// 否则 7 天抑制不生效——运营刚打完电话，客户第二天还会收到同一件事的自动邮件。
	// 这类「人工与自动打架」是运营系统最常见的信任崩塌方式。
	if body.Status == model.GrowthAlertDone {
		now := time.Now().Unix()
		touch := &model.GrowthTouch{
			UserId:       userId,
			Method:       "alert_ack",
			Feedback:     "在今日队列标记为已处理：" + kind,
			Conclusion:   "handled",
			OperatorId:   c.GetInt("id"),
			OperatorName: c.GetString("username"),
		}
		if err := model.CreateGrowthTouch(touch); err != nil {
			common.ApiError(c, err)
			return
		}
		_ = model.MarkBoardTouched(userId, now)
	}
	common.ApiSuccess(c, nil)
}

// GetGrowthProfile 返回客户档案：账户、分层、窗口消耗、时间线、触达记录。
func GetGrowthProfile(c *gin.Context) {
	userId, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	user, err := model.GetUserById(userId, false)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	stat, _ := model.GetGrowthBoardRow(userId)
	touches, _ := model.ListGrowthTouches(userId, 50)
	allAlerts, _ := service.BuildGrowthAlerts(true)
	mine := make([]model.GrowthAlertItem, 0, 4)
	for _, a := range allAlerts {
		if a.UserId == userId {
			mine = append(mine, a)
		}
	}
	timeline := model.BuildGrowthTimeline(user, stat, mine, touches)

	common.ApiSuccess(c, gin.H{
		"user": gin.H{
			"id":           user.Id,
			"username":     user.Username,
			"display_name": user.DisplayName,
			"quota":        user.Quota,
			"status":       user.Status,
			"created_at":   user.CreatedAt,
			"remark":       user.Remark,
		},
		"stat":     stat,
		"alerts":   mine,
		"timeline": timeline,
		"touches":  touches,
	})
}

// CreateGrowthTouch 追加一条触达记录。
func CreateGrowthTouch(c *gin.Context) {
	userId, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	var body struct {
		Method      string `json:"method"`
		ContactRole string `json:"contact_role"`
		Feedback    string `json:"feedback"`
		Conclusion  string `json:"conclusion"`
		FollowUpAt  int64  `json:"follow_up_at"`
	}
	if err := common.DecodeJson(c.Request.Body, &body); err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	if body.Method == "" || body.Feedback == "" {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	touch := &model.GrowthTouch{
		UserId:       userId,
		Method:       body.Method,
		ContactRole:  body.ContactRole,
		Feedback:     body.Feedback,
		Conclusion:   body.Conclusion,
		FollowUpAt:   body.FollowUpAt,
		OperatorId:   c.GetInt("id"),
		OperatorName: c.GetString("username"),
	}
	if err := model.CreateGrowthTouch(touch); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, touch)
}

// UpdateGrowthUserTier 设置人工层级覆盖。
func UpdateGrowthUserTier(c *gin.Context) {
	userId, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	var body struct {
		Tier   string `json:"tier"`
		Reason string `json:"reason"`
	}
	if err := common.DecodeJson(c.Request.Body, &body); err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	switch body.Tier {
	case "", model.GrowthTierKey, model.GrowthTierActive, model.GrowthTierLong, model.GrowthTierSilent:
	default:
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	if err := model.SetManualTier(userId, body.Tier, body.Reason); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}

// GetGrowthBatches 返回发放批次。
func GetGrowthBatches(c *gin.Context) {
	rows, err := model.ListGrowthBatches(100)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, rows)
}

// CreateGrowthBatch 创建批次并快照目标人群。
func CreateGrowthBatch(c *gin.Context) {
	var body struct {
		Name          string `json:"name"`
		Criteria      string `json:"criteria"`
		BenefitType   string `json:"benefit_type"`
		BenefitAmount int    `json:"benefit_amount"`
		WindowDays    int    `json:"window_days"`
		Tier          string `json:"tier"`
	}
	if err := common.DecodeJson(c.Request.Body, &body); err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	if body.WindowDays <= 0 {
		body.WindowDays = 14
	}
	ids, err := model.ListUserIdsByTier(body.Tier)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if len(ids) == 0 {
		common.ApiErrorT(c, "No users matched the selected segment")
		return
	}
	batch := &model.GrowthBatch{
		BatchNo:       "B-" + time.Now().Format("0102-150405"),
		Name:          body.Name,
		Criteria:      body.Criteria,
		BenefitType:   body.BenefitType,
		BenefitAmount: body.BenefitAmount,
		TargetCount:   len(ids),
		TargetUserIds: model.JoinUserIds(ids),
		Cost:          body.BenefitAmount * len(ids),
		WindowDays:    body.WindowDays,
		Status:        "running",
		StartedAt:     time.Now().Unix(),
	}
	if err := model.CreateGrowthBatch(batch); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, batch)
}

// GetGrowthModels 返回近 30 天各模型的消耗、用户数与请求数。
func GetGrowthModels(c *gin.Context) {
	stats, err := model.ScanModelBreakdown(c.Request.Context(), service.DayStart(-29))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	total := 0
	for _, m := range stats {
		total += m.Quota
	}
	out := make([]gin.H, 0, len(stats))
	for _, m := range stats {
		share := 0.0
		if total > 0 {
			share = float64(m.Quota) / float64(total) * 100
		}
		out = append(out, gin.H{
			"model_name": m.ModelName,
			"quota":      m.Quota,
			"usd":        service.QuotaToUSD(m.Quota),
			"users":      m.Users,
			"requests":   m.Requests,
			"share_pct":  share,
		})
	}
	common.ApiSuccess(c, out)
}

// GetGrowthSettings 返回分层与预警阈值。
func GetGrowthSettings(c *gin.Context) {
	common.ApiSuccess(c, service.LoadGrowthSettings())
}

// UpdateGrowthSettings 保存阈值。越界值会被夹回合法区间后返回。
func UpdateGrowthSettings(c *gin.Context) {
	var body service.GrowthSettings
	if err := common.DecodeJson(c.Request.Body, &body); err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	saved, err := service.SaveGrowthSettings(body)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, saved)
}

// StopGrowthBatch 手动停投一个批次。已结算的数据保留。
func StopGrowthBatch(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	if err := model.StopGrowthBatch(id); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}

// RecomputeGrowth 手动触发重算，供运营在页面上一键刷新。
func RecomputeGrowth(c *gin.Context) {
	go func() {
		if err := service.RefreshGrowthStats(); err != nil {
			common.SysLog(common.LogText("growth manual refresh failed: %s", err.Error()))
		}
		if err := service.SettleGrowthBatches(); err != nil {
			common.SysLog(common.LogText("growth manual settle failed: %s", err.Error()))
		}
	}()
	common.ApiSuccess(c, gin.H{"started": true})
}

/* APIDUO:growth:END */
