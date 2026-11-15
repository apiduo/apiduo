package mcp

import (
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
)

// SDK 默认会带 logging 能力，但 JSONResponse 下发不出 notifications/message，
// 而且该能力在 2026-07-28 已废弃。声明一个兑现不了的能力只会误导排障。
func TestInitializeOmitsDeprecatedLogging(t *testing.T) {
	srv := mcpServer(t)
	res := rpc(t, srv.URL, "sk-test", "initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "probe", "version": "1"},
	})
	caps, _ := res["capabilities"].(map[string]any)
	if _, exists := caps["logging"]; exists {
		t.Fatalf("不该再声明 logging: %v", caps)
	}
	tools, _ := caps["tools"].(map[string]any)
	if tools == nil {
		t.Fatalf("tools 能力丢了: %v", caps)
	}
}

// extra 通道的 n 必须和顶层走同一条判定，否则 extra.n=1000 只被 relay 截住。
func TestBatchNRejectsExtraOverLimit(t *testing.T) {
	setupDB(t)
	rec := mockUpstream(t)
	resetProtocol()
	if err := SaveProfile(&ModelProfile{ModelId: "loose-image", Kind: "image", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	srv := mcpServer(t)

	res := rpc(t, srv.URL, "sk-test", "tools/call", map[string]any{
		"name": "image.generate",
		"arguments": map[string]any{
			"model": "loose-image", "prompt": "a cat",
			"extra": map[string]any{"n": 1000},
		},
	})
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Fatalf("extra.n=1000 应被本地拒绝: %v", res)
	}
	sc, _ := res["structuredContent"].(map[string]any)
	terr, _ := sc["error"].(map[string]any)
	if code, _ := terr["code"].(string); code != CodeParamOutOfRange {
		t.Fatalf("code = %v，期望 %s", terr["code"], CodeParamOutOfRange)
	}
	if rec.imagePayload() != nil {
		t.Fatalf("本地拒绝不该出站: %v", rec.imagePayload())
	}

	res = rpc(t, srv.URL, "sk-test", "tools/call", map[string]any{
		"name": "image.generate",
		"arguments": map[string]any{
			"model": "loose-image", "prompt": "a cat",
			"extra": map[string]any{"n": 0},
		},
	})
	sc, _ = res["structuredContent"].(map[string]any)
	terr, _ = sc["error"].(map[string]any)
	if code, _ := terr["code"].(string); code != CodeParamOutOfRange {
		t.Fatalf("extra.n=0 应被拒，code = %v", terr["code"])
	}
}

// 合规的 extra.n 仍要能透传（别把整条通道一起关掉）。
func TestBatchNExtraWithinLimitPasses(t *testing.T) {
	setupDB(t)
	rec := mockUpstream(t)
	resetProtocol()
	if err := SaveProfile(&ModelProfile{ModelId: "loose-image", Kind: "image", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	srv := mcpServer(t)

	callTool(t, srv.URL, "sk-test", "image.generate", map[string]any{
		"model": "loose-image", "prompt": "a cat",
		"extra": map[string]any{"n": 4},
	})
	payload := rec.imagePayload()
	if payload == nil {
		t.Fatal("合规请求应出站")
	}
	if got := fmt.Sprint(payload["n"]); got != "4" {
		t.Fatalf("n 应透传为 4，实际 %q", got)
	}
}

// serverInfo 是排障时唯一能确定「连的是哪个构建」的字段，必须跟随构建版本。
func TestServerInfoVersionFollowsBuild(t *testing.T) {
	srv := mcpServer(t)
	res := rpc(t, srv.URL, "sk-test", "initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "probe", "version": "1"},
	})
	info, _ := res["serverInfo"].(map[string]any)
	version, _ := info["version"].(string)
	if !strings.HasPrefix(version, "mcp/") {
		t.Fatalf("版本应形如 mcp/<build>，实际 %q", version)
	}
	if !strings.Contains(version, common.Version) {
		t.Fatalf("版本应包含构建号 %q，实际 %q", common.Version, version)
	}
}

// 工具清单是静态的，标 0 等于让客户端每个会话重拉一次。
func TestToolsListIsCacheable(t *testing.T) {
	srv := mcpServer(t)
	res := rpc(t, srv.URL, "sk-test", "tools/list", nil)
	if ttl, _ := res["ttlMs"].(float64); ttl <= 0 {
		t.Fatalf("tools/list 应给正 TTL，实际 %v", res["ttlMs"])
	}
	if scope, _ := res["cacheScope"].(string); scope != "public" {
		t.Fatalf("cacheScope 应为 public，实际 %v", res["cacheScope"])
	}
}

// 每个工具都该有人类可读名，UI 里显示 image.generate 不如「生成图片」。
func TestToolsHaveTitles(t *testing.T) {
	srv := mcpServer(t)
	res := rpc(t, srv.URL, "sk-test", "tools/list", nil)
	tools, _ := res["tools"].([]any)
	if len(tools) == 0 {
		t.Fatal("没有工具")
	}
	for _, item := range tools {
		tool, _ := item.(map[string]any)
		if title, _ := tool["title"].(string); strings.TrimSpace(title) == "" {
			t.Errorf("工具 %v 没有 title", tool["name"])
		}
	}
}
