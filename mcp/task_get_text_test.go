package mcp

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// toolText 取出 tools/call 结果里的 text content。
func toolText(t *testing.T, res map[string]any) string {
	t.Helper()
	content, _ := res["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("没有 content: %v", res)
	}
	first, _ := content[0].(map[string]any)
	return fmt.Sprint(first["text"])
}

// taskGetServer 起一个只回答指定查询路径的假上游，并装配 MCP 服务。
func taskGetServer(t *testing.T, path, body string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/contents/generations/tasks/"+path, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	os.Setenv("MCP_UPSTREAM_BASE_URL", srv.URL+"/v1")
	t.Cleanup(func() { os.Unsetenv("MCP_UPSTREAM_BASE_URL") })
	resetProtocol()

	if err := SaveProfile(&ModelProfile{
		ModelId: "text-video", Kind: "video", Enabled: true, Async: true,
	}); err != nil {
		t.Fatal(err)
	}
	return mcpServer(t)
}

func signedVideoTask(t *testing.T, upstream string) string {
	t.Helper()
	signed, err := signTaskID(taskPayload{
		Model: "text-video", Upstream: upstream, Kind: "video",
	})
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

// 完成的视频，text 里必须能直接看到地址。
//
// 只放 structuredContent 不够：多数 MCP 客户端只渲染 text，agent 会回一句
// 「已生成」而用户拿不到链接 —— 这正是 generate 当初踩过的坑。
func TestTaskGetTextCarriesOutputURL(t *testing.T) {
	setupDB(t)
	srv := taskGetServer(t, "task-text",
		`{"task_id":"t1","status":"succeeded","content":{"video_url":"https://example.test/a.mp4"}}`)

	res := rpc(t, srv.URL, "sk-test", "tools/call", map[string]any{
		"name": "task.get", "arguments": map[string]any{"task_id": signedVideoTask(t, "task-text")},
	})
	if isErr, _ := res["isError"].(bool); isErr {
		t.Fatalf("task.get 报错: %v", res["content"])
	}
	text := toolText(t, res)
	if !strings.Contains(text, "https://example.test/a.mp4") {
		t.Fatalf("text 里没有产物地址: %q", text)
	}
	sc, _ := res["structuredContent"].(map[string]any)
	outs, _ := sc["outputs"].([]any)
	if len(outs) != 1 {
		t.Fatalf("structuredContent 产物数 = %d", len(outs))
	}
}

// 失败原因要进 text：只渲染 text 的客户端否则只看到「failed」。
func TestTaskGetTextCarriesFailureReason(t *testing.T) {
	setupDB(t)
	srv := taskGetServer(t, "task-bad",
		`{"task_id":"t2","status":"FAILURE","error":{"message":"content blocked","code":"policy"}}`)

	res := rpc(t, srv.URL, "sk-test", "tools/call", map[string]any{
		"name": "task.get", "arguments": map[string]any{"task_id": signedVideoTask(t, "task-bad")},
	})
	// 任务本身失败是「查询成功」，不是工具错误：agent 需要读到 status=failed。
	if isErr, _ := res["isError"].(bool); isErr {
		t.Fatalf("已落定的失败任务不应回 isError: %v", res["content"])
	}
	text := toolText(t, res)
	if !strings.Contains(text, "content blocked") {
		t.Fatalf("text 里没有失败原因: %q", text)
	}
}

// 上游说成功却没给地址：如实说明，不要留一句「completed」让 agent 编链接。
func TestTaskGetTextWhenCompletedWithoutOutputs(t *testing.T) {
	setupDB(t)
	srv := taskGetServer(t, "task-empty", `{"task_id":"t3","status":"succeeded"}`)

	res := rpc(t, srv.URL, "sk-test", "tools/call", map[string]any{
		"name": "task.get", "arguments": map[string]any{"task_id": signedVideoTask(t, "task-empty")},
	})
	text := toolText(t, res)
	if !strings.Contains(text, "没有返回产物地址") {
		t.Fatalf("完成但无产物时应说明，实际 %q", text)
	}
}

// 同一个地址从 result_url 与 content.video_url 各出现一次时只能算一个产物。
// 不去重的话 text 会连着列出两行一样的链接，用户以为生成了两个视频。
func TestParseOutputsDedupesSameURL(t *testing.T) {
	t.Parallel()
	got := parseOutputs(map[string]any{
		"result_url": "https://example.test/same.mp4",
		"content":    map[string]any{"video_url": "https://example.test/same.mp4"},
	})
	if len(got) != 1 {
		t.Fatalf("同一地址应只算一个产物，实际 %d: %+v", len(got), got)
	}
	if got[0].URL != "https://example.test/same.mp4" {
		t.Fatalf("地址不对: %q", got[0].URL)
	}
}

// 失败原因有四种常见形状，取值优先级必须稳定。
func TestUpstreamFailReasonShapes(t *testing.T) {
	cases := []struct {
		name string
		resp map[string]any
		want string
	}{
		{"归一化字段优先", map[string]any{"fail_reason": "blocked", "error": "boom"}, "blocked"},
		{"对象 error", map[string]any{"error": map[string]any{"message": "content blocked"}}, "content blocked"},
		{"对象 error 只有 code", map[string]any{"error": map[string]any{"code": "policy"}}, "policy"},
		{"字符串 error", map[string]any{"error": "boom"}, "boom"},
		{"顶层 message", map[string]any{"message": "quota"}, "quota"},
		{"都没有", map[string]any{}, "上游任务失败"},
	}
	for _, tc := range cases {
		if got := upstreamFailReason(tc.resp); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// 伪造 / 过期的 task_id 必须是「可分类的工具错误」，不是一段纯中文。
// 之前 getTask 直接 return err，SDK 只能包成 isError + 文本，
// 声明在 outputSchema 里的 error 对象永远拿不到。
func TestTaskGetBadIDIsStructured(t *testing.T) {
	setupDB(t)
	srv := taskGetServer(t, "unused", `{}`)

	res := rpc(t, srv.URL, "sk-test", "tools/call", map[string]any{
		"name": "task.get", "arguments": map[string]any{"task_id": "bogus.deadbeef"},
	})
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Fatalf("非法 task_id 应回 isError: %v", res)
	}
	sc, ok := res["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("失败也要有 structuredContent，实际 %v", res)
	}
	terr, _ := sc["error"].(map[string]any)
	if code, _ := terr["code"].(string); code != CodeTaskIDInvalid {
		t.Fatalf("code = %v，期望 %s", terr["code"], CodeTaskIDInvalid)
	}
	if retry, _ := terr["retryable"].(bool); retry {
		t.Fatal("非法句柄不该标可重试")
	}
	if txt := toolText(t, res); !strings.Contains(txt, "无效") {
		t.Fatalf("text 应说明句柄无效，实际 %q", txt)
	}
}

// 上游查询失败（404 / 5xx）同样要带 code：query 便宜，但 agent 需要知道
// 是「句柄没了」还是「网关抖了」，两者的下一步不同。
func TestTaskGetUpstreamErrorIsStructured(t *testing.T) {
	setupDB(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/contents/generations/tasks/task-gone", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"message":"task not found","code":"task_not_exist"}}`)
	})
	up := httptest.NewServer(mux)
	t.Cleanup(up.Close)
	os.Setenv("MCP_UPSTREAM_BASE_URL", up.URL+"/v1")
	t.Cleanup(func() { os.Unsetenv("MCP_UPSTREAM_BASE_URL") })
	resetProtocol()
	if err := SaveProfile(&ModelProfile{ModelId: "text-video", Kind: "video", Enabled: true, Async: true}); err != nil {
		t.Fatal(err)
	}
	srv := mcpServer(t)

	res := rpc(t, srv.URL, "sk-test", "tools/call", map[string]any{
		"name": "task.get", "arguments": map[string]any{"task_id": signedVideoTask(t, "task-gone")},
	})
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Fatalf("上游 404 应回 isError: %v", res)
	}
	sc, _ := res["structuredContent"].(map[string]any)
	terr, _ := sc["error"].(map[string]any)
	code, _ := terr["code"].(string)
	if !strings.HasPrefix(code, "UPSTREAM_") {
		t.Fatalf("上游错误码应带 UPSTREAM_ 前缀，实际 %q", code)
	}
}

