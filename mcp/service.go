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
	// Note 是给 agent 读的一句约束说明，随 models.list 下发、并在参数被拒时附在错误里。
	//
	// 有些边界表达不进 enum：「1080P 这个档位存在，但当前号池开不了」。
	// 只列可选值会让 agent 以为声明漏了，于是换个措辞继续试探同一个方向；
	// 写清原因，它才会换模型或接受 720P。
	Note string `json:"note,omitempty"`
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

	names := make([]string, 0, 8)
	for _, name := range ParamNames(p) {
		// 参数名要拼进 JSON 路径，来源不可信就必须校验。
		if paramNameRe.MatchString(name) {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	out, err := queryParams(model.LOG_DB, p.ModelId, names, since)
	if err != nil {
		return nil, err
	}
	sortStats(out)
	return out, nil
}

// queryParams 取出所有声明参数的取值分布。
//
// 每个参数一条 SQL 会把同一段日志扫 N 遍（N = 声明参数个数）。线上 ClickHouse
// 用 ARRAY JOIN 一次展开全部键；SQLite 本来就在应用层解析，顺手一起计数；
// MySQL 低版本没有 JSON_TABLE，只能按参数逐条查 —— 参数个数是个位数，可接受。
func queryParams(db *gorm.DB, modelId string, names []string, since int64) ([]ParamStat, error) {
	switch {
	case db.Dialector.Name() == "sqlite":
		return queryParamsSQLite(db, modelId, names, since)
	case common.UsingLogDatabase(common.DatabaseTypeClickHouse):
		return queryParamsClickHouse(db, modelId, names, since)
	default:
		return queryParamsMySQL(db, modelId, names, since)
	}
}

// queryParamsClickHouse 用一条 SQL 展开全部参数。
//
// 分组口径与逐参数查询完全一致：2026-11-12 拿线上 flux-3-image 的 7 天数据
// 逐行比对过，10 行结果一字不差。压缩的只是扫描次数，不是统计规则。
func queryParamsClickHouse(db *gorm.DB, modelId string, names []string, since int64) ([]ParamStat, error) {
	const sql = `
		SELECT param,
		       JSONExtractString(snap, param) AS value,
		       countIf(type = ?) AS ok,
		       countIf(type = ? AND NOT (
		           position(err, '"content"') > 0
		           OR position(err, concat(char(39), 'content', char(39))) > 0
		           OR (position(err, '未声明参数') > 0
		               AND position(err, concat('未声明参数 "', param, '"')) = 0)
		       )) AS fail
		FROM (
		    SELECT type,
		           JSONExtractString(other, 'admin_info', 'param_snapshot') AS snap,
		           JSONExtractString(other, 'admin_info', 'upstream_error') AS err
		    FROM logs
		    WHERE model_name = ? AND created_at >= ?
		)
		ARRAY JOIN JSONExtractKeys(snap) AS param
		GROUP BY param, value`
	rows := make([]ParamStat, 0, 16)
	if err := db.Raw(sql, logTypeConsume, logTypeError, modelId, since).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return cleanStats(rows, names), nil
}

// queryParamsMySQL 按参数逐条查。
//
// 一条 SQL 展开 JSON 键需要 JSON_TABLE（MySQL 8.0+），低版本没有；
// 参数个数是个位数，多扫几次换兼容性，值得。
func queryParamsMySQL(db *gorm.DB, modelId string, names []string, since int64) ([]ParamStat, error) {
	out := make([]ParamStat, 0, 16)
	for _, name := range names {
		sql := fmt.Sprintf(`
			SELECT JSON_UNQUOTE(JSON_EXTRACT(other, '$.admin_info.param_snapshot.%s')) AS value,
			       SUM(type = ?) AS ok,
			       SUM(type = ? AND NOT (
			           IFNULL(LOCATE('"content"',
			               JSON_UNQUOTE(JSON_EXTRACT(other, '$.admin_info.upstream_error'))), 0) > 0
			           OR IFNULL(LOCATE(CONCAT(CHAR(39), 'content', CHAR(39)),
			               JSON_UNQUOTE(JSON_EXTRACT(other, '$.admin_info.upstream_error'))), 0) > 0
			           OR (IFNULL(LOCATE('未声明参数',
			               JSON_UNQUOTE(JSON_EXTRACT(other, '$.admin_info.upstream_error'))), 0) > 0
			               AND IFNULL(LOCATE(CONCAT('未声明参数 "', '%s', '"'),
			               JSON_UNQUOTE(JSON_EXTRACT(other, '$.admin_info.upstream_error'))), 0) = 0)
			       )) AS fail
			FROM logs
			WHERE model_name = ? AND created_at >= ?
			  AND other IS NOT NULL AND other <> '' AND JSON_VALID(other)
			GROUP BY value`, name, name)
		rows := make([]ParamStat, 0, 8)
		if err := db.Raw(sql, logTypeConsume, logTypeError, modelId, since).Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("查询参数 %s 失败: %w", name, err)
		}
		out = append(out, cleanStats(rows, []string{name})...)
	}
	return out, nil
}

