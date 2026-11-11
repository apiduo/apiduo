package model

import (
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/apiduo/trace"
	"github.com/QuantumNous/new-api/setting/observability_setting"
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
		{"observability_setting.body_mode", "off"},
		{"observability_setting.body_mode", "headtail"},
		{"observability_setting.body_mode", "FULL"}, // 大小写不该决定成败
		{"observability_setting.body_head_bytes", "0"},
		{"observability_setting.body_head_bytes", strconv.Itoa(trace.MaxBodyWindowBytes)},
		{"observability_setting.body_full_bytes", strconv.Itoa(trace.MinBodyFullBytes)},
		{"observability_setting.body_full_bytes", "4194304"},
		{"observability_setting.body_full_bytes", strconv.Itoa(trace.MaxBodyFullBytes)},
		{"observability_setting.export_host", "http://apiduo-langfuse-web:3000"},
		{"observability_setting.export_host", ""}, // 空 = 跟随环境变量
		{"observability_setting.console_url", "https://langfuse.1task.cn"},
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
		{"observability_setting.body_mode", "hedtali", "off / headtail / full"},
		{"observability_setting.body_head_bytes", strconv.Itoa(trace.MaxBodyWindowBytes + 1), "超出范围"},
		// 消息得说 MB：表单上写的是 1–16，运维要照着它改。
		{"observability_setting.body_full_bytes", "0", "1 MB ~ 16 MB"},
		{"observability_setting.body_full_bytes", strconv.Itoa(trace.MinBodyFullBytes - 1), "1 MB ~ 16 MB"},
		{"observability_setting.body_full_bytes", strconv.Itoa(trace.MaxBodyFullBytes + 1), "1 MB ~ 16 MB"},
		{"observability_setting.body_full_bytes", "abc", "1 MB ~ 16 MB"},
		{"observability_setting.export_host", "langfuse.1task.cn", "http(s)"},
		{"observability_setting.export_host", "ftp://langfuse", "http(s)"},
		// 导出路径是 {host}/api/public/otel/v1/traces 直接拼的：带路径不报错，
		// 只会静默 404，所以写入期就必须拒。
		{"observability_setting.export_host", "https://langfuse.1task.cn/langfuse", "源地址"},
		{"observability_setting.console_url", "https://user:pw@langfuse.1task.cn", "源地址"},
		{"observability_setting.console_url", "https://langfuse.1task.cn?x=1", "源地址"},
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

	// 地址类字段是纯手输的，报错必须指回界面上的那一格，不能甩内部键名。
	for _, c := range []struct{ key, want string }{
		{"observability_setting.export_host", "上报地址"},
		{"observability_setting.console_url", "控制台地址"},
	} {
		err := ValidateApiduoOptionValue(c.key, "https://langfuse.1task.cn/langfuse")
		if err == nil {
			t.Fatalf("%s 应当被拒", c.key)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s 的报错应点名 %q，实际 %v", c.key, c.want, err)
		}
		if strings.Contains(err.Error(), "observability_setting.") {
			t.Errorf("%s 的报错不该出现内部键名，实际 %v", c.key, err)
		}
	}

	// 非扩展键不受影响
	if err := ValidateApiduoOptionValue("SystemName", "任何值"); err != nil {
		t.Errorf("不认识的键应当放行给上游，实际 %v", err)
	}
}

// 单条 128 KB 合法、两条合起来 256 KB 也「各自合法」，只有合并视图能拦住。
// 拦不住的后果不是多收数据，是每请求 256 KB 的采集缓冲 —— 千并发 256 MB。
func TestValidateApiduoOptionSetBodyWindow(t *testing.T) {
	limit := strconv.Itoa(trace.MaxBodyWindowBytes)

	if err := ValidateApiduoOptionSet(map[string]string{
		"observability_setting.body_head_bytes": limit,
		"observability_setting.body_tail_bytes": limit,
	}); err == nil {
		t.Fatal("批内两条合计超预算，应当被拒")
	}

	if err := ValidateApiduoOptionSet(map[string]string{
		"observability_setting.body_head_bytes": "65536",
		"observability_setting.body_tail_bytes": "16384",
	}); err != nil {
		t.Fatalf("合法组合不该被拒：%v", err)
	}

	// 只改一条时，另一条要按现存值合并 —— 拿不到现存值就会漏判。
	head := observability_setting.Get().BodyHeadBytes
	overflow := strconv.Itoa(trace.MaxBodyWindowBytes - head + 1024)
	if err := ValidateApiduoOptionSet(map[string]string{
		"observability_setting.body_tail_bytes": overflow,
	}); err == nil {
		t.Fatal("与现存头窗口合并后超预算，应当被拒")
	}

	// 不碰这两个键的保存不该被这段逻辑影响。
	if err := ValidateApiduoOptionSet(map[string]string{
		"observability_setting.metadata_sample_rate": "10",
	}); err != nil {
		t.Fatalf("无关的键不该触发窗口校验：%v", err)
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

// 设置页把 apiduoRange 的消息原样弹给运维。原先用 %g 格式化上界：
// 1048576 会打成 1.048576e+06，谁也没法照着它把表单改对。
func TestRangeMessagesStayReadable(t *testing.T) {
	cases := []struct{ key, val string }{
		{"observability_setting.body_full_bytes", strconv.Itoa(trace.MaxBodyFullBytes + 1)},
		{"observability_setting.body_head_bytes", strconv.Itoa(trace.MaxBodyWindowBytes + 1)},
		{"artifact_storage_setting.max_object_bytes", strconv.Itoa(1<<40 + 1)},
		{"apiduo_appearance_setting.ring_px", "0.1"},
	}
	for _, c := range cases {
		err := ValidateApiduoOptionValue(c.key, c.val)
		if err == nil {
			t.Fatalf("%s=%s 应当被拒", c.key, c.val)
		}
		msg := err.Error()
		if strings.Contains(msg, "e+") || strings.Contains(msg, "E+") {
			t.Errorf("%s 的消息里出现了科学计数法：%s", c.key, msg)
		}
	}
}

func TestApiduoNumKeepsFractions(t *testing.T) {
	// 整数按整数打，小数不能被截成 0 —— ring_px 的下界就是 0.5。
	for _, c := range []struct {
		in   float64
		want string
	}{
		{1048576, "1048576"},
		{1 << 40, "1099511627776"},
		{0.5, "0.5"},
		{0.1, "0.1"},
	} {
		if got := apiduoNum(c.in); got != c.want {
			t.Errorf("apiduoNum(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
