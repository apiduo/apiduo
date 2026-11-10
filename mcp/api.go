package mcp

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// profileView 是列表/详情的出参。
//
// 不直接回 ModelProfile：页面需要 model_card 这种派生视图，
// 但它不该被当成第二个事实来源，所以只在响应里拼，不落库。
type profileView struct {
	ModelProfile
	ModelCard map[string]any `json:"model_card,omitempty"`
}

// apiWriteErr 是控制面的统一错误出口。
//
// 只有两类错误能原样回前端：本包写的 userErr（「产物类型不合法」这类），
// 以及两个已知哨兵（不存在 / 已存在）。其余一律当内部错误处理 ——
// 驱动返回的原文里带表名、索引名，甚至约束里的值，回出去既泄露结构，
// 又给不出「该怎么办」。原文落 SysError，排障去日志里翻。
func apiWriteErr(c *gin.Context, action string, err error) {
	var ue userErr
	switch {
	case errors.As(err, &ue):
		common.ApiErrorMsg(c, string(ue))
	case errors.Is(err, ErrProfileNotFound), errors.Is(err, ErrProfileDuplicate):
		common.ApiErrorMsg(c, err.Error())
	default:
		common.SysError("[mcp] " + action + " 失败: " + err.Error())
		common.ApiErrorMsg(c, action+"失败：内部错误，详情见网关日志")
	}
}

func toView(p ModelProfile, withCard bool) profileView {
	v := profileView{ModelProfile: p}
	if withCard {
		v.ModelCard = ModelCard(p)
	}
	return v
}

func apiListProfiles(c *gin.Context) {
	list, err := ListProfiles()
	if err != nil {
		apiWriteErr(c, "读取能力声明", err)
		return
	}
	out := make([]profileView, 0, len(list))
	for _, p := range list {
		out = append(out, toView(p, false))
	}
	common.ApiSuccess(c, gin.H{"items": out, "total": len(out)})
}

func apiGetProfile(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	p, err := GetProfile(id)
	if err != nil {
		apiWriteErr(c, "读取能力声明", err)
		return
	}
	common.ApiSuccess(c, toView(p, true))
}

func apiCreateProfile(c *gin.Context) {
	var p ModelProfile
	if err := c.ShouldBindJSON(&p); err != nil {
		common.ApiErrorMsg(c, "请求体不是合法 JSON: "+err.Error())
		return
	}
	p.Id = 0
	if err := SaveProfile(&p); err != nil {
		apiWriteErr(c, "保存能力声明", err)
		return
	}
	recordControlAudit(c, auditActionProfileCreate,
		"Created MCP model profile "+p.ModelId,
		map[string]any{"model_id": p.ModelId, "after": profileAuditSnapshot(p)})
	common.ApiSuccess(c, toView(p, true))
}

func apiUpdateProfile(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	// 用键集合而不是完整对象：只改请求里出现的字段，没出现的保持原值。
	// 面板改一个显示名却要重传整份声明，任何漏传的 bool 都会被零值覆盖。
	var patch map[string]json.RawMessage
	if err := c.ShouldBindJSON(&patch); err != nil {
		common.ApiErrorMsg(c, "请求体不是合法 JSON: "+err.Error())
		return
	}
	// 先取旧值：审计要记「从什么改成什么」，而 UpdateProfile 只回新值。
	before, err := GetProfile(id)
	if err != nil {
		apiWriteErr(c, "读取能力声明", err)
		return
	}
	p, err := UpdateProfile(id, patch)
	if err != nil {
		apiWriteErr(c, "保存能力声明", err)
		return
	}
	changed := auditDiff(profileAuditSnapshot(before), profileAuditSnapshot(p))
	recordControlAudit(c, auditActionProfileUpdate,
		"Updated MCP model profile "+p.ModelId,
		map[string]any{"model_id": p.ModelId, "changed": changed})
	common.ApiSuccess(c, toView(p, true))
}

func apiDeleteProfile(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	// 删除后 model_id 就查不回来了，旧值必须在删除前取。
	before, err := GetProfile(id)
	if err != nil {
		apiWriteErr(c, "删除能力声明", err)
		return
	}
	if err := DeleteProfile(id); err != nil {
		apiWriteErr(c, "删除能力声明", err)
		return
	}
	recordControlAudit(c, auditActionProfileDelete,
		"Deleted MCP model profile "+before.ModelId,
		map[string]any{"model_id": before.ModelId, "before": profileAuditSnapshot(before)})
	common.ApiSuccess(c, gin.H{"deleted": id})
}

