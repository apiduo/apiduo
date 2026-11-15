package mcp

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/model"
)

// 两个读工具必须各用各的窗口：否则 models.list 的 60 次额度会把 task.get 一起吃掉。
func TestRateLimitBucketsAreIndependent(t *testing.T) {
	resetRateLimits()
	defer resetRateLimits()

	for i := range taskGetLimit {
		if _, ok := allowTaskGet(7); !ok {
			t.Fatalf("第 %d 次就被限流了", i+1)
		}
	}
	if _, ok := allowTaskGet(7); ok {
		t.Fatal("超过 task.get 上限后应被限流")
	}
	// 另一个桶不受影响，另一个令牌也不受影响。
	if _, ok := allowCatalogList(7); !ok {
		t.Fatal("models.list 被 task.get 的窗口带累了")
	}
	if _, ok := allowTaskGet(8); !ok {
		t.Fatal("另一个令牌被带累了")
	}
}

// 任务失败的原因分两类：上游抖了可以重试（失败任务会退款），内容/参数问题重试无意义。
func TestClassifyTaskFailure(t *testing.T) {
	setupDB(t)

	transient := []string{
		"upstream error, please retry later",
		"Input material restore service is unavailable",
		"upstream 502 Bad Gateway",
		"request timeout",
	}
	for _, reason := range transient {
		te := classifyTaskFailure("m", reason)
		if !te.Retryable || te.RetryAction != "wait" {
			t.Fatalf("%q 应判为可重试，实际 %+v", reason, te)
		}
	}

	permanent := []string{
		"content blocked",
		"invalid parameter: duration",
		"copyright restriction",
	}
	for _, reason := range permanent {
		te := classifyTaskFailure("m", reason)
		if te.Retryable {
			t.Fatalf("%q 不该判为可重试，实际 %+v", reason, te)
		}
	}
}