// 工具声明的 error 必须是对象。改回 string 时这条会红 —— 它守的是
// 「outputSchema 与实际返回同形」，而不是某个字段的具体取值。
func TestTaskGetOutputSchemaDeclaresErrorObject(t *testing.T) {
	srv := mcpServer(t)
	res := rpc(t, srv.URL, "sk-test", "tools/list", nil)

	tools, _ := res["tools"].([]any)
	for _, item := range tools {
		tool, _ := item.(map[string]any)
		if tool["name"] != "task.get" {
			continue
		}
		schema, _ := tool["outputSchema"].(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		errProp, _ := props["error"].(map[string]any)
		raw, _ := errProp["type"].(string)
		if raw == "string" {
			t.Fatalf("task.get 的 error 仍是 string: %v", errProp)
		}
		// 指针类型在 schema 里是 ["null","object"]；两种写法都接受。
		if list, ok := errProp["type"].([]any); ok {
			hasObject := false
			for _, v := range list {
				if v == "object" {
					hasObject = true
				}
			}
			if !hasObject {
				t.Fatalf("error 不是对象: %v", errProp)
			}
		}
		return
	}
	t.Fatal("tools/list 里没有 task.get")
}

// 非法 task_id 会被回显在 structuredContent 里，必须截断：
// 否则一次 1 MB 的垃圾输入能把响应放大成 1 MB。
func TestTaskGetBadIDDoesNotEchoHugeInput(t *testing.T) {
	setupDB(t)
	srv := taskGetServer(t, "unused", `{}`)

	res := rpc(t, srv.URL, "sk-test", "tools/call", map[string]any{
		"name": "task.get", "arguments": map[string]any{"task_id": strings.Repeat("a", 100000)},
	})
	sc, _ := res["structuredContent"].(map[string]any)
	got, _ := sc["task_id"].(string)
	if len(got) > 160 {
		t.Fatalf("回显的 task_id 没有截断，长度 %d", len(got))
	}
}
