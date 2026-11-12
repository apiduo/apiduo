package mcp

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

// ParamStat 是一个参数取值的观察结果：成功几次、被拒几次。
type ParamStat struct {
	Param string `json:"param"`
	Value string `json:"value"`
	OK    int64  `json:"ok"`
	Fail  int64  `json:"fail"`
}

// ParamSpec 是受控参数的约束。enum/min/max/default 是够用的最小公共子集，
// 与 MCP 契约 v4 的 paramSpec 一致，避免每个客户端实现一套 JSON Schema 解释器。
type ParamSpec struct {
	Enum    []any    `json:"enum,omitempty"`
	Min     *float64 `json:"min,omitempty"`
	Max     *float64 `json:"max,omitempty"`
	Default any      `json:"default,omitempty"`
}

// paramNameRe 限制参数名，因为它要拼进 SQL 路径。
// 允许的名字来自白名单或管理员填的 JSON key，但仍然必须校验 ——
// 拼进 SQL 的东西不能信任来源。
var paramNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// Observations 汇总某个模型近 days 天的参数分布。
//
// 数据源是 model.LOG_DB（线上为 ClickHouse），不新建表：
// logs.other 里的 admin_info.param_snapshot 就是 MCP 协议层写入的快照，
// 只读它。查询必须带 model_name 与时间范围 —— CH 靠分区裁剪，漏了范围会扫全表。
func Observations(p ModelProfile, days int) ([]ParamStat, error) {
	if model.LOG_DB == nil {
		return nil, userErrf("日志库未初始化，观察与巡检暂不可用")
	}
	if days <= 0 || days > 90 {
		days = 7
	}
	since := time.Now().AddDate(0, 0, -days).Unix()

	out := make([]ParamStat, 0, 16)
	for _, name := range ParamNames(p) {
		if !paramNameRe.MatchString(name) {
			continue
		}
		stats, err := queryParam(model.LOG_DB, p.ModelId, name, since)
		if err != nil {
			return nil, fmt.Errorf("查询参数 %s 失败: %w", name, err)
		}
		out = append(out, stats...)
	}
	sortStats(out)
	return out, nil
}

// queryParam 按日志库方言取一个参数的取值分布。
//
// 两种方言的差异都在这一层消化，上层只认 []ParamStat：
//   - ClickHouse 用 JSONExtractString，列存 + 分区裁剪，毫秒级
//   - MySQL 用 JSON_EXTRACT，且必须先过 JSON_VALID，否则一行脏数据会让整条查询报错
func queryParam(db *gorm.DB, modelId, name string, since int64) ([]ParamStat, error) {
	// SQLite 没有 JSON_EXTRACT / JSON_UNQUOTE（标准构建不带 JSON1），
	// 而 SQLite 恰恰是 apiduo 的默认库。这条分支让「本地跑起来」和
	// 「线上 MySQL/CH」行为一致，而不是让观察功能在默认部署下直接报错。
	if db.Dialector.Name() == "sqlite" {
		return queryParamSQLite(db, modelId, name, since)
	}

	var sql string
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		sql = fmt.Sprintf(`
			SELECT JSONExtractString(other, 'admin_info', 'param_snapshot', '%s') AS value,
			       countIf(type = ?) AS ok,
			       countIf(type = ?) AS fail
			FROM logs
			WHERE model_name = ? AND created_at >= ?
			GROUP BY value`, name)
	} else {
		sql = fmt.Sprintf(`
			SELECT JSON_UNQUOTE(JSON_EXTRACT(other, '$.admin_info.param_snapshot.%s')) AS value,
			       SUM(type = ?) AS ok,
			       SUM(type = ?) AS fail
			FROM logs
			WHERE model_name = ? AND created_at >= ?
			  AND other IS NOT NULL AND other <> '' AND JSON_VALID(other)
			GROUP BY value`, name)
	}

	rows := make([]ParamStat, 0, 8)
	if err := db.Raw(sql, logTypeConsume, logTypeError, modelId, since).Scan(&rows).Error; err != nil {
		return nil, err
	}
	// 空值与字符串 "null" 都是「这次调用没传该参数」，不是一种取值。
	clean := rows[:0]
	for _, r := range rows {
		v := strings.TrimSpace(r.Value)
		if v == "" || v == "null" {
			continue
		}
		r.Value = v
		r.Param = name
		clean = append(clean, r)
	}
	return clean, nil
}

