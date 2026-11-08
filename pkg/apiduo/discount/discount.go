// Package discount 解析并匹配用户级模型折扣。
//
// 规则存放在 users.setting 的 apiduo_discounts 字段里，是一个有序数组：
// 数组顺序即优先级，从上往下第一条命中即停，不同规则之间不相乘。
package discount

import (
	"encoding/json"
	"fmt"
	"math"
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

// Normalize 校验并规范化待写入的规则。
//
// 前端传来的数据不能直接落库：条数、倍率区间、空列都要在服务端再判一次，
// 并把 rate 收敛到两位小数，避免 7.5 折变成 0.7500000001 之后被判成 >= 1。
func Normalize(rules []Rule) ([]Rule, error) {
	if len(rules) > MaxRules {
		return nil, fmt.Errorf("规则最多 %d 条", MaxRules)
	}

	out := make([]Rule, 0, len(rules))
	for index, rule := range rules {
		groups := normalizeList(rule.Groups)
		models := normalizeList(rule.Models)
		if groups == "" || models == "" {
			return nil, fmt.Errorf("第 %d 条规则的分组和模型不能为空", index+1)
		}
		if rule.Until < 0 {
			return nil, fmt.Errorf("第 %d 条规则的到期时间不能为负", index+1)
		}

		rate := math.Round(rule.Rate*100) / 100
		if rate <= 0 || rate >= 1 {
			return nil, fmt.Errorf("第 %d 条规则的折扣要在 0 和 1 之间（不含）", index+1)
		}

		out = append(out, Rule{
			Groups: groups,
			Models: models,
			Rate:   rate,
			Until:  rule.Until,
		})
	}
	return out, nil
}

// normalizeList 去掉逗号分隔列表里的空白与重复项。
func normalizeList(list string) string {
	seen := make(map[string]struct{}, 4)
	parts := make([]string, 0, 4)
	for part := range strings.SplitSeq(list, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		parts = append(parts, part)
	}
	return strings.Join(parts, ",")
}
