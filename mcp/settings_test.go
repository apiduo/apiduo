package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
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

// 本地文件的正路：data URL 直接内联，不需要额外上传接口。
func TestParseRefsAcceptsDataURLAndRejectsOtherSchemes(t *testing.T) {
	ok := []string{
		"https://example.test/a.png",
		"data:image/png;base64,AAAA",
		"data:video/mp4;base64,AAAA",
	}
	for _, raw := range ok {
		if _, err := parseRefs(raw); err != nil {
			t.Fatalf("%q 应被接受: %v", raw, err)
		}
	}
	bad := []string{"a.png", "/tmp/a.png", "file:///tmp/a.png", "data:text/plain,hi"}
	for _, raw := range bad {
		if _, err := parseRefs(raw); err == nil {
			t.Fatalf("%q 应被拒绝", raw)
		}
	}
}

// 状态没变时只回最小信息，不重复下发整份结果。
func TestTaskGetUnchangedWhileWorking(t *testing.T) {
	setupDB(t)
	var queries atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/contents/generations/tasks/t-run", func(w http.ResponseWriter, _ *http.Request) {
		queries.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"task_id":"t-run","status":"running","progress":"30%"}`)
	})
	up := httptest.NewServer(mux)
	t.Cleanup(up.Close)
	os.Setenv("MCP_UPSTREAM_BASE_URL", up.URL+"/v1")
	t.Cleanup(func() { os.Unsetenv("MCP_UPSTREAM_BASE_URL") })
	resetProtocol()
	if err := SaveProfile(&ModelProfile{ModelId: "run-video", Kind: "video", Enabled: true, Async: true}); err != nil {
		t.Fatal(err)
	}
	srv := mcpServer(t)
	signed, err := signTaskID(taskPayload{Model: "run-video", Upstream: "t-run", Kind: "video"})
	if err != nil {
		t.Fatal(err)
	}

	first := callTool(t, srv.URL, "sk-test", "task.get", map[string]any{"task_id": signed})
	if first["status"] != "working" || first["unchanged"] != nil {
		t.Fatalf("第一次应完整下发: %v", first)
	}
	second := callTool(t, srv.URL, "sk-test", "task.get", map[string]any{"task_id": signed})
	if second["status"] != "working" {
		t.Fatalf("状态错误: %v", second["status"])
	}
	if v, _ := second["unchanged"].(bool); !v {
		t.Fatalf("状态没变应标 unchanged: %v", second)
	}
	if _, exists := second["outputs"]; exists {
		t.Fatalf("unchanged 不该带产物: %v", second)
	}
	if got := queries.Load(); got != 2 {
		t.Fatalf("working 每次都要查询真实状态，实际 %d 次", got)
	}
}

// 终态在 TTL 内直接回缓存，连 relay 都不查。
func TestTaskGetTerminalCached(t *testing.T) {
	setupDB(t)
	var queries atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/contents/generations/tasks/t-done", func(w http.ResponseWriter, _ *http.Request) {
		queries.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"task_id":"t-done","status":"succeeded","url":"https://example.test/v.mp4"}`)
	})
	up := httptest.NewServer(mux)
	t.Cleanup(up.Close)
	os.Setenv("MCP_UPSTREAM_BASE_URL", up.URL+"/v1")
	t.Cleanup(func() { os.Unsetenv("MCP_UPSTREAM_BASE_URL") })
	resetProtocol()
	if err := SaveProfile(&ModelProfile{ModelId: "done-video", Kind: "video", Enabled: true, Async: true}); err != nil {
		t.Fatal(err)
	}
	srv := mcpServer(t)
	signed, err := signTaskID(taskPayload{Model: "done-video", Upstream: "t-done", Kind: "video"})
	if err != nil {
		t.Fatal(err)
	}

	first := callTool(t, srv.URL, "sk-test", "task.get", map[string]any{"task_id": signed})
	if first["status"] != "completed" {
		t.Fatalf("第一次应 completed: %v", first)
	}
	second := callTool(t, srv.URL, "sk-test", "task.get", map[string]any{"task_id": signed})
	if v, _ := second["unchanged"].(bool); !v {
		t.Fatalf("终态第二次应标 unchanged: %v", second)
	}
	outs, _ := second["outputs"].([]any)
	if len(outs) != 1 {
		t.Fatalf("终态即使 unchanged 也要带产物（别的会话可能没拿到）: %v", second["outputs"])
	}
	if got := queries.Load(); got != 1 {
		t.Fatalf("终态应命中缓存，实际查询 %d 次", got)
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
