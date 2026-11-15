package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// idemUpstream 起一个计数的假上游，返回它的调用次数与最后一次请求体。
func idemUpstream(t *testing.T, status int, body string) (*atomic.Int64, *atomic.Pointer[map[string]any]) {
	t.Helper()
	var calls atomic.Int64
	var last atomic.Pointer[map[string]any]
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/images/generations", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		last.Store(&payload)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	os.Setenv("MCP_UPSTREAM_BASE_URL", srv.URL+"/v1")
	t.Cleanup(func() { os.Unsetenv("MCP_UPSTREAM_BASE_URL") })
	resetProtocol()
	return &calls, &last
}

func idemServer(t *testing.T) *httptest.Server {
	t.Helper()
	if err := SaveProfile(&ModelProfile{ModelId: "idem-image", Kind: "image", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	return mcpServer(t)
}

// 同一个 client_request_id 连打两次，上游只应收到一次。
func TestGenerateIdempotentReplay(t *testing.T) {
	setupDB(t)
	calls, _ := idemUpstream(t, http.StatusOK,
		`{"created":1,"data":[{"url":"https://example.test/i.png"}]}`)
	srv := idemServer(t)

	args := map[string]any{
		"model": "idem-image", "prompt": "a cat", "client_request_id": "req-1",
	}
	first := callTool(t, srv.URL, "sk-test", "image.generate", args)
	second := callTool(t, srv.URL, "sk-test", "image.generate", args)

	if got := calls.Load(); got != 1 {
		t.Fatalf("上游应只被调用一次，实际 %d", got)
	}
	f1, _ := first["outputs"].([]any)
	f2, _ := second["outputs"].([]any)
	if len(f1) != 1 || len(f2) != 1 {
		t.Fatalf("两次产物数不对: %d / %d", len(f1), len(f2))
	}
	u1 := fmt.Sprint(f1[0].(map[string]any)["url"])
	u2 := fmt.Sprint(f2[0].(map[string]any)["url"])
	if u1 != u2 {
		t.Fatalf("重放结果应一致: %q vs %q", u1, u2)
	}

	res := rpc(t, srv.URL, "sk-test", "tools/call", map[string]any{
		"name": "image.generate", "arguments": args,
	})
	if txt := toolText(t, res); !strings.Contains(txt, "未重复生成") {
		t.Fatalf("重放的 text 应说明没有重复生成，实际 %q", txt)
	}
	// 只看 structuredContent 的客户端也要能分辨：否则它会以为这是第二次生成。
	if rp, _ := second["replayed"].(bool); !rp {
		t.Fatalf("重放的结果应带 replayed=true，实际 %v", second["replayed"])
	}
	if rp, _ := first["replayed"].(bool); rp {
		t.Fatalf("首次结果不该带 replayed，实际 %v", first["replayed"])
	}
}

// 同键首次还在跑时，第二次必须被挡住，而不是再发一次。
func TestGenerateIdempotentInFlight(t *testing.T) {
	setupDB(t)
	calls, _ := idemUpstream(t, http.StatusOK, `{"created":1,"data":[{"url":"https://example.test/i.png"}]}`)
	srv := idemServer(t)

	if _, err := reserveIdempotency(0, "image", "req-busy", ""); err != nil {
		t.Fatal(err)
	}
	res := rpc(t, srv.URL, "sk-test", "tools/call", map[string]any{
		"name": "image.generate",
		"arguments": map[string]any{
			"model": "idem-image", "prompt": "a cat", "client_request_id": "req-busy",
		},
	})
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Fatalf("在途同键应报错: %v", res)
	}
	sc, _ := res["structuredContent"].(map[string]any)
	terr, _ := sc["error"].(map[string]any)
	if code, _ := terr["code"].(string); code != CodeRequestInFlight {
		t.Fatalf("code = %v，期望 %s", terr["code"], CodeRequestInFlight)
	}
	if retry, _ := terr["retryable"].(bool); !retry {
		t.Fatal("在途应标可重试（等它跑完）")
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("在途请求不该打到上游，实际调用 %d 次", got)
	}
}

// 上游失败要释放占位：没产生结果，调用方应当能重试。
func TestGenerateIdempotentReleasedOnFailure(t *testing.T) {
	setupDB(t)
	calls, _ := idemUpstream(t, http.StatusInternalServerError,
		`{"error":{"message":"boom","code":"server_error"}}`)
	srv := idemServer(t)

	args := map[string]any{
		"model": "idem-image", "prompt": "a cat", "client_request_id": "req-fail",
	}
	rpc(t, srv.URL, "sk-test", "tools/call", map[string]any{"name": "image.generate", "arguments": args})
	rpc(t, srv.URL, "sk-test", "tools/call", map[string]any{"name": "image.generate", "arguments": args})

	if got := calls.Load(); got != 2 {
		t.Fatalf("失败后应释放占位，上游应被调用两次，实际 %d", got)
	}
}

// client_request_id 是协议字段，不能被当成模型参数透传给上游。
func TestClientRequestIDNotForwarded(t *testing.T) {
	setupDB(t)
	_, last := idemUpstream(t, http.StatusOK, `{"created":1,"data":[{"url":"https://example.test/i.png"}]}`)
	srv := idemServer(t)

	callTool(t, srv.URL, "sk-test", "image.generate", map[string]any{
		"model": "idem-image", "prompt": "a cat", "client_request_id": "req-x",
	})
	payload := *last.Load()
	if _, exists := payload["client_request_id"]; exists {
		t.Fatalf("client_request_id 被透传给上游了: %v", payload)
	}
}

// 超长键在本地就被拒，且不产生上游调用。
func TestClientRequestIDTooLong(t *testing.T) {
	setupDB(t)
	calls, _ := idemUpstream(t, http.StatusOK, `{"created":1,"data":[{"url":"https://example.test/i.png"}]}`)
	srv := idemServer(t)

	res := rpc(t, srv.URL, "sk-test", "tools/call", map[string]any{
		"name": "image.generate",
		"arguments": map[string]any{
			"model": "idem-image", "prompt": "a cat",
			"client_request_id": strings.Repeat("x", 65),
		},
	})
	sc, _ := res["structuredContent"].(map[string]any)
	terr, _ := sc["error"].(map[string]any)
	if code, _ := terr["code"].(string); code != CodeParamOutOfRange {
		t.Fatalf("code = %v，期望 %s", terr["code"], CodeParamOutOfRange)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("本地拒绝不该打上游，实际 %d", got)
	}
}

// 首次调用的进程挂了，占位停在 pending：超过阈值后应允许接管，
// 而不是把这个键锁到 24 小时清理。
func TestIdempotentStalePendingIsTakenOver(t *testing.T) {
	setupDB(t)
	calls, _ := idemUpstream(t, http.StatusOK,
		`{"created":1,"data":[{"url":"https://example.test/i.png"}]}`)
	srv := idemServer(t)

	rec := IdempotencyRecord{
		TokenId: 0, Tool: "image", ClientRequestId: "req-stale",
		Status: idempotencyPending, CreatedAt: common.GetTimestamp() - 3600,
	}
	if err := model.DB.Create(&rec).Error; err != nil {
		t.Fatal(err)
	}

	callTool(t, srv.URL, "sk-test", "image.generate", map[string]any{
		"model": "idem-image", "prompt": "a cat", "client_request_id": "req-stale",
	})
	if got := calls.Load(); got != 1 {
		t.Fatalf("陈旧占位应被接管并放行，上游调用 %d 次", got)
	}
}

// 同一个 client_request_id 配另一组参数，必须报错而不是静默返回上一次的产物。
// 这不是「幂等命中」，是「键被复用」—— 重放会让 agent 以为自己生成了新东西。
func TestIdempotentKeyReuseDifferentPayloadRejected(t *testing.T) {
	setupDB(t)
	calls, _ := idemUpstream(t, http.StatusOK,
		`{"created":1,"data":[{"url":"https://example.test/first.png"}]}`)
	srv := idemServer(t)

	first := callTool(t, srv.URL, "sk-test", "image.generate", map[string]any{
		"model": "idem-image", "prompt": "a red apple", "client_request_id": "req-reuse",
	})
	if _, ok := first["outputs"]; !ok {
		t.Fatalf("首次应成功: %v", first)
	}

	res := rpc(t, srv.URL, "sk-test", "tools/call", map[string]any{
		"name": "image.generate",
		"arguments": map[string]any{
			"model": "idem-image", "prompt": "a blue car", "client_request_id": "req-reuse",
		},
	})
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Fatalf("同键不同参数应报错: %v", res)
	}
	sc, _ := res["structuredContent"].(map[string]any)
	terr, _ := sc["error"].(map[string]any)
	if code, _ := terr["code"].(string); code != CodeIdempotencyKeyReused {
		t.Fatalf("code = %v，期望 %s", terr["code"], CodeIdempotencyKeyReused)
	}
	if retry, _ := terr["retryable"].(bool); retry {
		t.Fatal("键复用不该标可重试")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("只应有一次上游调用，实际 %d", got)
	}
}

