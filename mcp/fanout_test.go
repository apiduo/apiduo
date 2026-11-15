package mcp

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fanoutUpstream 让每次调用返回不同的 URL，并可按调用序号注入失败。
func fanoutUpstream(t *testing.T, failAt map[int]bool, delay time.Duration) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/images/generations", func(w http.ResponseWriter, r *http.Request) {
		idx := int(calls.Add(1)) - 1
		time.Sleep(delay)
		if failAt[idx] {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":{"message":"boom"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"created":1,"data":[{"url":"https://example.test/%d.png"}]}`, idx)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	os.Setenv("MCP_UPSTREAM_BASE_URL", srv.URL+"/v1")
	t.Cleanup(func() { os.Unsetenv("MCP_UPSTREAM_BASE_URL") })
	return srv, &calls
}

// n 不再交给上游串行生成，而是拆成 n 个单张**并发**发出。
func TestImageFanoutIsConcurrent(t *testing.T) {
	setupDB(t)
	_, calls := fanoutUpstream(t, nil, 120*time.Millisecond)
	resetProtocol()
	resetRateLimits()
	n := 4
	if err := SaveProfile(&ModelProfile{
		ModelId: "fan-image", Kind: "image", Enabled: true,
		Parameters: fmt.Sprintf(`{"n":{"min":1,"max":%d}}`, n),
	}); err != nil {
		t.Fatal(err)
	}
	srv := mcpServer(t)

	start := time.Now()
	sc := callTool(t, srv.URL, "sk-test", "image.generate", map[string]any{
		"model": "fan-image", "prompt": "x", "n": n,
	})
	elapsed := time.Since(start)

	if got := calls.Load(); got != int64(n) {
		t.Fatalf("应发出 %d 个单张请求，实际 %d", n, got)
	}
	outs, _ := sc["outputs"].([]any)
	if len(outs) != n {
		t.Fatalf("产物数 = %d，期望 %d", len(outs), n)
	}
	// 串行需要 480ms；并发应接近单个耗时（120ms）+ 开销。
	if elapsed > 350*time.Millisecond {
		t.Fatalf("四个请求没有并发：耗时 %v", elapsed)
	}
}

// 部分失败：钱已经花了，把成功的交出去，失败数量写进 text。
func TestImageFanoutPartialFailure(t *testing.T) {
	setupDB(t)
	_, calls := fanoutUpstream(t, map[int]bool{1: true}, 0)
	resetProtocol()
	resetRateLimits()
	if err := SaveProfile(&ModelProfile{
		ModelId: "fan-image", Kind: "image", Enabled: true,
		Parameters: `{"n":{"min":1,"max":4}}`,
	}); err != nil {
		t.Fatal(err)
	}
	srv := mcpServer(t)

	// 只调用一次：再调一次会产生新的 3 个请求，断言就会取到第二批结果。
	res := rpc(t, srv.URL, "sk-test", "tools/call", map[string]any{
		"name":      "image.generate",
		"arguments": map[string]any{"model": "fan-image", "prompt": "x", "n": 3},
	})
	if isErr, _ := res["isError"].(bool); isErr {
		t.Fatalf("部分失败不该整体报错: %v", res)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("应发出 3 个请求，实际 %d", got)
	}
	sc, _ := res["structuredContent"].(map[string]any)
	outs, _ := sc["outputs"].([]any)
	if len(outs) != 2 {
		t.Fatalf("成功 2 张，实际 %d", len(outs))
	}
	content, _ := res["content"].([]any)
	txt := fmt.Sprint(content[0].(map[string]any)["text"])
	if !strings.Contains(txt, "另有 1 张失败") {
		t.Fatalf("text 应说明部分失败: %q", txt)
	}
}

// 全部失败才回错误，并保留上游状态码。
func TestImageFanoutAllFail(t *testing.T) {
	setupDB(t)
	fanoutUpstream(t, map[int]bool{0: true, 1: true}, 0)
	resetProtocol()
	resetRateLimits()
	if err := SaveProfile(&ModelProfile{
		ModelId: "fan-image", Kind: "image", Enabled: true,
		Parameters: `{"n":{"min":1,"max":4}}`,
	}); err != nil {
		t.Fatal(err)
	}
	srv := mcpServer(t)

	res := rpc(t, srv.URL, "sk-test", "tools/call", map[string]any{
		"name":      "image.generate",
		"arguments": map[string]any{"model": "fan-image", "prompt": "x", "n": 2},
	})
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Fatalf("全失败应报错: %v", res)
	}
	sc, _ := res["structuredContent"].(map[string]any)
	terr, _ := sc["error"].(map[string]any)
	if code, _ := terr["code"].(string); code != "UPSTREAM_500" {
		t.Fatalf("code = %v，期望 UPSTREAM_500", terr["code"])
	}
}