// apiToggleProfile 只翻转 enabled，避免页面为了启停重传整份声明。
// 整份重传的风险是「页面打开很久，把别人刚改的字段覆盖回去」。
func apiToggleProfile(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		common.ApiErrorMsg(c, "请求体不是合法 JSON: "+err.Error())
		return
	}
	before, err := GetProfile(id)
	if err != nil {
		apiWriteErr(c, "切换模型启停", err)
		return
	}
	if err := SetProfileEnabled(id, body.Enabled); err != nil {
		apiWriteErr(c, "切换模型启停", err)
		return
	}
	p, err := GetProfile(id)
	if err != nil {
		apiWriteErr(c, "读取能力声明", err)
		return
	}
	verb := "Disabled"
	if p.Enabled {
		verb = "Enabled"
	}
	recordControlAudit(c, auditActionProfileToggle,
		verb+" MCP model profile "+p.ModelId,
		map[string]any{
			"model_id": p.ModelId,
			"enabled":  map[string]any{"before": before.Enabled, "after": p.Enabled},
		})
	common.ApiSuccess(c, toView(p, true))
}

func apiListErrorRules(c *gin.Context) {
	modelId := strings.TrimSpace(c.Query("model_id"))
	rules, err := ListErrorRules(modelId)
	if err != nil {
		apiWriteErr(c, "读取错误规则", err)
		return
	}
	common.ApiSuccess(c, gin.H{"items": rules, "total": len(rules)})
}

func apiUpdateErrorRule(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var patch map[string]json.RawMessage
	if err := c.ShouldBindJSON(&patch); err != nil {
		common.ApiErrorMsg(c, "请求体不是合法 JSON: "+err.Error())
		return
	}
	before, err := GetErrorRule(id)
	if err != nil {
		apiWriteErr(c, "读取错误规则", err)
		return
	}
	r, err := UpdateErrorRule(id, patch)
	if err != nil {
		apiWriteErr(c, "保存错误规则", err)
		return
	}
	changed := auditDiff(errorRuleAuditSnapshot(before), errorRuleAuditSnapshot(r))
	recordControlAudit(c, auditActionErrorRule,
		"Updated MCP error rule "+r.Match,
		map[string]any{"rule_id": id, "model_id": r.ModelId, "match": r.Match, "changed": changed})
	common.ApiSuccess(c, r)
}

// apiObservations 返回参数取值分布。
func apiObservations(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	days, _ := strconv.Atoi(c.DefaultQuery("days", "7"))
	p, err := GetProfile(id)
	if err != nil {
		apiWriteErr(c, "读取能力声明", err)
		return
	}
	stats, err := Observations(p, days)
	if err != nil {
		apiWriteErr(c, "查询观察数据", err)
		return
	}
	total := int64(0)
	for _, s := range stats {
		total += s.OK + s.Fail
	}
	common.ApiSuccess(c, gin.H{
		"model_id": p.ModelId,
		"days":     days,
		"stats":    stats,
		"total":    total,
	})
}

// apiAudit 返回声明与观察的对比结论。
func apiAudit(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	days, _ := strconv.Atoi(c.DefaultQuery("days", "7"))
	p, err := GetProfile(id)
	if err != nil {
		apiWriteErr(c, "读取能力声明", err)
		return
	}
	stats, err := Observations(p, days)
	if err != nil {
		apiWriteErr(c, "查询观察数据", err)
		return
	}
	issues := Audit(p, stats)
	counts := map[string]int{}
	for _, i := range issues {
		counts[string(i.Level)]++
	}
	common.ApiSuccess(c, gin.H{
		"model_id": p.ModelId,
		"days":     days,
		"issues":   issues,
		"counts":   counts,
	})
}

// apiOverview 是页面顶部摘要。只做计数，不跑巡检 ——
// 每打开一次页面就全量巡检会把日志库打满，巡检应该是显式动作。
func apiOverview(c *gin.Context) {
	list, err := ListProfiles()
	if err != nil {
		apiWriteErr(c, "读取能力声明", err)
		return
	}
	enabled := 0
	kinds := map[string]int{}
	for _, p := range list {
		if p.Enabled {
			enabled++
		}
		kinds[p.Kind]++
	}
	common.ApiSuccess(c, gin.H{
		"profiles": len(list),
		"enabled":  enabled,
		"kinds":    kinds,
		"log_db":   logDBKind(),
	})
}

// logDBKind 告诉页面当前观察数据来自哪种日志库，避免「面板空白」时误判为功能坏了。
func logDBKind() string {
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return "clickhouse"
	}
	return "sql"
}

// apiCandidates 列出「上游有、但还没声明」的模型，用于新建时的选择器。
//
// 数据来自 model 包现有的模型查询，不额外调上游接口 —— 那要多一次往返，
// 而且拿不到用户分组视角下的真实可用集合。
func apiCandidates(c *gin.Context) {
	names := model.GetEnabledModels()
	declared, err := ListProfiles()
	if err != nil {
		apiWriteErr(c, "读取能力声明", err)
		return
	}
	known := make(map[string]bool, len(declared))
	for _, p := range declared {
		known[p.ModelId] = true
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		if !known[n] {
			out = append(out, n)
		}
	}
	common.ApiSuccess(c, gin.H{"items": out})
}

func parseID(c *gin.Context) (int64, bool) {
	raw := c.Param("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		common.ApiErrorMsg(c, "id 不合法: "+raw)
		return 0, false
	}
	return id, true
}