// 参数等价（显式传默认值 vs 不传）不算复用：指纹取自归一化后的载荷。
func TestIdempotentKeyReuseDetectsParamChange(t *testing.T) {
	setupDB(t)
	calls, _ := idemUpstream(t, http.StatusOK,
		`{"created":1,"data":[{"url":"https://example.test/x.png"}]}`)
	srv := idemServer(t)

	callTool(t, srv.URL, "sk-test", "image.generate", map[string]any{
		"model": "idem-image", "prompt": "a cat", "client_request_id": "req-norm",
	})
	res := rpc(t, srv.URL, "sk-test", "tools/call", map[string]any{
		"name": "image.generate",
		"arguments": map[string]any{
			"model": "idem-image", "prompt": "a cat", "client_request_id": "req-norm",
			"n": 1, "size": "1024x1024",
		},
	})
	sc, _ := res["structuredContent"].(map[string]any)
	// n=1 与 size 都会进 payload，指纹因此不同 —— 这里断言的是「被识别为复用」，
	// 不是静默重放：说明判定看的是载荷而不是「有没有多传键」。
	if code := (sc["error"]).(map[string]any)["code"]; code != CodeIdempotencyKeyReused {
		t.Fatalf("载荷不同应判为键复用，实际 %v", code)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("上游应只调用一次，实际 %d", got)
	}
}

