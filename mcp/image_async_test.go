package mcp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

// 选路是「同步与异步收同一个 body、只换提交路径」的唯一实现点。
// 四种组合都要对，其中视频两半同值 —— 那是刻意的，不是漏写。
func TestTransportWithExecution(t *testing.T) {
	cases := []struct {
		kind  string
		async bool
		want  string
	}{
		{"image", false, "/images/generations"},
		{"image", true, "/images/generations/async"},
		{"video", false, "/contents/generations/tasks"},
		{"video", true, "/contents/generations/tasks"},
	}
	for _, c := range cases {
		base, err := getTransport(c.kind)
		if err != nil {
			t.Fatalf("%s: %v", c.kind, err)
		}
		got := base.withExecution(c.async)
		if got.Generate != c.want {
			t.Fatalf("%s async=%v: Generate = %q，期望 %q", c.kind, c.async, got.Generate, c.want)
		}
		if got.Query != base.Query {
			t.Fatalf("%s async=%v: 形态解析不该改 Query", c.kind, c.async)
		}
	}
}

// asyncUpstream 起一个只认异步端点的假上游，记录打过的路径。
func asyncUpstream(t *testing.T, respond string, status int) (*atomic.Int64, *atomic.Bool) {
	t.Helper()
	var asyncCalls atomic.Int64
	var hitSync atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/images/generations/async", func(w http.ResponseWriter, r *http.Request) {
		asyncCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, respond)
	})
	// 同步路径单独记账而不是不注册：命中它就是选路错了，
	// 404 只会让测试停在「上游报错」，看不出根因。
	mux.HandleFunc("/v1/images/generations", func(w http.ResponseWriter, r *http.Request) {
		hitSync.Store(true)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"url":"https://example.test/sync.png"}]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	os.Setenv("MCP_UPSTREAM_BASE_URL", srv.URL+"/v1")
	t.Cleanup(func() { os.Unsetenv("MCP_UPSTREAM_BASE_URL") })
	return &asyncCalls, &hitSync
}

// 声明为 async 的图片模型必须打异步端点，并立刻交出 task_id。
func TestImageAsyncSubmitsToAsyncEndpoint(t *testing.T) {
	setupDB(t)
	asyncCalls, hitSync := asyncUpstream(t, `{"task_id":"task_up1"}`, http.StatusAccepted)
	resetProtocol()
	resetRateLimits()
	if err := SaveProfile(&ModelProfile{
		ModelId: "async-image", Kind: "image", Async: true, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	srv := mcpServer(t)

	sc := callTool(t, srv.URL, "sk-test", "image.generate", map[string]any{
		"model": "async-image", "prompt": "x",
	})

	if hitSync.Load() {
		t.Fatal("异步模型打到了同步端点")
	}
	if got := asyncCalls.Load(); got != 1 {
		t.Fatalf("异步端点调用 %d 次，期望 1", got)
	}
	if status, _ := sc["status"].(string); status != "working" {
		t.Fatalf("status = %v，期望 working", sc["status"])
	}
	if tid, _ := sc["task_id"].(string); tid == "" {
		t.Fatalf("异步提交必须返回 task_id: %v", sc)
	}
}

// 同步模型的路径不能被异步半边带偏。
func TestImageSyncKeepsSyncEndpoint(t *testing.T) {
	setupDB(t)
	asyncCalls, _ := asyncUpstream(t, `{"data":[{"url":"https://example.test/a.png"}]}`, http.StatusOK)
	resetProtocol()
	resetRateLimits()
	if err := SaveProfile(&ModelProfile{
		ModelId: "sync-image", Kind: "image", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	srv := mcpServer(t)

	sc := callTool(t, srv.URL, "sk-test", "image.generate", map[string]any{
		"model": "sync-image", "prompt": "x",
	})

	if got := asyncCalls.Load(); got != 0 {
		t.Fatalf("同步模型不该打异步端点，实际 %d 次", got)
	}
	if status, _ := sc["status"].(string); status != "completed" {
		t.Fatalf("status = %v，期望 completed", sc["status"])
	}
}

// n>1 在异步下不能拆：拆开就是 N 个已计费的 task_id，而输出只能装一个。
func TestImageAsyncDoesNotFanout(t *testing.T) {
	setupDB(t)
	asyncCalls, _ := asyncUpstream(t, `{"task_id":"task_up1"}`, http.StatusAccepted)
	resetProtocol()
	resetRateLimits()
	if err := SaveProfile(&ModelProfile{
		ModelId: "async-batch", Kind: "image", Async: true, Enabled: true,
		Parameters: `{"n":{"min":1,"max":4}}`,
	}); err != nil {
		t.Fatal(err)
	}
	srv := mcpServer(t)

	callTool(t, srv.URL, "sk-test", "image.generate", map[string]any{
		"model": "async-batch", "prompt": "x", "n": 4,
	})

	if got := asyncCalls.Load(); got != 1 {
		t.Fatalf("异步 n=4 应只提交 1 次，实际 %d 次", got)
	}
}

// 只读 text 的客户端看不到 structuredContent 的 execution，
// 清单标签是它判断「要不要轮询」的唯一线索。
func TestModelsSummaryMarksAsync(t *testing.T) {
	async := modelView{ID: "a-img", Kind: "image", Async: true, Summary: "异步出图"}
	sync := modelView{ID: "s-img", Kind: "image", Summary: "同步出图"}
	txt := modelsSummary([]map[string]any{async.card(), sync.card()})

	if !strings.Contains(txt, "a-img（image，异步") {
		t.Fatalf("异步模型在 text 里没有标记: %q", txt)
	}
	if strings.Contains(txt, "s-img（image，异步") {
		t.Fatalf("同步模型被误标为异步: %q", txt)
	}
}
