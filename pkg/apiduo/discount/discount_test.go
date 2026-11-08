package discount

import (
	"encoding/json"
	"testing"
)

const now = int64(1800000000)

func fixture() []Rule {
	return []Rule{
		{Groups: "vip", Models: "gpt-4o", Rate: 0.70},
		{Groups: "vip,svip", Models: "claude-3.7,gemini-2.5", Rate: 0.85},
		{Groups: "vip", Models: "*", Rate: 0.80},
		{Groups: "svip", Models: "*", Rate: 0.90},
		{Groups: "*", Models: "*", Rate: 0.95},
	}
}

func TestResolveOrderAndWildcard(t *testing.T) {
	cases := []struct {
		group, model string
		want         float64
		wantOK       bool
	}{
		{"vip", "gpt-4o", 0.70, true},      // 第 1 行，最具体
		{"vip", "claude-3.7", 0.85, true},  // 第 2 行，跨组模型例外
		{"vip", "gemini-2.5", 0.85, true},  // 第 2 行
		{"vip", "deepseek-r1", 0.80, true}, // 第 3 行，组保底
		{"svip", "gpt-4o", 0.90, true},     // 第 4 行，第 2 行模型不匹配
		{"svip", "claude-3.7", 0.85, true}, // 第 2 行先于第 4 行
		{"default", "gpt-4o", 0.95, true},  // 第 5 行兜底
		{"team-c", "deepseek-r1", 0.95, true},
		{"vip", "", 0, false},    // 空模型名不命中
		{"", "gpt-4o", 0, false}, // 空组名不命中
	}
	for _, c := range cases {
		got, ok := Resolve(fixture(), c.group, c.model, now)
		if ok != c.wantOK || (ok && got != c.want) {
			t.Fatalf("Resolve(%q,%q) = (%v,%v), want (%v,%v)", c.group, c.model, got, ok, c.want, c.wantOK)
		}
	}
}

func TestResolveFirstMatchWins(t *testing.T) {
	// 兜底行放到最前，后面全部失效 —— 与界面死行提示同一语义
	rules := []Rule{
		{Groups: "*", Models: "*", Rate: 0.95},
		{Groups: "vip", Models: "gpt-4o", Rate: 0.70},
	}
	got, ok := Resolve(rules, "vip", "gpt-4o", now)
	if !ok || got != 0.95 {
		t.Fatalf("第一条命中即停失败: got %v %v", got, ok)
	}
}

func TestResolveExpiry(t *testing.T) {
	rules := []Rule{
		{Groups: "vip", Models: "gpt-4o", Rate: 0.70, Until: now - 1}, // 已过期
		{Groups: "vip", Models: "gpt-4o", Rate: 0.75, Until: now},     // 等于当前时刻也应失效
		{Groups: "vip", Models: "gpt-4o", Rate: 0.78, Until: now + 1}, // 仍有效
		{Groups: "vip", Models: "*", Rate: 0.80, Until: 0},            // 永久
	}
	if got, ok := Resolve(rules, "vip", "gpt-4o", now); !ok || got != 0.78 {
		t.Fatalf("过期边界错误: got %v %v", got, ok)
	}
	if got, ok := Resolve(rules, "vip", "other", now); !ok || got != 0.80 {
		t.Fatalf("永久规则错误: got %v %v", got, ok)
	}
}

func TestResolveSkipsInvalidRate(t *testing.T) {
	for _, rate := range []float64{0, 1, 1.5, -0.1} {
		rules := []Rule{{Groups: "vip", Models: "*", Rate: rate}}
		if _, ok := Resolve(rules, "vip", "gpt-4o", now); ok {
			t.Fatalf("rate=%v 不应命中", rate)
		}
	}
}