// 配了规则就以规则为准，别让启发式覆盖管理员的判断。
func TestClassifyTaskFailurePrefersRule(t *testing.T) {
	setupDB(t)
	// SaveErrorRule 只更新已有规则（全局规则的 Match 是识别依据），新建走 DB。
	if err := model.DB.Create(&ErrorRule{
		Scope: "global", Match: "task:FAILED", Retryable: true,
		RetryAction: "wait", RetryAfterMs: 5000, Hint: "按规则等 5 秒", Enabled: true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	te := classifyTaskFailure("m", "task:FAILED quota exhausted")
	if !te.Retryable || te.RetryAfterMS != 5000 || te.Hint != "按规则等 5 秒" {
		t.Fatalf("规则没生效: %+v", te)
	}
}

// 异步任务的重放不该再给「进行中」：查一次当前状态，完成就直接给地址。
func TestVideoReplayRefreshesStatus(t *testing.T) {
	setupDB(t)
	rec := mockUpstream(t)
	resetProtocol()
	resetRateLimits()

	if err := SaveProfile(&ModelProfile{
		ModelId: "refresh-video", Kind: "video", Enabled: true, Async: true,
	}); err != nil {
		t.Fatal(err)
	}
	srv := mcpServer(t)

	args := map[string]any{
		"model": "refresh-video", "prompt": "a paper boat", "client_request_id": "rep-refresh",
	}
	first := callTool(t, srv.URL, "sk-test", "video.generate", args)
	if first["status"] != "working" {
		t.Fatalf("首次应是 working，实际 %v", first["status"])
	}
	if q := rec.queries.Load(); q != 0 {
		t.Fatalf("首次提交不该查任务，实际 %d 次", q)
	}

	second := callTool(t, srv.URL, "sk-test", "video.generate", args)
	if second["status"] != "completed" {
		t.Fatalf("重放应刷成 completed，实际 %v", second["status"])
	}
	if rp, _ := second["replayed"].(bool); !rp {
		t.Fatal("重放结果应带 replayed=true")
	}
	outs, _ := second["outputs"].([]any)
	if len(outs) != 1 {
		t.Fatalf("刷新后应带产物，实际 %v", second["outputs"])
	}
	if q := rec.queries.Load(); q != 1 {
		t.Fatalf("重放应只查一次任务，实际 %d 次", q)
	}

	// 第三次重放：已写回终态，不该再查。
	third := callTool(t, srv.URL, "sk-test", "video.generate", args)
	if third["status"] != "completed" {
		t.Fatalf("第三次应是 completed，实际 %v", third["status"])
	}
	if q := rec.queries.Load(); q != 1 {
		t.Fatalf("终态写回后不该再查任务，实际 %d 次", q)
	}
}

// task.get 不再回显整份上游对象：它占响应体近一半，且带 provider 模型名与任务号。
func TestTaskGetOmitsUpstreamEcho(t *testing.T) {
	setupDB(t)
	mockUpstream(t)
	resetProtocol()
	resetRateLimits()

	if err := SaveProfile(&ModelProfile{
		ModelId: "echo-video", Kind: "video", Enabled: true, Async: true,
	}); err != nil {
		t.Fatal(err)
	}
	srv := mcpServer(t)
	signed, err := signTaskID(taskPayload{Model: "echo-video", Upstream: "upstream-task-1", Kind: "video"})
	if err != nil {
		t.Fatal(err)
	}

	res := rpc(t, srv.URL, "sk-test", "tools/call", map[string]any{
		"name": "task.get", "arguments": map[string]any{"task_id": signed},
	})
	sc, _ := res["structuredContent"].(map[string]any)
	if _, exists := sc["upstream"]; exists {
		t.Fatalf("task.get 不该再回显 upstream: %v", sc["upstream"])
	}
	if sc["status"] != "completed" {
		t.Fatalf("状态丢失: %v", sc["status"])
	}
	content, _ := res["content"].([]any)
	if len(content) == 0 {
		t.Fatal("没有 text content")
	}
	if txt := fmt.Sprint(content[0].(map[string]any)["text"]); !strings.Contains(txt, "v.mp4") {
		t.Fatalf("text 仍应带产物地址: %q", txt)
	}
}

// 限流封顶时，失败的是「这次查询」而不是任务本身：status 必须是 working，
// 否则 agent 会以为任务失败、放弃等待。
func TestTaskGetRateLimitedKeepsTaskStatus(t *testing.T) {
	setupDB(t)
	mockUpstream(t)
	resetProtocol()
	resetRateLimits()
	t.Cleanup(resetRateLimits)

	if err := SaveProfile(&ModelProfile{
		ModelId: "rl-video", Kind: "video", Enabled: true, Async: true,
	}); err != nil {
		t.Fatal(err)
	}
	srv := mcpServer(t)
	signed, err := signTaskID(taskPayload{Model: "rl-video", Upstream: "upstream-task-1", Kind: "video"})
	if err != nil {
		t.Fatal(err)
	}

	// 把该令牌的窗口用光（测试环境里 caller 没有真实令牌，TokenId 为 0）。
	for range taskGetLimit {
		allowTaskGet(0)
	}

	res := rpc(t, srv.URL, "sk-test", "tools/call", map[string]any{
		"name": "task.get", "arguments": map[string]any{"task_id": signed},
	})
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Fatalf("限流应回 isError: %v", res)
	}
	sc, _ := res["structuredContent"].(map[string]any)
	if sc["status"] != "working" {
		t.Fatalf("限流时任务状态不该被写成 %v", sc["status"])
	}
	terr, _ := sc["error"].(map[string]any)
	if code, _ := terr["code"].(string); code != CodeRateLimited {
		t.Fatalf("code = %v，期望 %s", terr["code"], CodeRateLimited)
	}
	if retry, _ := terr["retryable"].(bool); !retry {
		t.Fatal("限流应可重试")
	}
}

// 上游 200 但没产物：计费很可能已发生，键不能释放，重放要维持 isError。
func TestGenerateEmptyOutputKeepsKey(t *testing.T) {
	setupDB(t)
	var calls atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/images/generations", func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"created":1,"data":[]}`)
	})
	up := httptest.NewServer(mux)
	t.Cleanup(up.Close)
	os.Setenv("MCP_UPSTREAM_BASE_URL", up.URL+"/v1")
	t.Cleanup(func() { os.Unsetenv("MCP_UPSTREAM_BASE_URL") })
	resetProtocol()
	if err := SaveProfile(&ModelProfile{ModelId: "empty-image", Kind: "image", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	srv := mcpServer(t)

	args := map[string]any{"model": "empty-image", "prompt": "x", "client_request_id": "rep-empty"}
	first := rpc(t, srv.URL, "sk-test", "tools/call", map[string]any{"name": "image.generate", "arguments": args})
	if isErr, _ := first["isError"].(bool); !isErr {
		t.Fatalf("首次应报错: %v", first)
	}
	second := rpc(t, srv.URL, "sk-test", "tools/call", map[string]any{"name": "image.generate", "arguments": args})
	if isErr, _ := second["isError"].(bool); !isErr {
		t.Fatalf("重放同一件失败必须是 isError: %v", second)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("键被释放了，上游被调用 %d 次", got)
	}
	content, _ := second["content"].([]any)
	if txt := fmt.Sprint(content[0].(map[string]any)["text"]); !strings.Contains(txt, "不再重新生成") {
		t.Fatalf("重放文案应说明不会重新生成: %q", txt)
	}
}

// 任务失败是「查询成功、任务失败」，重放两次都应 isError=false，且每次都刷新
// （失败结果不写回表，否则第二次会走成 isError=true）。
func TestVideoReplayFailedTaskStaysNonError(t *testing.T) {
	setupDB(t)
	var queries atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/contents/generations/tasks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"task_id":"fail-task","status":"queued"}`)
	})
	mux.HandleFunc("/v1/contents/generations/tasks/fail-task", func(w http.ResponseWriter, _ *http.Request) {
		queries.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"task_id":"fail-task","status":"failed","fail_reason":"content blocked"}`)
	})
	up := httptest.NewServer(mux)
	t.Cleanup(up.Close)
	os.Setenv("MCP_UPSTREAM_BASE_URL", up.URL+"/v1")
	t.Cleanup(func() { os.Unsetenv("MCP_UPSTREAM_BASE_URL") })
	resetProtocol()
	resetRateLimits()
	if err := SaveProfile(&ModelProfile{ModelId: "fail-video", Kind: "video", Enabled: true, Async: true}); err != nil {
		t.Fatal(err)
	}
	srv := mcpServer(t)

	args := map[string]any{"model": "fail-video", "prompt": "x", "client_request_id": "rep-fail-task"}
	if sc := callTool(t, srv.URL, "sk-test", "video.generate", args); sc["status"] != "working" {
		t.Fatalf("首次应 working: %v", sc)
	}
	for i := range 2 {
		res := rpc(t, srv.URL, "sk-test", "tools/call", map[string]any{"name": "video.generate", "arguments": args})
		if isErr, _ := res["isError"].(bool); isErr {
			t.Fatalf("第 %d 次重放不该是 isError: %v", i+2, res)
		}
		sc, _ := res["structuredContent"].(map[string]any)
		if sc["status"] != "failed" {
			t.Fatalf("第 %d 次重放 status=%v，期望 failed", i+2, sc["status"])
		}
		terr, _ := sc["error"].(map[string]any)
		if retry, _ := terr["retryable"].(bool); retry {
			t.Fatalf("content blocked 不该可重试: %v", terr)
		}
	}
	if got := queries.Load(); got != 2 {
		t.Fatalf("失败任务每次重放都该刷新，实际查询 %d 次", got)
	}
}

