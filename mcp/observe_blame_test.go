package mcp

import "testing"

// 本地参数校验拒绝的是被点名那个参数，不能连坐同一次调用里的其它参数。
func TestBlamesParam(t *testing.T) {
	const err = `模型 happyhorse1.1 未声明参数 "content"，可用：duration, generate_audio, n, ratio, resolution`
	cases := []struct {
		name   string
		err    string
		param  string
		expect bool
	}{
		{"点名的参数记失败", err, "content", true},
		{"没点名的参数不记", err, "reference_images", false},
		{"上游错误照样算", "upstream 429 too many requests", "reference_images", true},
		{"空错误照样算", "", "n", true},
		{
			"上游信封错误不算到参数头上",
			`upstream 400 (POST /contents/generations/tasks): {"error":{"message":"'content' 必须是非空数组"}}`,
			"reference_images",
			false,
		},
	}
	for _, c := range cases {
		if got := blamesParam(c.err, c.param); got != c.expect {
			t.Errorf("%s: blamesParam(%q, %q) = %v, 期望 %v", c.name, c.err, c.param, got, c.expect)
		}
	}
}

func TestSnapshotError(t *testing.T) {
	other := `{"admin_info":{"mcp":true,"param_snapshot":{"n":"9"},"upstream_error":"boom"}}`
	if got := snapshotError(other); got != "boom" {
		t.Fatalf("应当取出 upstream_error，实际 %q", got)
	}
	if got := snapshotError("{}"); got != "" {
		t.Fatalf("没有错误时应当是空串，实际 %q", got)
	}
	if got := snapshotError("not json"); got != "" {
		t.Fatalf("坏 JSON 应当当没有错误，实际 %q", got)
	}
}