func TestResolveTrimsAndSkipsEmptyParts(t *testing.T) {
	rules := []Rule{
		{Groups: " vip , svip ", Models: " claude-3.7 , gemini-2.5 ", Rate: 0.85},
		{Groups: "", Models: "*", Rate: 0.10},     // 空组不应变成全匹配
		{Groups: "vip", Models: " , ", Rate: 0.2}, // 空模型列表不应命中
	}
	if got, ok := Resolve(rules, "svip", "gemini-2.5", now); !ok || got != 0.85 {
		t.Fatalf("空格/多值解析失败: got %v %v", got, ok)
	}
	if _, ok := Resolve(rules, "vip", "gpt-4o", now); ok {
		t.Fatalf("空 groups 不应命中")
	}
}

func TestResolveIsCaseSensitive(t *testing.T) {
	rules := []Rule{{Groups: "vip", Models: "gpt-4o", Rate: 0.70}}
	if _, ok := Resolve(rules, "VIP", "GPT-4O", now); ok {
		t.Fatalf("模型名/组名应区分大小写")
	}
}

func TestParseTolerance(t *testing.T) {
	if got := Parse(nil); got != nil {
		t.Fatalf("nil 应返回 nil")
	}
	for _, raw := range []string{`not json`, `{"groups":"vip"}`, `[{"groups":"vip","rate":"8折"}]`, `[]`} {
		if got := Parse(json.RawMessage(raw)); len(got) != 0 {
			t.Fatalf("非法输入 %s 应返回空，得到 %d 条", raw, len(got))
		}
	}
	good := `[{"groups":"vip","models":"gpt-4o","rate":0.7,"until":0}]`
	if got := Parse(json.RawMessage(good)); len(got) != 1 || got[0].Rate != 0.7 {
		t.Fatalf("正常解析失败: %+v", got)
	}
}

func TestParseCapsRuleCount(t *testing.T) {
	raw := make([]Rule, 0, MaxRules+10)
	for range MaxRules + 10 {
		raw = append(raw, Rule{Groups: "vip", Models: "*", Rate: 0.9})
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := Parse(encoded); len(got) != MaxRules {
		t.Fatalf("应截断到 %d 条，得到 %d", MaxRules, len(got))
	}
}

func TestExpand(t *testing.T) {
	got := Expand(fixture(), []string{"vip", "svip", "default"}, now)

	want := map[string]map[string]float64{
		"vip":     {"gpt-4o": 0.70, "claude-3.7": 0.85, "gemini-2.5": 0.85, "*": 0.80},
		"svip":    {"claude-3.7": 0.85, "gemini-2.5": 0.85, "*": 0.90},
		"default": {"*": 0.95},
	}
	if len(got) != len(want) {
		t.Fatalf("组数不符: %+v", got)
	}
	for group, rules := range want {
		if len(got[group]) != len(rules) {
			t.Fatalf("%s 条目数不符: %+v", group, got[group])
		}
		for model, rate := range rules {
			if got[group][model] != rate {
				t.Fatalf("Expand[%s][%s] = %v want %v", group, model, got[group][model], rate)
			}
		}
	}
}

// TestExpandMatchesResolve 是展开表的正确性底线：
// 前端只查表，后端按规则算，两者对任意「组 × 模型」必须完全一致。
func TestExpandMatchesResolve(t *testing.T) {
	rules := fixture()
	groups := []string{"vip", "svip", "default", "team-x", ""}
	// 真实请求的模型名一定非空；空名由 Resolve 单独拒绝（见 TestResolveOrderAndWildcard）。
	models := []string{"gpt-4o", "claude-3.7", "gemini-2.5", "deepseek-r1", "unlisted-model"}
	table := Expand(rules, groups, now)

	for _, group := range groups {
		for _, model := range models {
			want, wantOK := Resolve(rules, group, model, now)
			got, gotOK := table[group][model]
			if !gotOK {
				got, gotOK = table[group]["*"]
			}
			if gotOK != wantOK || (gotOK && got != want) {
				t.Fatalf("不一致 group=%q model=%q: table=%v,%v resolve=%v,%v", group, model, got, gotOK, want, wantOK)
			}
		}
	}
}

func TestExpandSkipsUncoveredGroupAndExpired(t *testing.T) {
	// 没有 "*" 兜底时，没有规则覆盖的组不应出现在结果里
	rules := []Rule{{Groups: "vip", Models: "gpt-4o", Rate: 0.70, Until: now - 1}}
	if got := Expand(rules, []string{"vip", "team-x"}, now); len(got) != 0 {
		t.Fatalf("过期规则不应展开，得到 %+v", got)
	}
	rules[0].Until = 0
	got := Expand(rules, []string{"vip", "team-x"}, now)
	if len(got) != 1 || got["vip"]["gpt-4o"] != 0.70 {
		t.Fatalf("只应展开 vip，得到 %+v", got)
	}
}

func TestExpandReturnsIndependentMaps(t *testing.T) {
	rules := fixture()
	first := Expand(rules, []string{"vip"}, now)
	first["vip"]["gpt-4o"] = 0.01
	second := Expand(rules, []string{"vip"}, now)
	if second["vip"]["gpt-4o"] != 0.70 {
		t.Fatalf("两次展开共享了底层 map，调用方改写会污染后续结果")
	}
}

func BenchmarkExpandWorstCase(b *testing.B) {
	groups := []string{"default", "vip", "svip", "auto", "team-a", "team-b", "team-c", "internal"}
	ts := int64(1800000000)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = Expand(fixture(), groups, ts)
	}
}