// 提示词有全局硬顶：没声明上限的模型也不能无限长。
func TestPromptHasGlobalCap(t *testing.T) {
	m := modelView{ID: "no-limit", Kind: "image"}
	_, err := buildPayload(m, generateInput{Prompt: strings.Repeat("字", maxPromptChars+1)}, nil)
	var pe *paramError
	if !errors.As(err, &pe) || pe.Code != CodePromptTooLong {
		t.Fatalf("应回 %s，实际 %v", CodePromptTooLong, err)
	}

	// 模型声明更小的上限时以声明为准。
	small := 100
	m2 := modelView{ID: "small", Kind: "image", MaxPromptChars: &small}
	_, err = buildPayload(m2, generateInput{Prompt: strings.Repeat("a", 200)}, nil)
	if !errors.As(err, &pe) || !strings.Contains(pe.Msg, "100") {
		t.Fatalf("应使用声明的 100，实际 %v", err)
	}
}

// 鉴权端点也要有请求体上限，否则一个令牌就能把进程内存打满。
func TestMCPRejectsOversizedBody(t *testing.T) {
	srv := mcpServer(t)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + strings.Repeat(" ", maxMCPBodyBytes+4096)
	resp, err := http.Post(srv.URL+"/mcp", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("超限请求被放行了: %d", resp.StatusCode)
	}
}

// cancelled / expired 也是 failed，但下一步动作不同，提示不能共用。
func TestTaskGetDistinguishesCancelledAndExpired(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{"cancelled", "取消"},
		{"expired", "过期"},
	} {
		setupDB(t)
		mux := http.NewServeMux()
		mux.HandleFunc("/v1/contents/generations/tasks/t-1", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"task_id":"t-1","status":%q}`, tc.raw)
		})
		up := httptest.NewServer(mux)
		os.Setenv("MCP_UPSTREAM_BASE_URL", up.URL+"/v1")
		resetProtocol()
		resetRateLimits()
		if err := SaveProfile(&ModelProfile{ModelId: "st-video", Kind: "video", Enabled: true, Async: true}); err != nil {
			t.Fatal(err)
		}
		srv := mcpServer(t)
		signed, err := signTaskID(taskPayload{Model: "st-video", Upstream: "t-1", Kind: "video"})
		if err != nil {
			t.Fatal(err)
		}
		sc := callTool(t, srv.URL, "sk-test", "task.get", map[string]any{"task_id": signed})
		terr, _ := sc["error"].(map[string]any)
		if msg := fmt.Sprint(terr["message"]); !strings.Contains(msg, tc.want) {
			t.Fatalf("%s: message=%q，期望包含 %q", tc.raw, msg, tc.want)
		}
		up.Close()
		os.Unsetenv("MCP_UPSTREAM_BASE_URL")
	}
}

// 生成结果不再回显整份上游对象。
func TestGenerateOmitsUpstreamEcho(t *testing.T) {
	setupDB(t)
	mockUpstream(t)
	resetProtocol()
	if err := SaveProfile(&ModelProfile{ModelId: "echo-image", Kind: "image", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	srv := mcpServer(t)
	sc := callTool(t, srv.URL, "sk-test", "image.generate", map[string]any{
		"model": "echo-image", "prompt": "x",
	})
	if _, exists := sc["upstream"]; exists {
		t.Fatalf("generate 不该再回显 upstream: %v", sc["upstream"])
	}
	if len(sc["outputs"].([]any)) != 1 {
		t.Fatalf("产物丢了: %v", sc["outputs"])
	}
}
