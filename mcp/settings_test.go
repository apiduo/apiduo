package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// 没有配置时用默认值，且 GET 回的是「合并后的有效值」。
func TestSettingsDefaults(t *testing.T) {
	setupDB(t)
	s := LoadSettings()
	if s.MaxBatchN != defaultSettings.MaxBatchN || s.MaxBodyBytes != defaultSettings.MaxBodyBytes {
		t.Fatalf("默认值不对: %+v", s)
	}
}

// 局部更新：只改请求里出现的键，其余保持。
func TestSettingsPatchMerges(t *testing.T) {
	setupDB(t)
	before := LoadSettings()

	next, err := SaveSettings(map[string]json.RawMessage{
		"max_batch_n": json.RawMessage(`2`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.MaxBatchN != 2 {
		t.Fatalf("max_batch_n = %d", next.MaxBatchN)
	}
	if next.TaskGetPerMin != before.TaskGetPerMin {
		t.Fatalf("没改的字段被动了: %d -> %d", before.TaskGetPerMin, next.TaskGetPerMin)
	}
	// 立刻生效：写侧失效缓存。
	if got := LoadSettings().MaxBatchN; got != 2 {
		t.Fatalf("设置没有热更新: %d", got)
	}
}

// 未知键要报错：静默忽略会让人以为改生效了。
func TestSettingsRejectUnknownKey(t *testing.T) {
	setupDB(t)
	_, err := SaveSettings(map[string]json.RawMessage{"nope": json.RawMessage(`1`)})
	if err == nil || !strings.Contains(err.Error(), "未知的设置项") {
		t.Fatalf("应拒绝未知键，实际 %v", err)
	}
}

// 越界值夹到边界，而不是整份打回默认。
func TestSettingsClampsOutOfRange(t *testing.T) {
	setupDB(t)
	next, err := SaveSettings(map[string]json.RawMessage{
		"max_batch_n":    json.RawMessage(`999`),
		"max_body_bytes": json.RawMessage(`1`),
		"poll_after_ms":  json.RawMessage(`999999`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.MaxBatchN != 64 {
		t.Fatalf("max_batch_n 应夹到 64，实际 %d", next.MaxBatchN)
	}
	if next.MaxBodyBytes != 64<<10 {
		t.Fatalf("max_body_bytes 应夹到 64KB，实际 %d", next.MaxBodyBytes)
	}
	if next.PollAfterMS != 600_000 {
		t.Fatalf("poll_after_ms 应夹到 600000，实际 %d", next.PollAfterMS)
	}
}

// 改设置要立刻影响工具行为，不用重启。
func TestSettingsHotApplyBatchN(t *testing.T) {
	setupDB(t)
	mockUpstream(t)
	resetProtocol()
	if _, err := SaveSettings(map[string]json.RawMessage{"max_batch_n": json.RawMessage(`2`)}); err != nil {
		t.Fatal(err)
	}
	// 没有声明 n 的模型，兜底值现在应该是 2。
	if err := SaveProfile(&ModelProfile{ModelId: "loose", Kind: "image", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	srv := mcpServer(t)
	res := rpc(t, srv.URL, "sk-test", "tools/call", map[string]any{
		"name":      "image.generate",
		"arguments": map[string]any{"model": "loose", "prompt": "x", "n": 3},
	})
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Fatalf("n=3 应被新的兜底上限拒绝: %v", res)
	}
	sc, _ := res["structuredContent"].(map[string]any)
	terr, _ := sc["error"].(map[string]any)
	if msg := fmt.Sprint(terr["message"]); !strings.Contains(msg, "2") {
		t.Fatalf("错误里应带新的上限 2: %q", msg)
	}
}

// 参考图三种形态都收：http(s) URL、data URL、裸 base64。
//
// 线上实测：qwen2api 的 image 字段认裸 base64（105 秒出图），但对同一张图的
// data URI 回 502。所以 MCP 必须同时接受两者，并在发送前把 data: 前缀剥掉。
func TestParseRefsAcceptsURLDataURLAndRawBase64(t *testing.T) {
	raw64 := strings.Repeat("QUJDRA", 12) // 72 字符，合法 base64
	ok := []string{
		"https://example.test/a.png",
		"data:image/png;base64," + raw64,
		"data:video/mp4;base64," + raw64,
		raw64,
	}
	for _, raw := range ok {
		if _, err := parseRefs(raw); err != nil {
			t.Fatalf("%q 应被接受: %v", clipRunes(raw, 40), err)
		}
	}
	bad := []string{
		"a.png",                      // 相对路径
		"/tmp/a.png",                 // 绝对路径：点号暴露它不是 base64
		"file:///tmp/a.png",          // 不支持的协议
		"data:text/plain,hi",         // 不是图片/视频
		"data:image/png,abcd",        // 缺 base64 声明
		"data:image/png;base64,====", // 载荷非法
		strings.Repeat("A", 20),      // 太短，不像真实图片
	}
	for _, raw := range bad {
		if _, err := parseRefs(raw); err == nil {
			t.Fatalf("%q 应被拒绝", clipRunes(raw, 40))
		}
	}
}

// 发出去的形态：单张是字符串、多张是数组；data URI 要剥成裸 base64。
func TestBuildPayloadImageRefShape(t *testing.T) {
	raw64 := strings.Repeat("QUJDRA", 12)
	m := modelView{ID: "ref-image", Kind: "image"}

	p, err := buildPayload(m, generateInput{Prompt: "x"}, []string{"data:image/png;base64," + raw64})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := p["image"].(string); got != raw64 {
		t.Fatalf("单张应是剥掉前缀的字符串，实际 %T %v", p["image"], clipRunes(fmt.Sprint(p["image"]), 40))
	}

	p, err = buildPayload(m, generateInput{Prompt: "x"}, []string{"https://a.test/1.png", raw64})
	if err != nil {
		t.Fatal(err)
	}
	arr, ok := p["image"].([]string)
	if !ok || len(arr) != 2 {
		t.Fatalf("多张应是数组，实际 %T", p["image"])
	}
	if arr[0] != "https://a.test/1.png" || arr[1] != raw64 {
		t.Fatalf("数组内容不对: %v", arr)
	}

	// content.image_url 需要一个真 URL，裸 base64 在视频上不成立。
	if _, err := buildPayload(modelView{ID: "v", Kind: "video"}, generateInput{Prompt: "x"}, []string{raw64}); err == nil {
		t.Fatal("视频不该接受裸 base64")
	}
}

// 请求体上限必须真的跟着设置走：SDK 自带的 4 MiB 默认值会把它截断，
// 所以这里从 64KB 调到 1MB 再验证一次，确保两层没有打架。
func TestBodyLimitFollowsSettings(t *testing.T) {
	setupDB(t)
	srv := mcpServer(t)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + strings.Repeat(" ", 100<<10)

	post := func() int {
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/mcp", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if _, err := SaveSettings(map[string]json.RawMessage{"max_body_bytes": json.RawMessage(`65536`)}); err != nil {
		t.Fatal(err)
	}
	if code := post(); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("64KB 上限下 100KB 应 413，实际 %d", code)
	}

	if _, err := SaveSettings(map[string]json.RawMessage{"max_body_bytes": json.RawMessage(`1048576`)}); err != nil {
		t.Fatal(err)
	}
	if code := post(); code != http.StatusOK {
		t.Fatalf("1MB 上限下 100KB 应 200，实际 %d", code)
	}
}
