package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/apiduo/trace"

	"github.com/gin-gonic/gin"
)

// 这条接口把「控制台地址 + 项目 id」交给管理员，拿到它就能顺着 trace id 读到
// 完整对话 —— 比「能读日志」更强的权限。所以它必须自己判一次：root 或名单内的人，
// 其余一律 403，而且**密钥永远不出现在响应里**。
func TestGetApiduoTraceConfigAuthorization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	restore := snapshotTraceSettings()
	t.Cleanup(restore)

	call := func(role, userID int) (int, map[string]any) {
		t.Helper()
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/apiduo/trace/config", nil)
		c.Set("role", role)
		c.Set("id", userID)
		GetApiduoTraceConfig(c)

		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode body: %v (%s)", err, rec.Body.String())
		}
		return rec.Code, body
	}

	// 普通管理员、不在名单里 → 403
	status, body := call(common.RoleAdminUser, 7)
	if status != http.StatusForbidden || body["success"] != false {
		t.Fatalf("admin without grant = %d %v, want 403", status, body)
	}

	// 把 7 号加进名单 → 200
	applyTraceViewers([]int{7})
	if status, body = call(common.RoleAdminUser, 7); status != http.StatusOK || body["success"] != true {
		t.Fatalf("listed admin = %d %v, want 200", status, body)
	}

	// root 永远可见，不需要名单
	applyTraceViewers(nil)
	if status, body = call(common.RoleRootUser, 99); status != http.StatusOK || body["success"] != true {
		t.Fatalf("root = %d %v, want 200", status, body)
	}

	// 响应里只能有拼链接需要的东西：密钥不能出现。
	data, _ := body["data"].(map[string]any)
	raw, _ := json.Marshal(data)
	if secret := trace.CurrentConfig().SecretKey; secret != "" && strings.Contains(string(raw), secret) {
		t.Fatalf("response leaked the secret key: %s", raw)
	}
	if strings.Contains(strings.ToLower(string(raw)), "secret") {
		t.Fatalf("response carries a secret-shaped field: %s", raw)
	}
}

func applyTraceViewers(ids []int) {
	conf := trace.CurrentConfig()
	defaults := trace.Rates{}
	policy := conf.Policy
	if policy != nil {
		defaults = policy.Defaults
	}
	settings := trace.Settings{
		Defaults:      defaults,
		Location:      conf.Location,
		ParentBased:   conf.ParentBased,
		ViewerUserIDs: ids,
	}
	if policy != nil {
		settings.Rules = policy.Rules
	}
	trace.ApplySettings(settings)
}

func snapshotTraceSettings() func() {
	conf := trace.CurrentConfig()
	policy := conf.Policy
	defaults := trace.Rates{}
	var rules []trace.Rule
	if policy != nil {
		defaults = policy.Defaults
		rules = policy.Rules
	}
	location := conf.Location
	if location == nil {
		location = time.UTC
	}
	return func() {
		trace.ApplySettings(trace.Settings{
			Defaults:      defaults,
			Rules:         rules,
			Location:      location,
			ParentBased:   conf.ParentBased,
			ViewerUserIDs: conf.TraceViewerUserIDs,
		})
	}
}
