package controller

import (
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// auditLogView 在审计行上挂一个还原出来的令牌名。
//
// 不往 model.AuditLog 里加字段：那个结构直连日志库，它的形状就是表结构。
// 为了展示加一个 gorm:"-" 字段，等于让领域模型变成接口投影，往后谁都能往里塞
// 展示用的东西。这里包一层，投影归投影。
type auditLogView struct {
	*model.AuditLog
	// TokenLabel 是人类给这把钥匙起的名字；TokenAgentKey 是它背后的 agent key。
	// 两者都为空表示令牌已被删除 —— 审计还在，钥匙没了，这本身是有效信息。
	TokenLabel    string `json:"token_label,omitempty"`
	TokenAgentKey string `json:"token_agent_key,omitempty"`
}

// attachAuditTokenLabels 给整页审计补令牌名。
//
// 补不上不算失败：审计本身是有效证据，令牌被删了不代表这条记录没价值。
// 查询出错只写服务端日志，宁可少一个可读名，也不要让审计页整页打不开 ——
// 这个接口的可用性比它的美观更重要。
func attachAuditTokenLabels(logs []*model.AuditLog) []*auditLogView {
	refs := make([]string, 0, len(logs))
	for _, entry := range logs {
		if entry.TokenRef != "" {
			refs = append(refs, entry.TokenRef)
		}
	}
	labels := map[string]model.TokenRefLabel{}
	if len(refs) > 0 {
		resolved, err := model.AuditTokenLabels(refs)
		if err != nil {
			common.SysError("[audit] 还原令牌名失败，本页按指纹展示: " + err.Error())
		} else {
			labels = resolved
		}
	}

	views := make([]*auditLogView, 0, len(logs))
	for _, entry := range logs {
		view := &auditLogView{AuditLog: entry}
		if label, ok := labels[entry.TokenRef]; ok {
			view.TokenLabel = label.Name
			view.TokenAgentKey = label.AgentKey
		}
		views = append(views, view)
	}
	return views
}

// GetAuditTokenOptions 给审计筛选下拉提供可选的令牌。
//
// 走 UserAuth 而不是 AdminAuth：普通用户在 /audit/self 里也要能筛自己的令牌。
// 可见范围在查询条件里按角色收窄 —— 不是「全查回来再过滤」，那等于把别人的
// 令牌名先读进内存，日志里也会留下痕迹。
func GetAuditTokenOptions(c *gin.Context) {
	limit := 200
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > 500 {
			common.ApiErrorMsg(c, "limit 必须是 1 到 500 之间的整数")
			return
		}
		limit = parsed
	}

	options, err := model.AuditTokenOptions(
		c.GetInt("id"),
		c.GetInt("role") >= common.RoleAdminUser,
		c.Query("q"),
		limit,
	)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	// truncated 用「拿满了 limit」推断：精确总数要再来一次 COUNT，
	// 而下拉只需要知道「还有更多，换关键字搜」——够用就别多打一次库。
	common.ApiSuccess(c, gin.H{"items": options, "truncated": len(options) == limit})
}