func TestNormalizeTrimsAndDedupes(t *testing.T) {
	got, err := Normalize([]Rule{
		{Groups: " vip , , svip , vip ", Models: " gpt-4o , gpt-4o ", Rate: 0.8},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got[0].Groups != "vip,svip" {
		t.Fatalf("组未去重: %q", got[0].Groups)
	}
	if got[0].Models != "gpt-4o" {
		t.Fatalf("模型未去重: %q", got[0].Models)
	}
}

func TestNormalizeRoundsRateToTwoDecimals(t *testing.T) {
	// 前端「7.5 折」除以 10 会带浮点尾巴，不收敛就会被判成 >= 1 或存成脏值。
	got, err := Normalize([]Rule{{Groups: "vip", Models: "*", Rate: 0.750000001}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got[0].Rate != 0.75 {
		t.Fatalf("rate 未收敛: %v", got[0].Rate)
	}
}

func TestNormalizeRejectsInvalidRules(t *testing.T) {
	cases := []struct {
		name  string
		rules []Rule
	}{
		{"空分组", []Rule{{Groups: "  ", Models: "*", Rate: 0.8}}},
		{"空模型", []Rule{{Groups: "vip", Models: "", Rate: 0.8}}},
		{"倍率为 0", []Rule{{Groups: "vip", Models: "*", Rate: 0}}},
		{"倍率为 1", []Rule{{Groups: "vip", Models: "*", Rate: 1}}},
		{"倍率为负", []Rule{{Groups: "vip", Models: "*", Rate: -0.5}}},
		{"到期为负", []Rule{{Groups: "vip", Models: "*", Rate: 0.8, Until: -1}}},
	}
	for _, tc := range cases {
		if _, err := Normalize(tc.rules); err == nil {
			t.Fatalf("%s 应被拒绝", tc.name)
		}
	}
}

func TestNormalizeRejectsTooManyRules(t *testing.T) {
	rules := make([]Rule, MaxRules+1)
	for i := range rules {
		rules[i] = Rule{Groups: "vip", Models: "*", Rate: 0.8}
	}
	if _, err := Normalize(rules); err == nil {
		t.Fatalf("超过 %d 条应被拒绝", MaxRules)
	}
}

func TestNormalizeAllowsEmptyList(t *testing.T) {
	got, err := Normalize(nil)
	if err != nil {
		t.Fatalf("空列表表示清除折扣，不应报错: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("期望空切片，得到 %+v", got)
	}
}
