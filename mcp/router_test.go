package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// Register 是 apiduo 与本包唯一的接入点。这条测试锁住路由契约：
// 只要路由名改了，这里先红，而不是等页面 404 才发现。
func TestRegisterExposesRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// DB 为 nil 时 Migrate 会报错并 SysError，但不应中断注册 ——
	// MCP 是增值能力，建表失败不能让网关起不来。
	Register(r)

	want := []string{
		"GET /api/mcp/overview",
		"GET /api/mcp/candidates",
		"GET /api/mcp/profiles",
		"POST /api/mcp/profiles",
		"GET /api/mcp/profiles/:id",
		"PUT /api/mcp/profiles/:id",
		"GET /api/mcp/profiles/by-model",
		"PUT /api/mcp/profiles/by-model",
		"DELETE /api/mcp/profiles/:id",
		"POST /api/mcp/profiles/:id/toggle",
		"GET /api/mcp/profiles/:id/observations",
		"GET /api/mcp/profiles/:id/audit",
		"GET /api/mcp/error-rules",
		"PUT /api/mcp/error-rules/:id",
		"POST /mcp",
	}

	have := make(map[string]bool, len(r.Routes()))
	for _, rt := range r.Routes() {
		have[rt.Method+" "+rt.Path] = true
	}
	for _, w := range want {
		if !have[w] {
			t.Errorf("路由缺失: %s", w)
		}
	}
}

// 公网 /mcp 的一条红线：没带 Authorization 必须 401。
//
// 只验这一步是刻意的：TokenAuthReadOnly 之后的行为（查 token 表、IP 限制、分组）
// 是 apiduo 既有能力，由它自己的测试保证；这里要守住的是「不存在匿名入口」。
//
// 带令牌能进协议层，由 e2e_test.go 的 TestEndToEndProtocol 覆盖
// （那里直接挂协议 handler，绕过 token 查询，专注验协议本身）。
func TestRegisterRejectsAnonymousMCP(t *testing.T) {
	setupDB(t)
	mockUpstream(t)
	resetProtocol()

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Register(engine)
	srv := httptest.NewServer(engine)
	defer srv.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无令牌应 401，实际 %d", resp.StatusCode)
	}

	// 带了「头」但不是 Bearer，也不能被当成匿名放行。
	req2, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", strings.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Accept", "application/json, text/event-stream")
	req2.Header.Set("Authorization", "not-a-bearer")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode == http.StatusOK {
		t.Fatal("畸形 Authorization 不该返回 200")
	}
}