// queryParamsSQLite 在应用层解析 other，绕开 SQLite 缺失的 JSON 函数。
//
// 行数上限 20000 是刻意的：SQLite 只用于单机小规模部署，全量拉取会把内存打满。
// 超出的部分按「最近优先」截断 —— 统计口径宁可少样本，也不要 OOM。
// 解析一次 other 就把所有参数都算上，不再每个参数重扫一遍。
func queryParamsSQLite(db *gorm.DB, modelId string, names []string, since int64) ([]ParamStat, error) {
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

	counts := make(map[string]map[string]*ParamStat, len(names))
	for _, r := range rows {
		snap, errText := snapshotOf(r.Other)
		if len(snap) == 0 {
			continue
		}
		for _, name := range names {
			v := snap[name]
			if v == "" {
				continue
			}
			per := counts[name]
			if per == nil {
				per = make(map[string]*ParamStat, 4)
				counts[name] = per
			}
			st := per[v]
			if st == nil {
				st = &ParamStat{Param: name, Value: v}
				per[v] = st
			}
			switch r.Type {
			case logTypeConsume:
				st.OK++
			case logTypeError:
				if blamesParam(errText, name) {
					st.Fail++
				}
			}
		}
	}

	out := make([]ParamStat, 0, 16)
	for _, per := range counts {
		for _, st := range per {
			out = append(out, *st)
		}
	}
	return out, nil
}

// cleanStats 只保留声明过的参数，并丢掉「这次没传」的空值。
//
// ClickHouse 那条 SQL 会把快照里出现的所有键都返回（包括没声明的），
// 过滤放在 Go 里而不是 SQL 的 IN 列表：参数个数少，SQL 更短。
func cleanStats(rows []ParamStat, names []string) []ParamStat {
	allowed := make(map[string]bool, len(names))
	for _, n := range names {
		allowed[n] = true
	}
	out := rows[:0]
	for _, r := range rows {
		if !allowed[r.Param] {
			continue
		}
		v := strings.TrimSpace(r.Value)
		// 空值与字符串 "null" 都是「这次调用没传该参数」，不是一种取值。
		if v == "" || v == "null" {
			continue
		}
		r.Value = v
		out = append(out, r)
	}
	return out
}

// snapshotOf 解析一次 other，同时给出参数快照与错误文案。
//
// SQLite 路径要按行遍历所有参数，逐参数再 Unmarshal 一遍纯属浪费；
// 结构对不上就返回空，调用方按「这条日志没有快照」处理。
func snapshotOf(other string) (map[string]string, string) {
	if strings.TrimSpace(other) == "" {
		return nil, ""
	}
	var doc struct {
		AdminInfo struct {
			ParamSnapshot map[string]any `json:"param_snapshot"`
			UpstreamError string         `json:"upstream_error"`
		} `json:"admin_info"`
	}
	if err := json.Unmarshal([]byte(other), &doc); err != nil {
		return nil, ""
	}
	vals := make(map[string]string, len(doc.AdminInfo.ParamSnapshot))
	for k, v := range doc.AdminInfo.ParamSnapshot {
		vals[k] = strings.TrimSpace(fmt.Sprint(v))
	}
	return vals, doc.AdminInfo.UpstreamError
}

// blamesParam 判断一次失败该不该记在当前参数头上。
//
// 本地参数校验失败时，错误文案会点名一个参数（模型 X 未声明参数 "content"）。
// 这种失败只跟被点名的参数有关，不能算到同一次调用快照里的其它参数上 ——
// 否则 agent 传真了 content，reference_images 会平白多一次「被拒」，
// 巡检就会报出「声明 9 却 1 被拒」这种自相矛盾的结论。
//
// 没有点名任何参数的失败（上游 4xx/5xx）仍然算数：那才是「这个值可能不被接受」
// 的证据，把它一起排掉就等于把巡检的输入掐了。
func blamesParam(errText, name string) bool {
	if strings.TrimSpace(errText) == "" {
		return true
	}
	// 本地校验失败会点名一个参数：点名了谁就只记给谁。
	if strings.Contains(errText, "未声明参数") {
		return strings.Contains(errText, `未声明参数 "`+name+`"`)
	}
	// 上游的信封类错误（'content' 必须是非空数组）：content 是请求体结构，
	// 不是快照里的可调参数，记到 duration / reference_images 头上就是冤枉。
	if strings.Contains(errText, `"content"`) || strings.Contains(errText, `'content'`) {
		return false
	}
	return true
}

// snapshotError 取日志里的 upstream_error 原文。
func snapshotError(other string) string {
	if strings.TrimSpace(other) == "" {
		return ""
	}
	var doc struct {
		AdminInfo struct {
			UpstreamError string `json:"upstream_error"`
		} `json:"admin_info"`
	}
	if err := json.Unmarshal([]byte(other), &doc); err != nil {
		return ""
	}
	return doc.AdminInfo.UpstreamError
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
		"execution": map[string]any{
			"task": map[bool]string{true: "async", false: "sync"}[p.Async],
		},
	}
	if p.Summary != "" {
		card["summary"] = p.Summary
	}
	if p.Cost != "" {
		card["cost"] = p.Cost
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
