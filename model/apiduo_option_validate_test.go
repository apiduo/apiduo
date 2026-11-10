package model

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
)

// 线上实测：批量接口对任何键名都回 200 并落库，拼错的键名同样「保存成功」。
func TestOptionKeyWritableRejectsUnknown(t *testing.T) {
	old := common.OptionMap
	common.OptionMap = map[string]string{"SystemName": "apiduo", "LoginTheme": "aurora"}
	t.Cleanup(func() { common.OptionMap = old })

	if !OptionKeyWritable("SystemName") {
		t.Error("上游已存在的键应当可写")
	}
	if !OptionKeyWritable("artifact_storage_setting.targets") {
		t.Error("扩展模块的键应当可写，哪怕库里还没有这一行")
	}
	for _, bad := range []string{
		"not_a_real_option_key",
		"artifact_storage_setting.targts", // 前缀对、后缀拼错
		"apiduo_appearance_setting.Speed", // 大小写错
		"",
	} {
		if OptionKeyWritable(bad) {
			t.Errorf("未知键 %q 不该被接受", bad)
		}
	}
}

func TestValidateApiduoOptionValue(t *testing.T) {
	ok := []struct{ key, val string }{
		{"LoginTheme", "aurora"},
		{"apiduo_appearance_setting.style", "sheen"},
		{"apiduo_appearance_setting.color", "#16a34a"},
		{"apiduo_appearance_setting.speed_ms", "3800"},
		{"apiduo_appearance_setting.ring_px", "1.5"},
		{"apiduo_appearance_setting.tail_deg", "120"},
		{"apiduo_appearance_setting.holiday", ""},
		{"apiduo_appearance_setting.holiday", "2026-02-17"},
		{"observability_setting.metadata_sample_rate", "0"},
		{"observability_setting.content_sample_rate", "100"},
		{"observability_setting.timezone", "Asia/Shanghai"},
		{"observability_setting.trace_viewer_user_ids", "[1,2]"},
		{"artifact_storage_setting.endpoint", "https://s3.example.com"},
		{"artifact_storage_setting.endpoint", ""},
		{"artifact_storage_setting.max_object_bytes", "0"},
		{"artifact_storage_setting.presign_ttl_seconds", "900"},
		{"artifact_storage_setting.rewrite_paths", "null"},
		{"artifact_storage_setting.targets", "[]"},
		{"artifact_storage_setting.targets", `[{"endpoint":"https://s3","bucket":"b"}]`},
		{"observability_setting.rules", "[]"},
		{"observability_setting.rules", `[{"days":[1,7],"start":"09:00","end":"18:00","metadata_rate":10,"content_rate":50}]`},
	}
	for _, c := range ok {
		if err := ValidateApiduoOptionValue(c.key, c.val); err != nil {
			t.Errorf("%s=%q 应当通过，实际 %v", c.key, c.val, err)
		}
	}

	bad := []struct{ key, val, want string }{
		{"LoginTheme", "neon-unknown", "登录页风格"},
		{"apiduo_appearance_setting.style", "glitter", "外观预设"},
		{"apiduo_appearance_setting.color", "green", "#RRGGBB"},
		{"apiduo_appearance_setting.speed_ms", "999999", "超出范围"},
		{"apiduo_appearance_setting.ring_px", "0.1", "超出范围"},
		{"apiduo_appearance_setting.tail_deg", "400", "超出范围"},
		{"apiduo_appearance_setting.holiday", "2026/02/17", "YYYY-MM-DD"},
		{"observability_setting.metadata_sample_rate", "999", "超出范围"},
		{"observability_setting.timezone", "Asia Shanghai", "时区名不合法"},
		{"artifact_storage_setting.endpoint", "ftp://x", "http(s)"},
		{"artifact_storage_setting.targets", "[{bad json", "JSON"},
		{"artifact_storage_setting.targets", `[{"endpoint":"https://s3","bucket":""}]`, "缺端点或存储桶"},
		{"observability_setting.rules", `[{"days":[9]}]`, "星期只能是 1-7"},
		{"observability_setting.rules", `[{"metadata_rate":999}]`, "元数据采样率"},
		{"observability_setting.rules", `[{"start":"18:00","end":"09:00"}]`, "结束时间要晚于开始时间"},
	}
	for _, c := range bad {
		err := ValidateApiduoOptionValue(c.key, c.val)
		if err == nil {
			t.Errorf("%s=%q 应当被拒", c.key, c.val)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s=%q 的报错应包含 %q，实际 %v", c.key, c.val, c.want, err)
		}
	}

	// 非扩展键不受影响
	if err := ValidateApiduoOptionValue("SystemName", "任何值"); err != nil {
		t.Errorf("不认识的键应当放行给上游，实际 %v", err)
	}
}

// 线上实测：targets 为空的存储配置也能被启用，直到真正转存才在链路里失败。
func TestValidateApiduoOptionSetStorageEnabled(t *testing.T) {
	old := common.OptionMap
	common.OptionMap = map[string]string{
		"artifact_storage_setting.enabled": "false",
		"artifact_storage_setting.targets": "null",
	}
	t.Cleanup(func() { common.OptionMap = old })

	if err := ValidateApiduoOptionSet(map[string]string{"artifact_storage_setting.enabled": "true"}); err == nil {
		t.Fatal("没有目标时启用应当被拒")
	}

	// 同一批里同时给 targets 和 enabled：合并视图下应当通过
	both := map[string]string{
		"artifact_storage_setting.enabled": "true",
		"artifact_storage_setting.targets": `[{"endpoint":"https://s3","bucket":"b"}]`,
	}
	if err := ValidateApiduoOptionSet(both); err != nil {
		t.Fatalf("同批设置目标并启用应当通过，实际 %v", err)
	}

	// 关掉存储不受 targets 限制
	if err := ValidateApiduoOptionSet(map[string]string{"artifact_storage_setting.enabled": "false"}); err != nil {
		t.Errorf("关闭存储不该被拦，实际 %v", err)
	}
}
