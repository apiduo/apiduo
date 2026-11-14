package mcp

import "testing"

// 失败归因在写入侧算一次：本地校验点名谁就是谁；上游错误不点名时，
// 只有单参数快照才能定责。多参数请求里硬归因会让巡检建议改错声明。
func TestBlameFrom(t *testing.T) {
	cases := []struct {
		name   string
		err    string
		params map[string]string
		want   string
	}{
		{
			"未声明参数点名 content，不连坐其它参数",
			`模型 x 未声明参数 "content"，可用：n, aspect_ratio`,
			map[string]string{"n": "9", "aspect_ratio": "32:9"},
			"",
		},
		{
			"点名了快照里的参数就归给它",
			`模型 x 未声明参数 "n"，可用：size`,
			map[string]string{"n": "9"},
			"n",
		},
		{
			"单参数样本的上游拒绝可以定责",
			"upstream 400 invalid value",
			map[string]string{"duration": "12"},
			"duration",
		},
		{
			"多参数样本的上游拒绝无法定责",
			"upstream 403 insufficient_user_quota (POST /images/generations)",
			map[string]string{"n": "1", "size": "1:1"},
			"",
		},
		{
			"上游点名该参数就算",
			"n must be <= 4",
			map[string]string{"n": "9"},
			"n",
		},
		{
			"content 信封错误不算到可调参数上",
			`'content' 必须是非空数组`,
			map[string]string{"n": "9"},
			"",
		},
		{"空错误没有归因依据", "", map[string]string{"n": "9"}, ""},
		{"没有快照就没有归因", "boom", nil, ""},
	}
	for _, c := range cases {
		if got := blameFrom(c.err, c.params); got != c.want {
			t.Errorf("%s: blameFrom(%q, %v) = %q，期望 %q", c.name, c.err, c.params, got, c.want)
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
