package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func resetAdminCircuit() {
	adminCircuit.Lock()
	adminCircuit.entries = make(map[int]*adminCircuitEntry, 32)
	adminCircuit.Unlock()
}

func adminCircuitTestContext(t *testing.T) *gin.Context {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/api/channel/", nil)
	return c
}

// 判据是广度不是次数：同一个端点连点十次仍然是误操作，不该熔断。
func TestAdminCircuitIgnoresRepeatedSameEndpoint(t *testing.T) {
	setupDashboardAuthMiddlewareTest(t)
	resetAdminCircuit()

	user := &model.UserBase{Id: 42, Username: "probe", Role: common.RoleCommonUser}
	for range 10 {
		opened, _ := noteAdminDenial(user.Id, "/api/channel/")
		require.False(t, opened, "same endpoint must never open the circuit")
	}
	require.False(t, adminCircuitBlocked(adminCircuitTestContext(t), user))
}

func TestAdminCircuitOpensAfterCrossEndpointProbes(t *testing.T) {
	setupDashboardAuthMiddlewareTest(t)
	resetAdminCircuit()

	user := &model.UserBase{Id: 43, Username: "scanner", Role: common.RoleCommonUser}
	opened, _ := noteAdminDenial(user.Id, "/api/channel/")
	require.False(t, opened)
	opened, _ = noteAdminDenial(user.Id, "/api/option/")
	require.False(t, opened)
	opened, routes := noteAdminDenial(user.Id, "/api/user/")
	require.True(t, opened, "third distinct endpoint must open the circuit")
	require.Equal(t, []string{"/api/channel/", "/api/option/", "/api/user/"}, routes)

	require.True(t, adminCircuitBlocked(adminCircuitTestContext(t), user))
}

// 熔断到期自动恢复，并留下一条带 suppressed 计数的汇总。
func TestAdminCircuitExpiresAndReportsSuppressed(t *testing.T) {
	setupDashboardAuthMiddlewareTest(t)
	resetAdminCircuit()

	user := &model.UserBase{Id: 44, Username: "scanner", Role: common.RoleCommonUser}
	noteAdminDenial(user.Id, "/api/channel/")
	noteAdminDenial(user.Id, "/api/option/")
	noteAdminDenial(user.Id, "/api/user/")

	c := adminCircuitTestContext(t)
	require.True(t, adminCircuitBlocked(c, user))
	require.True(t, adminCircuitBlocked(c, user))

	adminCircuit.Lock()
	adminCircuit.entries[user.Id].openedAt = time.Now().Add(-adminCircuitTTL - time.Minute)
	adminCircuit.Unlock()

	require.False(t, adminCircuitBlocked(c, user), "expired circuit must let the request through")

	var recovered []model.AuditLog
	require.NoError(t, model.LOG_DB.Where("action = ?", "access.denied.circuit_recovered").Find(&recovered).Error)
	require.Len(t, recovered, 1)
	// Params 落库再读回来是原始 JSON，不是 Go 整数。
	raw, ok := recovered[0].Other.Op.Params["suppressed_count"].(json.RawMessage)
	require.True(t, ok, "suppressed_count should round-trip as raw JSON")
	require.Equal(t, "2", string(raw))
}

func TestAdminCircuitCanBeDisabled(t *testing.T) {
	setupDashboardAuthMiddlewareTest(t)
	resetAdminCircuit()

	previous := adminCircuitEnabled
	adminCircuitEnabled = false
	t.Cleanup(func() { adminCircuitEnabled = previous })

	user := &model.UserBase{Id: 45, Username: "scanner", Role: common.RoleCommonUser}
	for _, route := range []string{"/a", "/b", "/c", "/d"} {
		opened, _ := noteAdminDenial(user.Id, route)
		require.False(t, opened, "kill switch must stop all circuit opening")
	}
}

// 熔断的价值是停止写入，不是拦截请求：调用方本来就会 403。
func TestDeniedAccessAuditStopsWritingAfterCircuitOpens(t *testing.T) {
	setupDashboardAuthMiddlewareTest(t)
	resetAdminCircuit()

	user := &model.UserBase{Id: 46, Username: "scanner", Role: common.RoleCommonUser}
	denied := func(path string) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, path, nil)
		recordDeniedAccessAudit(c, user, common.RoleAdminUser)
	}

	denied("/api/channel/")
	denied("/api/option/")
	denied("/api/user/") // 第三个端点：打开熔断，本条仍然留痕
	denied("/api/log/")
	denied("/api/user/manage")

	var deniedLogs []model.AuditLog
	require.NoError(t, model.LOG_DB.Where("action = ?", "access.denied").Order("id").Find(&deniedLogs).Error)
	require.Len(t, deniedLogs, 3, "probes before the circuit opened stay on record")

	var opened []model.AuditLog
	require.NoError(t, model.LOG_DB.Where("action = ?", "access.denied.circuit_open").Find(&opened).Error)
	require.Len(t, opened, 1)
}
