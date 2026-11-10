package mcp

import (
	"strings"
	"testing"
)

// 三态是 MCP 最容易搞错的地方：nil = 未声明（本地不拦截），0 = 明确不支持。
// 审计要能分辨这两者，否则「从不限制」和「禁止垫图」在留痕里长得一样。
func TestProfileAuditSnapshotKeepsTriState(t *testing.T) {
	zero := 0
	three := 3
	snap := profileAuditSnapshot(ModelProfile{
		ModelId:      "m1",
		MaxRefImages: &zero,
		MaxRefVideos: &three,
		// MaxPromptChars 故意留 nil
	})

	if v, ok := snap["max_ref_images"]; !ok || v != 0 {
		t.Fatalf("declared zero must stay 0, got %#v (present=%v)", v, ok)
	}
	if v, ok := snap["max_ref_videos"]; !ok || v != 3 {
		t.Fatalf("declared 3 must stay 3, got %#v", v)
	}
	if v, ok := snap["max_prompt_chars"]; !ok || v != nil {
		t.Fatalf("undeclared field must be nil, got %#v", v)
	}
}

// 只记变化项：一份声明十几个字段，把未改动的值也铺进审计会淹掉真正的改动。
func TestAuditDiffOnlyReturnsChanges(t *testing.T) {
	before := map[string]any{"model_id": "m1", "display_name": "旧名", "enabled": true, "max_ref_images": 3}
	after := map[string]any{"model_id": "m1", "display_name": "新名", "enabled": true, "max_ref_images": 0}

	changed := auditDiff(before, after)
	if _, ok := changed["enabled"]; ok {
		t.Fatal("unchanged field must not appear in diff")
	}
	if _, ok := changed["model_id"]; ok {
		t.Fatal("unchanged field must not appear in diff")
	}
	if len(changed) != 2 {
		t.Fatalf("expected exactly 2 changed fields, got %d: %#v", len(changed), changed)
	}

	name, _ := changed["display_name"].(map[string]any)
	if name["before"] != "旧名" || name["after"] != "新名" {
		t.Fatalf("display_name diff must carry before/after, got %#v", name)
	}
	refs, _ := changed["max_ref_images"].(map[string]any)
	if refs["before"] != 3 || refs["after"] != 0 {
		t.Fatalf("max_ref_images diff must carry before/after, got %#v", refs)
	}
}

// nil → 0 必须算一次变化：这是「从没声明」变成「明确不支持」，语义完全不同。
func TestAuditDiffTreatsNilToZeroAsChange(t *testing.T) {
	before := map[string]any{"max_ref_images": nil}
	after := map[string]any{"max_ref_images": 0}

	changed := auditDiff(before, after)
	if _, ok := changed["max_ref_images"]; !ok {
		t.Fatal("nil -> 0 must be recorded as a change")
	}
}

// 同一个 key 两次取到相同值不算变化，哪怕它是 nil。
func TestAuditDiffIgnoresIdenticalNil(t *testing.T) {
	before := map[string]any{"max_prompt_chars": nil}
	after := map[string]any{"max_prompt_chars": nil}

	if changed := auditDiff(before, after); len(changed) != 0 {
		t.Fatalf("identical nil must not be a change, got %#v", changed)
	}
}

func TestClipHintTruncatesByRuneNotByte(t *testing.T) {
	// 每个汉字三字节：按字节截断会把最后一个字劈开，落库就是乱码。
	long := strings.Repeat("重试", auditHintLimit)
	got := clipHint(long)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("overlong hint must be marked as truncated, got suffix %q", got[len(got)-6:])
	}
	if runes := []rune(got); len(runes) != auditHintLimit+1 {
		t.Fatalf("expected %d runes plus ellipsis, got %d", auditHintLimit, len(runes))
	}
	if short := clipHint("短文本"); short != "短文本" {
		t.Fatalf("short hint must pass through unchanged, got %q", short)
	}
}

// 错误规则的快照同样要保留 hint 的截断行为与可枚举字段。
func TestErrorRuleAuditSnapshot(t *testing.T) {
	snap := errorRuleAuditSnapshot(ErrorRule{
		Match:        "429",
		Retryable:    true,
		RetryAction:  "wait",
		RetryAfterMs: 2000,
		Enabled:      true,
		Sort:         3,
	})

	for key, want := range map[string]any{
		"match":          "429",
		"retryable":      true,
		"retry_action":   "wait",
		"retry_after_ms": 2000,
		"enabled":        true,
		"sort":           3,
	} {
		if snap[key] != want {
			t.Fatalf("%s: want %#v, got %#v", key, want, snap[key])
		}
	}
}
