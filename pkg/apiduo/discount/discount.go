// Package discount 解析并匹配用户级模型折扣。
//
// 规则存放在 users.setting 的 apiduo_discounts 字段里，是一个有序数组：
// 数组顺序即优先级，从上往下第一条命中即停，不同规则之间不相乘。
package discount

import (
	"encoding/json"
	"strings"
)

// Rule 是一条折扣规则。
type Rule struct {
	Groups string  `json:"groups"` // 逗号分隔的组名，或 "*"
	Models string  `json:"models"` // 逗号分隔的精确模型名，或 "*"
	Rate   float64 `json:"rate"`   // 最终组倍率，严格 0 < rate < 1
	Until  int64   `json:"until"`  // 失效时刻（unix 秒），0 表示永久
}

// MaxRules 限制单用户规则数量，防止 users.setting 被撑爆。
const MaxRules = 20

// Parse 容错解析：任何非法输入都只让折扣失效，不影响用户其它设置。
func Parse(raw json.RawMessage) []Rule {
	if len(raw) == 0 {
		return nil
	}
	var rules []Rule
	if err := json.Unmarshal(raw, &rules); err != nil {
		return nil
	}
	if len(rules) > MaxRules {
		rules = rules[:MaxRules]
	}
	return rules
}

// Resolve 按数组顺序取第一条命中的规则，返回最终组倍率。
// 命中即停、不相乘；过期规则跳过。
func Resolve(rules []Rule, group, model string, now int64) (float64, bool) {
	for _, rule := range rules {
		if !rule.valid(now) || !rule.MatchGroup(group) || !rule.MatchModel(model) {
			continue
		}
		return rule.Rate, true
	}
	return 0, false
}

// Expand 把规则展开成「组 → 模型模式 → 折后倍率」，保留 "*" 作为该组的兜底价。
// 输出条目数只与规则数相关，与模型总数无关，调用方查表时先查精确名、再查 "*"。
func Expand(rules []Rule, groups []string, now int64) map[string]map[string]float64 {
	var out map[string]map[string]float64
	for _, group := range groups {
		coveredAll := false
		for _, rule := range rules {
			if coveredAll {
				break
			}
			if !rule.valid(now) || !rule.MatchGroup(group) {
				continue
			}
			for part := range strings.SplitSeq(rule.Models, ",") {
				part = strings.TrimSpace(part)
				if part == "" {
					continue
				}
				if part == "*" {
					out = setRate(out, group, "*", rule.Rate)
					coveredAll = true
					break
				}
				if _, exists := out[group][part]; exists {
					continue
				}
				out = setRate(out, group, part, rule.Rate)
			}
		}
	}
	return out
}

func setRate(out map[string]map[string]float64, group, model string, rate float64) map[string]map[string]float64 {
	if out == nil {
		out = make(map[string]map[string]float64, 2)
	}
	if out[group] == nil {
		out[group] = make(map[string]float64, 4)
	}
	out[group][model] = rate
	return out
}

// valid 判断规则在 now 时刻是否可用。
func (r Rule) valid(now int64) bool {
	if r.Until > 0 && now >= r.Until {
		return false
	}
	return r.Rate > 0 && r.Rate < 1
}

// MatchGroup 判断规则的分组列是否命中该组。
func (r Rule) MatchGroup(group string) bool { return matchAny(r.Groups, group) }

// MatchModel 判断规则的模型列是否命中该模型。
func (r Rule) MatchModel(model string) bool { return matchAny(r.Models, model) }

// matchAny 判断逗号分隔的候选里是否命中目标；"*" 命中任意非空目标。
func matchAny(list, target string) bool {
	if target == "" {
		return false
	}
	for part := range strings.SplitSeq(list, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if part == "*" || part == target {
			return true
		}
	}
	return false
}
