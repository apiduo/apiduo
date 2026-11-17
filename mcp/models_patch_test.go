package mcp

import (
	"encoding/json"
	"reflect"
	"testing"
)

// profilePatchKeys 是「保存声明」的白名单，applyProfilePatch 是它唯一的实现。
//
// 两者不一致的症状是最难查的那一种：接口返回成功、审计记了一笔、字段却没变。
// cost 与 priority 就这样静默丢了很久 —— 它们在白名单里，switch 却没有 case。
//
// 这里逐个 key 单独 patch，断言至少有一个字段真的动了。漏 case 当场失败；
// 以后往白名单加字段却忘了在这里补样本，也会失败。
func TestEveryPatchKeyHasEffect(t *testing.T) {
	samples := map[string]string{
		"display_name":     `"Hailuo"`,
		"kind":             `"video"`,
		"async":            `true`,
		"cost":             `"$1/次"`,
		"priority":         `7`,
		"max_ref_images":   `1`,
		"max_ref_videos":   `2`,
		"max_prompt_chars": `3`,
		"parameters":       `"{}"`,
		"summary":          `"s"`,
		"tags":             `"t"`,
		"source":           `"manual"`,
	}

	base := ModelProfile{}
	for _, key := range profilePatchKeys {
		sample, ok := samples[key]
		if !ok {
			t.Fatalf("profilePatchKeys 新增了 %q，请在本测试补一个非零样本", key)
		}
		got, err := applyProfilePatch(base, map[string]json.RawMessage{
			key: json.RawMessage(sample),
		})
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if reflect.DeepEqual(got, base) {
			t.Errorf("%q 在 profilePatchKeys 里，但 applyProfilePatch 没有对应 case —— 传了会被静默丢弃", key)
		}
	}
}

// 未出现在 patch 里的字段必须原样保留。这是 patch 语义的全部价值：
// 面板只改一个显示名时，不该把 priority 或 cost 抹成零值。
func TestPatchLeavesAbsentFieldsAlone(t *testing.T) {
	base := ModelProfile{
		DisplayName: "keep",
		Cost:        "$0.01/次",
		Priority:    50,
		Async:       true,
		Kind:        "image",
	}
	got, err := applyProfilePatch(base, map[string]json.RawMessage{
		"display_name": json.RawMessage(`"changed"`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.DisplayName != "changed" {
		t.Errorf("display_name 未生效: %q", got.DisplayName)
	}
	if got.Cost != base.Cost || got.Priority != base.Priority || got.Async != base.Async || got.Kind != base.Kind {
		t.Errorf("未传的字段被改了: %+v", got)
	}
}