// 幂等表只存地址，不存图片字节；但**不能**因此清掉本次响应里的 b64。
// out.Outputs 与返回给调用方的是同一个底层数组，原地裁剪会两个一起改。
func TestCompleteIdempotencyDropsInlineButKeepsResponse(t *testing.T) {
	setupDB(t)
	out := generateOutput{
		Status: "completed", Model: "m",
		Outputs: []outputItem{
			{B64JSON: "AAAA"},
			{URL: "https://example.test/a.png", B64JSON: "BBBB"},
		},
	}
	if _, err := reserveIdempotency(0, "image", "req-inline", ""); err != nil {
		t.Fatal(err)
	}
	completeIdempotency(0, "image", "req-inline", "生成完成", out)

	// 调用方手里的对象不能被改。
	if out.Outputs[0].B64JSON != "AAAA" || out.Outputs[1].B64JSON != "BBBB" {
		t.Fatalf("调用方的产物被就地清空了: %#v", out.Outputs)
	}

	var rec IdempotencyRecord
	if err := model.DB.Where("client_request_id = ?", "req-inline").First(&rec).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rec.Result, "AAAA") || strings.Contains(rec.Result, "BBBB") {
		t.Fatalf("表里仍有 base64: %s", rec.Result)
	}
	if !strings.Contains(rec.Result, "https://example.test/a.png") {
		t.Fatalf("地址应保留: %s", rec.Result)
	}
	if !strings.Contains(rec.Result, "未缓存") {
		t.Fatalf("应说明有产物未缓存: %s", rec.Result)
	}
}

// 只有「确定什么都没发生」才允许放行重试。
func TestShouldReleaseIdempotency(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"上游 4xx/5xx（有响应）", &upError{Status: 400, Msg: "bad"}, true},
		{"拨号超时（没发出去）", &errUpstreamTimeout{Msg: "x", BeforeSend: true}, true},
		{"等响应超时（可能已计费）", &errUpstreamTimeout{Msg: "x", BeforeSend: false}, false},
		{"编码/构造失败（没发出去）", &errNotSent{Err: errors.New("bad body")}, true},
		{"200 但解析失败（结论未知）", errors.New("解析上游响应失败"), false},
		{"普通错误（结论未知）", errors.New("boom"), false},
	}
	for _, c := range cases {
		if got := shouldReleaseIdempotency(c.err); got != c.want {
			t.Fatalf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