// queryParamSQLite 在应用层解析 other，绕开 SQLite 缺失的 JSON 函数。
//
// 行数上限 20000 是刻意的：SQLite 只用于单机小规模部署，全量拉取会把内存打满。
// 超出的部分按「最近优先」截断 —— 统计口径宁可少样本，也不要 OOM。
func queryParamSQLite(db *gorm.DB, modelId, name string, since int64) ([]ParamStat, error) {
	var rows []struct {
		Other string
		Type  int
	}
	err := db.Raw(
		"SELECT other, type FROM logs WHERE model_name = ? AND created_at >= ? ORDER BY id DESC LIMIT 20000",
		modelId, since,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	counts := make(map[string]*ParamStat, 8)
	for _, r := range rows {
		v := snapshotValue(r.Other, name)
		if v == "" {
			continue
		}
		st := counts[v]
		if st == nil {
			st = &ParamStat{Param: name, Value: v}
			counts[v] = st
		}
		switch r.Type {
		case logTypeConsume:
			st.OK++
		case logTypeError:
			st.Fail++
		}
	}

	out := make([]ParamStat, 0, len(counts))
	for _, st := range counts {
		out = append(out, *st)
	}
	return out, nil
}

// snapshotValue 从 other JSON 里取 param_snapshot 的一个键。
// 结构对不上就返回空串，调用方按「没传该参数」处理。
func snapshotValue(other, name string) string {
	if strings.TrimSpace(other) == "" {
		return ""
	}
	var doc struct {
		AdminInfo struct {
			ParamSnapshot map[string]any `json:"param_snapshot"`
		} `json:"admin_info"`
	}
	if err := json.Unmarshal([]byte(other), &doc); err != nil {
		return ""
	}
	v, ok := doc.AdminInfo.ParamSnapshot[name]
	if !ok {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

// 日志类型值与 model.LogTypeConsume / model.LogTypeError 对齐。
const (
	logTypeConsume = 2
	logTypeError   = 5
)

func sortStats(stats []ParamStat) {
	sort.SliceStable(stats, func(i, j int) bool {
		if stats[i].Param != stats[j].Param {
			return stats[i].Param < stats[j].Param
		}
		a, aok := asFloat(stats[i].Value)
		b, bok := asFloat(stats[j].Value)
		if aok && bok && a != b {
			return a < b
		}
		if aok != bok {
			return aok
		}
		return stats[i].Value < stats[j].Value
	})
}

func asFloat(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v, err == nil
}

// ParseSpecs 解析模型声明的参数约束。解析失败按「未声明」处理，
// 不阻断查询 —— 声明错是保存时要拦的事，不该让面板整页打不开。
func ParseSpecs(raw string) map[string]ParamSpec {
	out := map[string]ParamSpec{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	var m map[string]ParamSpec
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return out
	}
	return m
}

// AuditLevel 表示巡检结论的严重度。
type AuditLevel string

const (
	AuditConflict AuditLevel = "conflict" // 声明与观察矛盾，需要人裁决
	AuditPending  AuditLevel = "pending"  // 观察到但未声明，可以定为约束
	AuditUnprobed AuditLevel = "unprobed" // 有声明但无样本，只有文档依据
	AuditOK       AuditLevel = "ok"
)

// AuditSuggestion 是一条可以直接写回能力声明的建议。
//
// 只在「观察到了明确边界」时给出：数值参数给 max，字符串参数给 enum。
// 其余情况一律 nil —— 巡检是让人裁决，不是替人猜；猜错会把本来能用的值挡掉。
type AuditSuggestion struct {
	Param string   `json:"param"`
	Max   *float64 `json:"max,omitempty"`
	Enum  []string `json:"enum,omitempty"`
}

// AuditIssue 是一条巡检结论。
type AuditIssue struct {
	Level      AuditLevel       `json:"level"`
	Title      string           `json:"title"`
	Object     string           `json:"object"`
	Detail     string           `json:"detail"`
	Suggestion *AuditSuggestion `json:"suggestion,omitempty"`
}

// Audit 对比「声明」与「观察」，产出需要人裁决的条目。
//
// 只报不确定的东西：一致的不列，避免巡检变成一张永远全绿的表 ——
// 全绿的表没人看第二次。
func Audit(p ModelProfile, stats []ParamStat) []AuditIssue {
	issues := make([]AuditIssue, 0, 8)
	specs := ParseSpecs(p.Parameters)

	byParam := map[string][]ParamStat{}
	for _, s := range stats {
		byParam[s.Param] = append(byParam[s.Param], s)
	}

	names := make([]string, 0, len(specs)+len(byParam))
	seen := map[string]bool{}
	for k := range specs {
		if !seen[k] {
			seen[k] = true
			names = append(names, k)
		}
	}
	for k := range byParam {
		if !seen[k] {
			seen[k] = true
			names = append(names, k)
		}
	}
	sort.Strings(names)

	for _, name := range names {
		spec, declared := specs[name]
		// 参考图上限不是 parameters 里的一个参数，而是 ModelProfile.MaxRefImages。
		// 不在这里对齐的话，巡检会把它当成「未声明的参数」，
		// 对着一个明明声明过上上限的模型报「观察到未声明的边界，建议定为上限 N」。
		if name == "reference_images" && p.MaxRefImages != nil {
			max := float64(*p.MaxRefImages)
			spec = ParamSpec{Max: &max}
			declared = true
		}
		rows := byParam[name]

		if len(rows) == 0 {
			if declared {
				issues = append(issues, AuditIssue{
					Level:  AuditUnprobed,
					Title:  "参数从未探测",
					Object: p.ModelId + " · " + name,
					Detail: "有声明但近 7 天无调用样本，目前只有文档依据",
				})
			}
			continue
		}

		// 声明了上限，却有不超过上限的值被上游拒绝 —— 这是声明错了。
		if declared && spec.Max != nil {
			var rejected []string
			var maxOK float64
			var hasOK bool
			for _, r := range rows {
				v, ok := asFloat(r.Value)
				if !ok {
					continue
				}
				if r.Fail > 0 && v <= *spec.Max {
					rejected = append(rejected, r.Value)
				}
				if r.OK > 0 && (!hasOK || v > maxOK) {
					maxOK, hasOK = v, true
				}
			}
			if len(rejected) > 0 {
				detail := fmt.Sprintf("声明上限 %g，但 %s 被拒过 %s；成功过 %s",
					*spec.Max, strings.Join(rejected, " / "), describeRejects(rows, rejected), describeMaxOK(rows))
				issues = append(issues, AuditIssue{
					Level:  AuditConflict,
					Title:  "声明与观察不一致",
					Object: p.ModelId + " · " + name,
					Detail: detail,
				})
				continue
			}
			if hasOK && maxOK < *spec.Max {
				issues = append(issues, AuditIssue{
					Level:  AuditPending,
					Title:  "上限可能偏大",
					Object: p.ModelId + " · " + name,
					Detail: fmt.Sprintf("声明上限 %g，但观察到最大成功值只有 %g，建议核实", *spec.Max, maxOK),
				})
				continue
			}
		}

		// 没声明上限，但观察到被拒 —— 说明上游有边界，值得固化。
		// 没声明也没被拒 —— 不报，属于「未声明即不拦截」的正常状态。
		if !declared {
			var rejected []string
			for _, r := range rows {
				if r.Fail > 0 {
					rejected = append(rejected, r.Value)
				}
			}
			if len(rejected) > 0 {
				issues = append(issues, AuditIssue{
					Level:      AuditPending,
					Title:      "观察到未声明的边界",
					Object:     p.ModelId + " · " + name,
					Detail:     fmt.Sprintf("%s 被拒，建议定为上限 %s", strings.Join(rejected, " / "), suggestMax(rows)),
					Suggestion: suggestForParam(name, rows, rejected),
				})
			}
		}
	}
	return issues
}

func describeRejects(rows []ParamStat, values []string) string {
	parts := make([]string, 0, len(values))
	for _, v := range values {
		for _, r := range rows {
			if r.Value == v {
				parts = append(parts, fmt.Sprintf("%s（%d 次）", v, r.Fail))
			}
		}
	}
	return strings.Join(parts, "、")
}

func describeMaxOK(rows []ParamStat) string {
	best := 0.0
	found := false
	for _, r := range rows {
		if r.OK == 0 {
			continue
		}
		if v, ok := asFloat(r.Value); ok && (!found || v > best) {
			best, found = v, true
		}
	}
	if !found {
		return "无"
	}
	return strconv.FormatFloat(best, 'f', -1, 64)
}

// suggestForParam 把「被拒过的取值」翻译成一条可写回的约束。
//
// 数值参数：最小被拒值减一当上限。字符串参数：成功过的取值收成枚举 ——
// 只留成功值等于「把已知会被拒的值排除掉」，比拍一个上限更贴近事实。
// 两者都拿不到（例如所有样本都被拒）就返回 nil：信息不足以定约束，
// 硬写一条只会把还没试过的合法值一起挡掉。
func suggestForParam(name string, rows []ParamStat, rejected []string) *AuditSuggestion {
	minRejected, numeric := 0.0, true
	for _, v := range rejected {
		f, ok := asFloat(v)
		if !ok {
			numeric = false
			break
		}
		if minRejected == 0 || f < minRejected {
			minRejected = f
		}
	}
	if numeric && minRejected > 0 {
		max := minRejected - 1
		return &AuditSuggestion{Param: name, Max: &max}
	}
	okValues := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.OK > 0 {
			okValues = append(okValues, r.Value)
		}
	}
	if len(okValues) > 0 {
		return &AuditSuggestion{Param: name, Enum: okValues}
	}
	return nil
}

// suggestMax 用「最小被拒值减一」推建议上限。这是保守估计：
// 只保证不会把已知被拒的值当成合法值，不代表上限就是它。
func suggestMax(rows []ParamStat) string {
	minRejected := 0.0
	found := false
	for _, r := range rows {
		if r.Fail == 0 {
			continue
		}
		if v, ok := asFloat(r.Value); ok && (!found || v < minRejected) {
			minRejected, found = v, true
		}
	}
	if !found {
		return "待核实"
	}
	return strconv.FormatFloat(minRejected-1, 'f', -1, 64)
}

// ModelCard 是给 agent 看的能力摘要。它不是新的事实来源，
// 而是把声明与观察拼成一句话 —— 少了它，agent 只能靠模型 ID 猜差别。
func ModelCard(p ModelProfile) map[string]any {
	card := map[string]any{
		"model_id": p.ModelId,
		"kind":     p.Kind,
		"protocol": p.Protocol,
		"execution": map[string]any{
			"task": map[bool]string{true: "async", false: "sync"}[p.Async],
		},
	}
	if p.Summary != "" {
		card["summary"] = p.Summary
	}
	if p.Tags != "" {
		card["tags"] = strings.Split(p.Tags, ",")
	}
	input := map[string]any{}
	if p.MaxRefImages != nil {
		input["max_reference_images"] = *p.MaxRefImages
	}
	if p.MaxRefVideos != nil {
		input["max_reference_videos"] = *p.MaxRefVideos
	}
	if p.MaxPromptChars != nil {
		input["max_prompt_chars"] = *p.MaxPromptChars
	}
	if len(input) > 0 {
		card["input"] = input
	}
	if specs := ParseSpecs(p.Parameters); len(specs) > 0 {
		card["parameters"] = specs
	}
	return card
}
