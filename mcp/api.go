package mcp

import (
	"encoding/json"
	"errors"
	"net/http"
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

// apiGetProfileByModelId 按业务键读一条，读不到回 404 + code。
//
// 与 upsert 成对：agent 纳管的循环是「读当前版本 → 带版本写」，
// 只给写不给读，它就只能拉全表再筛。
func apiGetProfileByModelId(c *gin.Context) {
	modelId := strings.TrimSpace(c.Query("model_id"))
	if modelId == "" {
		common.ApiErrorMsg(c, "缺少 model_id 查询参数")
		return
	}
	p, found := GetProfileByModelId(modelId)
	if !found {
		common.ApiErrorStatus(c, http.StatusNotFound, ErrProfileNotFound, gin.H{
			"code": "MCP_PROFILE_NOT_FOUND",
		})
		return
	}
	common.ApiSuccess(c, toView(p, true))
}

// apiUpsertProfile 按 model_id 幂等写入，供 agent 纳管使用。
//
// 与 PUT /profiles/:id 的分工：那条要求调用方先知道自增 id（得先拉列表再筛），
// 这条只认业务键，一次调用完成「没有就建、有就按 patch 改」。
func apiUpsertProfile(c *gin.Context) {
	modelId := strings.TrimSpace(c.Query("model_id"))
	if modelId == "" {
		common.ApiErrorMsg(c, "缺少 model_id 查询参数")
		return
	}

	var patch map[string]json.RawMessage
	if err := c.ShouldBindJSON(&patch); err != nil {
		common.ApiErrorMsg(c, "请求体不是合法 JSON: "+err.Error())
		return
	}

	// 体里也写 model_id 是常见误用：两份不一致时必须拒绝。
	// 静默以 query 为准的话，调用方以为在改 A，实际改的是 B，而且回包还是 200。
	if raw, given := patch["model_id"]; given {
		var bodyModelId string
		if err := json.Unmarshal(raw, &bodyModelId); err != nil {
			common.ApiErrorMsg(c, "请求体里的 model_id 不是字符串")
			return
		}
		if bodyModelId = strings.TrimSpace(bodyModelId); bodyModelId != modelId {
			common.ApiErrorMsg(c, "请求体里的 model_id（"+bodyModelId+"）与查询参数（"+modelId+"）不一致")
			return
		}
	}

	// upsert 不改启停（patch 白名单里没有 enabled），但静默忽略比报错更糟：
	// agent 传 {"enabled":false} 拿到的是 enabled:true，它会以为自己停用了。
	if _, given := patch["enabled"]; given {
		common.ApiErrorMsg(c, "upsert 不改启停，请改完声明后调 POST /api/mcp/profiles/{id}/toggle")
		return
	}

	var expected *int64
	if raw := strings.TrimSpace(c.Query("expected_version")); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v < 0 {
			common.ApiErrorMsg(c, "expected_version 必须是 0 或正整数，收到 "+raw)
			return
		}
		expected = &v
	}

	before, _ := GetProfileByModelId(modelId)
	p, created, err := UpsertProfileByModelId(modelId, patch, expected)
	if err != nil {
		if errors.Is(err, ErrProfileConflict) {
			// 回 409 而不是 200+success:false：agent 需要机器可判别的「你慢了」，
			// 靠文案匹配在换句话、换语言时就失效。当前值一起带回，
			// 调用方可以直接在它上面重放 patch，不必再发一次 GET。
			current, found := GetProfileByModelId(modelId)
			payload := gin.H{"exists": found}
			// 不存在时不回一个空壳：调用方看到 exists=false 就够了，
			// 一屏零值字段只会让人以为「有一条被清空了」。
			if found {
				payload["current"] = toView(current, false)
			}
			// 冲突留痕：谁、想改哪个模型、手上是哪一版、库里是哪一版。
			// 只留一条 409 的话，审计回答不了「是不是两个 agent 在抢同一个模型」。
			conflictParams := map[string]any{"model_id": modelId, "exists": found, "via": "upsert"}
			if expected != nil {
				conflictParams["expected_version"] = *expected
			}
			if found {
				conflictParams["current_version"] = current.Version
			}
			recordControlAudit(c, auditActionProfileConflict,
				"Rejected stale MCP model profile write for "+modelId,
				conflictParams, http.StatusConflict)

			common.ApiErrorStatus(c, http.StatusConflict, err, gin.H{
				"code": "MCP_PROFILE_VERSION_CONFLICT",
				"data": payload,
			})
			return
		}
		apiWriteErr(c, "保存能力声明", err)
		return
	}

	action, content, params := auditActionProfileUpdate,
		"Updated MCP model profile "+p.ModelId,
		map[string]any{"model_id": p.ModelId, "changed": auditDiff(profileAuditSnapshot(before), profileAuditSnapshot(p))}
	if created {
		action, content, params = auditActionProfileCreate,
			"Created MCP model profile "+p.ModelId,
			map[string]any{"model_id": p.ModelId, "after": profileAuditSnapshot(p)}
	}
	// 审计里区分「走 upsert 进来的」与「页面上点保存的」：后者带 id 路径，
	// 前者只认业务键，排查时先看这一条能省一轮猜。
	params["via"] = "upsert"
	// 版本号一起记：agent 下次带 expected_version 来时，能直接对照审计里的数字，
	// 不必再去翻「那一刻它读到的是哪一版」。
	params["version"] = p.Version
	recordControlAudit(c, action, content, params)

	common.ApiSuccess(c, gin.H{"created": created, "profile": toView(p, true)})
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

// apiCandidates 列出「上游有、但还没声明」的模型，并为每个模型给出协议建议。
//
// 数据来自 model 包现有的模型查询，不额外调上游接口 —— 那要多一次往返，
// 而且拿不到用户分组视角下的真实可用集合。
//
// 判据带两类本地佐证：渠道类型的端点映射（同一模型可能挂在多个渠道上，
// 合并后再判）与模型端点（模型元数据、自定义渠道路由里声明过的
// /v1/images/generations 之类）。
// 端点缓存由定价刷新维护，这里先确保它至少算过一次：否则新实例上刚打开设置页时
// 端点证据恒为空，那条兜底判据就等于不存在。
func apiCandidates(c *gin.Context) {
	model.GetPricing()
	abilities, err := model.GetAllEnableAbilityWithChannels()
	if err != nil {
		apiWriteErr(c, "读取可用模型", err)
		return
	}
	declared, err := ListProfiles()
	if err != nil {
		apiWriteErr(c, "读取能力声明", err)
		return
	}
	known := make(map[string]bool, len(declared))
	for _, p := range declared {
		known[p.ModelId] = true
	}

	channelTypes := make(map[string][]int, len(abilities))
	order := make([]string, 0, len(abilities))
	for _, a := range abilities {
		if a.Model == "" || known[a.Model] {
			continue
		}
		if _, seen := channelTypes[a.Model]; !seen {
			order = append(order, a.Model)
		}
		channelTypes[a.Model] = append(channelTypes[a.Model], a.ChannelType)
	}

	items := make([]string, 0, len(order))
	hints := make(map[string]ProtocolHint, len(order))
	for _, modelID := range order {
		items = append(items, modelID)
		hints[modelID] = SuggestProtocol(
			modelID,
			channelTypes[modelID],
			model.GetModelSupportEndpointTypes(modelID),
		)
	}

	// items 仍然是字符串数组：旧版前端只读这一项，suggestions 是增量字段，
	// 于是后端先发版时旧页面照样能用，不会因为多了个字段白屏。
	common.ApiSuccess(c, gin.H{"items": items, "suggestions": hints})
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
