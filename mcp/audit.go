package mcp

// 控制面写操作的精细审计（APIDUO:mcp:audit）。
//
// 为什么需要这一层：鉴权链路里已经有兜底审计（middleware/audit.go 的
// finishAdminAudit），任何经过 AdminAuth 的 POST/PUT/DELETE 都会留一条。
// 但兜底只能看到「方法 + 路由」，action 一律是 generic，params 里没有
// 任何业务字段。生产实测（2026-11-10）这几条长这样：
//
//	action=generic  content=DELETE /api/mcp/profiles/:id
//
// 于是「谁把哪个模型的参考图上限从 3 改成 0」这种问题在审计里查不出来 ——
// 记了等于没记。这一层把 action 落到具体动作，并把改动前后的值写进 params。
//
// 与兜底的配合：记完设置 ContextKeyAuditLogged，finishAdminAudit 据此跳过，
// 不会出现一条 generic 加一条精细的重复记录。

import (
	"reflect"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// 控制面动作名。命名沿用既有风格（option.batch_update / token.status_update）：
// 资源在前、动作在后，下划线连接。
const (
	auditActionProfileCreate = "mcp.profile_create"
	auditActionProfileUpdate = "mcp.profile_update"
	auditActionProfileDelete = "mcp.profile_delete"
	auditActionProfileToggle = "mcp.profile_toggle"
	// 冲突不是「保存失败」，是「有人想覆盖别人的改动」——安全审计里这是唯一证据，
	// 必须单独一个 action，否则会淹没在参数校验失败里。
	auditActionProfileConflict = "mcp.profile_conflict"
	auditActionErrorRule       = "mcp.error_rule_update"
	// 采纳巡检建议是一次真实的声明变更，必须和手工改参数区分开：
	// 事后追「这个上限是谁定的」时，答案可能是「巡检建议 + 人点了采纳」。
	auditActionProfileAdopt = "mcp.profile_adopt"
)

// auditHintLimit 限制 hint 在审计里保留的长度。
// hint 是给 agent 的提示文案，可能很长；审计要的是「改过」这个事实和它的
// 大致内容，不是完整副本。超长截断并标出省略，避免审计表被单条记录撑大。
const auditHintLimit = 200

// recordControlAudit 记一条控制面审计，并抑制鉴权链路的 generic 兜底。
//
// token_ref 不在这里填：RecordAuditLog 已经统一从鉴权上下文回填，
// 操作审计与 access_token 审计共用一条归属链路。此处只管业务字段。
//
// status 省略时按成功记。拒绝路径（如版本冲突）必须显式给出：handler 是在
// 写响应之前调用的，c.Writer.Status() 此刻还是 200，照抄会把 409 记成成功。
func recordControlAudit(c *gin.Context, action, content string, params map[string]any, status ...int) {
	if params == nil {
		params = map[string]any{}
	}
	agent := middleware.AttributeAgent(c)
	authMethod := "session"
	if c.GetBool("use_access_token") {
		authMethod = "access_token"
	}
	operatorId := c.GetInt("id")
	operatorRole := c.GetInt("role")
	adminInfo := &model.AuditAdminInfo{
		AdminID:       operatorId,
		AdminUsername: c.GetString("username"),
		AdminRole:     operatorRole,
		AuthMethod:    authMethod,
		// Agent 名与来源只用于人工区分，不参与鉴权（与 controller 侧同源）。
		Agent:       agent.Label,
		AgentSource: agent.Source,
	}
	// 只有被拒绝的写入才带 request 详情：成功路径的 method/route/status
	// 与列表列重复，塞进去只会让详情多一屏噪音。
	var auditInfo *model.AuditRequestInfo
	if len(status) > 0 {
		auditInfo = &model.AuditRequestInfo{
			Method:  c.Request.Method,
			Route:   c.FullPath(),
			Path:    c.Request.URL.Path,
			Status:  status[0],
			Success: status[0] < 400,
		}
	}
	model.RecordOperationAuditLog(operatorId, operatorRole, content, c.ClientIP(), action, params, adminInfo, auditInfo, c)
	common.SetContextKey(c, constant.ContextKeyAuditLogged, true)
}

// profileAuditSnapshot 取能力声明里值得留痕的字段。
//
// 三态字段（max_ref_images / max_ref_videos / max_prompt_chars）保留「未声明」
// 与 0 的区别：nil 记成 nil，0 记成 0。把两者混为一谈会让审计看不出
// 「从没限制过」和「明确禁止垫图」的区别，而这正是 MCP 最容易搞错的语义。
func profileAuditSnapshot(p ModelProfile) map[string]any {
	return map[string]any{
		"display_name":     p.DisplayName,
		"kind":             p.Kind,
		"async":            p.Async,
		"enabled":          p.Enabled,
		"max_ref_images":   intOrNil(p.MaxRefImages),
		"max_ref_videos":   intOrNil(p.MaxRefVideos),
		"max_prompt_chars": intOrNil(p.MaxPromptChars),
		"parameters":       p.Parameters,
		"summary":          p.Summary,
		"tags":             p.Tags,
	}
}

func errorRuleAuditSnapshot(r ErrorRule) map[string]any {
	return map[string]any{
		"match":          r.Match,
		"retryable":      r.Retryable,
		"retry_action":   r.RetryAction,
		"retry_after_ms": r.RetryAfterMs,
		"enabled":        r.Enabled,
		"sort":           r.Sort,
		"hint":           clipHint(r.Hint),
	}
}

func intOrNil(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

func clipHint(s string) string {
	if utf8.RuneCountInString(s) <= auditHintLimit {
		return s
	}
	runes := []rune(s)
	return string(runes[:auditHintLimit]) + "…"
}

// auditDiff 只返回真正变化的字段，形如
// {"max_ref_images": {"before": 3, "after": 0}}。
//
// 只记变化项而不是整份快照：一份声明十几个字段，审计列表里铺满未改动的值
// 会淹掉真正的改动。键顺序无序、由 JSON 序列化保证稳定输出。
func auditDiff(before, after map[string]any) map[string]any {
	changed := map[string]any{}
	for key, newValue := range after {
		oldValue, existed := before[key]
		if existed && reflect.DeepEqual(oldValue, newValue) {
			continue
		}
		changed[key] = map[string]any{"before": oldValue, "after": newValue}
	}
	return changed
}
