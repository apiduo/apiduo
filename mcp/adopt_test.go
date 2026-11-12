package mcp

import (
	"encoding/json"
	"testing"
)

// 数值参数：最小被拒值减一就是建议上限。
func TestAuditSuggestionNumericMax(t *testing.T) {
	p := ModelProfile{ModelId: "img", Kind: "image"}
	issues := Audit(p, []ParamStat{{Param: "n", Value: "9", OK: 0, Fail: 2}})
	if len(issues) != 1 {
		t.Fatalf("应当只有一条未声明边界，实际 %+v", issues)
	}
	s := issues[0].Suggestion
	if s == nil || s.Max == nil {
		t.Fatalf("数值参数应当给出 max 建议，实际 %+v", s)
	}
	if *s.Max != 8 {
		t.Fatalf("9 被拒，建议上限应当是 8，实际 %v", *s.Max)
	}
}

// 字符串参数：把成功过的取值收成枚举，而不是拍一个数值上限。
func TestAuditSuggestionEnumFromOKValues(t *testing.T) {
	p := ModelProfile{ModelId: "img", Kind: "image"}
	issues := Audit(p, []ParamStat{
		{Param: "aspect_ratio", Value: "1:1", OK: 3, Fail: 0},
		{Param: "aspect_ratio", Value: "32:9", OK: 0, Fail: 1},
	})
	if len(issues) != 1 || issues[0].Suggestion == nil {
		t.Fatalf("应当给出一条枚举建议，实际 %+v", issues)
	}
	s := issues[0].Suggestion
	if s.Max != nil {
		t.Fatalf("字符串参数不该给数值上限，实际 %v", *s.Max)
	}
	if len(s.Enum) != 1 || s.Enum[0] != "1:1" {
		t.Fatalf("只该保留成功过的 1:1，实际 %v", s.Enum)
	}
}

// 全都失败、一次也没成功过：信息不足以定约束，不能硬写。
func TestAuditSuggestionNilWhenNoOKValue(t *testing.T) {
	p := ModelProfile{ModelId: "img", Kind: "image"}
	issues := Audit(p, []ParamStat{
		{Param: "size", Value: "1024x1024", OK: 0, Fail: 2},
		{Param: "size", Value: "999x999", OK: 0, Fail: 1},
	})
	if len(issues) != 1 {
		t.Fatalf("应当报一条未声明边界，实际 %+v", issues)
	}
	if issues[0].Suggestion != nil {
		t.Fatalf("没有成功样本时不该给建议，实际 %+v", issues[0].Suggestion)
	}
}

// 合并建议时不能把同一个模型上别的参数弄丢。
func TestApplySuggestionMergesParameters(t *testing.T) {
	p := ModelProfile{
		ModelId:    "img",
		Kind:       "image",
		Parameters: `{"aspect_ratio":{"enum":["1:1","16:9"]},"n":{"min":1,"default":1}}`,
	}
	max := 8.0
	applied, err := applySuggestion(&p, AuditSuggestion{Param: "n", Max: &max})
	if err != nil {
		t.Fatalf("合并不该失败: %v", err)
	}
	if applied["max"] != max {
		t.Fatalf("返回值应当说明写入了 max=8，实际 %+v", applied)
	}

	var got map[string]map[string]any
	if err := json.Unmarshal([]byte(p.Parameters), &got); err != nil {
		t.Fatalf("结果不是合法 JSON: %v", err)
	}
	if got["n"]["max"] != max {
		t.Errorf("n 应当带上 max=8，实际 %+v", got["n"])
	}
	if got["n"]["min"] != 1.0 {
		t.Errorf("n 原有的 min 不能被清掉，实际 %+v", got["n"])
	}
	if len(got["aspect_ratio"]["enum"].([]any)) != 2 {
		t.Errorf("别的参数不能被丢掉，实际 %+v", got["aspect_ratio"])
	}
}

// reference_images 不在 parameters 里，它对应强类型字段 MaxRefImages。
func TestApplySuggestionReferenceImages(t *testing.T) {
	p := ModelProfile{ModelId: "img", Kind: "image"}
	max := 2.0
	if _, err := applySuggestion(&p, AuditSuggestion{Param: "reference_images", Max: &max}); err != nil {
		t.Fatalf("合并不该失败: %v", err)
	}
	if p.MaxRefImages == nil || *p.MaxRefImages != 2 {
		t.Fatalf("应当写到 MaxRefImages，实际 %+v", p.MaxRefImages)
	}
	if p.Parameters != "" {
		t.Fatalf("不该往 parameters 里塞 reference_images，实际 %q", p.Parameters)
	}
}
