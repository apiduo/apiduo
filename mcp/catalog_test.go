package mcp

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"

	"gorm.io/gorm"
)

func listResult(t *testing.T, srv *httptest.Server) (string, string) {
	t.Helper()
	res := rpc(t, srv.URL, "sk-test", "tools/call", map[string]any{
		"name": "models.list", "arguments": map[string]any{},
	})
	sc, ok := res["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("models.list 没有 structuredContent: %v", res)
	}
	version, _ := sc["catalog_version"].(string)
	return version, toolText(t, res)
}

// 目录版本必须跟着声明走：新增、改字段、删除都要让它变。
// 它是 agent 判断「手上这份清单过期了」的唯一依据。
func TestCatalogVersionTracksWrites(t *testing.T) {
	setupDB(t)
	p := ModelProfile{ModelId: "ver-image", Kind: "image", Enabled: true, Summary: "v1"}
	if err := SaveProfile(&p); err != nil {
		t.Fatal(err)
	}
	srv := mcpServer(t)

	v1, text1 := listResult(t, srv)
	if v1 == "" {
		t.Fatal("catalog_version 为空")
	}
	if !strings.Contains(text1, "清单版本 "+v1) {
		t.Fatalf("text 里应带版本号，实际 %q", text1)
	}

	p.Summary = "v2"
	if err := SaveProfile(&p); err != nil {
		t.Fatal(err)
	}
	v2, _ := listResult(t, srv)
	if v2 == v1 {
		t.Fatalf("改字段后版本没变: %s", v1)
	}

	if err := DeleteProfile(p.Id); err != nil {
		t.Fatal(err)
	}
	v3, _ := listResult(t, srv)
	if v3 == v2 {
		t.Fatalf("删除后版本没变: %s", v2)
	}
}

// 缓存必须真的生效：绕过写路径直接改库（也顺手把行版本 +1），
// TTL 内看到的仍是旧版本；TTL 之后才刷新。
func TestCatalogCacheHonoursTTL(t *testing.T) {
	setupDB(t)
	p := ModelProfile{ModelId: "cache-image", Kind: "image", Enabled: true, Summary: "v1"}
	if err := SaveProfile(&p); err != nil {
		t.Fatal(err)
	}
	srv := mcpServer(t)
	v1, _ := listResult(t, srv)

	// 模拟「另一个副本改了库」：本地没收到 invalidate，只能靠 TTL 兜底。
	if err := model.DB.Model(&ModelProfile{}).Where("model_id = ?", p.ModelId).
		Updates(map[string]any{"summary": "v2", "version": gorm.Expr("version + 1")}).Error; err != nil {
		t.Fatal(err)
	}
	if v, _ := listResult(t, srv); v != v1 {
		t.Fatalf("TTL 内不应刷新，v1=%s 现在=%s", v1, v)
	}

	time.Sleep(catalogTTL + 100*time.Millisecond)
	if v, _ := listResult(t, srv); v == v1 {
		t.Fatalf("TTL 之后应刷新，仍是 %s", v1)
	}
}

// models.list 不走 relay，没有额度与限流；窗口用尽后必须回结构化错误。
func TestCatalogListRateLimited(t *testing.T) {
	setupDB(t)
	p := ModelProfile{ModelId: "rate-image", Kind: "image", Enabled: true}
	if err := SaveProfile(&p); err != nil {
		t.Fatal(err)
	}
	srv := mcpServer(t)

	for i := 0; i < catalogListLimit; i++ {
		if _, ok := allowCatalogList(0); !ok {
			t.Fatalf("第 %d 次就限流了，窗口算错", i+1)
		}
	}
	res := rpc(t, srv.URL, "sk-test", "tools/call", map[string]any{
		"name": "models.list", "arguments": map[string]any{},
	})
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Fatalf("超限应回 isError: %v", res)
	}
	sc, _ := res["structuredContent"].(map[string]any)
	terr, _ := sc["error"].(map[string]any)
	if code, _ := terr["code"].(string); code != CodeRateLimited {
		t.Fatalf("code = %v，期望 %s", terr["code"], CodeRateLimited)
	}
	if retry, _ := terr["retryable"].(bool); !retry {
		t.Fatal("限流应标可重试")
	}
	if ms, _ := terr["retry_after_ms"].(float64); ms <= 0 {
		t.Fatalf("retry_after_ms 应为正数，实际 %v", terr["retry_after_ms"])
	}
}
