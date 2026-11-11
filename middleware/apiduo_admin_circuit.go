/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
package middleware

import (
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// 管理面自动熔断。
//
// 为什么审计不够：access.denied 是事后证据，不是防线。一个脚本可以在一分钟内
// 扫完所有管理端点，每条都留痕，但每一条都是走到 handler 前才 403 —— 审计表
// 因此变成一个能被无限灌写的介质，而「这是同一次扫描」这个真正的信号，
// 要人工聚合才看得出来。
//
// 判据为什么是「跨端点广度」而不是「请求次数」：
//
//	正常用户误触管理入口，通常是同一个按钮连点（前端没反应），次数可以很高，
//	但 distinct route 恒为 1；扫描器必须换端点，因为它要找的是「哪个端点开着」。
//	所以 distinct route >= 3 这个判据对真实用户几乎不可能误伤，对扫描器一击必中。
//
// 为什么熔断是静默的（对外仍返回 403）：
//
//	返回 429 等于告诉攻击者「你被限流了」，他会换 IP、换账号、拉长间隔；
//	返回与权限不足完全一致的 403，他得不到任何关于熔断是否生效的反馈。
//	运维侧靠审计里的 circuit_open / circuit_recovered 两条记录获知，不靠响应码。

const (
	// adminCircuitRouteThreshold 判定扫描的端点广度阈值。
	adminCircuitRouteThreshold = 3
	// adminCircuitWindow 广度统计的滑动窗口。
	adminCircuitWindow = time.Minute
	// adminCircuitTTL 熔断时长。到点自动恢复，不需要人工介入。
	adminCircuitTTL = 30 * time.Minute
	// adminCircuitCapacity 触发过期清理的水位，防止长时间运行后 map 无界增长。
	adminCircuitCapacity = 4096
)

// adminCircuitEnabled 应急开关。误伤时置 false 并重启即可解除所有熔断。
var adminCircuitEnabled = common.GetEnvOrDefaultBool("APIDUO_ADMIN_CIRCUIT_ENABLED", true)

type adminCircuitEntry struct {
	routes      map[string]struct{}
	windowStart time.Time
	openedAt    time.Time
	suppressed  int
}

var adminCircuit = struct {
	sync.Mutex
	entries map[int]*adminCircuitEntry
}{entries: make(map[int]*adminCircuitEntry, 32)}

// adminCircuitBlocked 判断该用户是否处于熔断期。
//
// 熔断到期时顺手写一条恢复汇总：期间被静默吞掉多少请求，是判断「他还在不在」
// 的唯一依据 —— 熔断期内不写 access.denied，否则静默就失去意义了。
func adminCircuitBlocked(c *gin.Context, user *model.UserBase) bool {
	if !adminCircuitEnabled {
		return false
	}

	now := time.Now()

	adminCircuit.Lock()
	entry, ok := adminCircuit.entries[user.Id]
	if !ok || entry.openedAt.IsZero() {
		adminCircuit.Unlock()
		return false
	}
	if now.Sub(entry.openedAt) < adminCircuitTTL {
		entry.suppressed++
		adminCircuit.Unlock()
		return true
	}
	suppressed := entry.suppressed
	openedAt := entry.openedAt
	delete(adminCircuit.entries, user.Id)
	adminCircuit.Unlock()

	model.RecordAuditLog(c, model.AuditLog{
		UserId:    user.Id,
		Username:  user.Username,
		ActorRole: user.Role,
		Category:  model.AuditCategorySecurity,
		Action:    "access.denied.circuit_recovered",
		Content:   "Admin access circuit closed automatically",
		Status:    http.StatusForbidden,
		Success:   false,
		Other: model.AuditOther{
			Op: &model.AuditOperation{Action: "access.denied.circuit_recovered", Params: model.AuditFields{
				"opened_at":        openedAt.Unix(),
				"ttl_seconds":      int(adminCircuitTTL.Seconds()),
				"suppressed_count": suppressed,
			}},
		},
	})
	return false
}

// noteAdminDenial 记一次「已认证但权限不足」的访问，返回是否在这一次打开了熔断。
// 只在打开的那一刻返回 true，后续同处于熔断期的请求不会走到这里。
func noteAdminDenial(userID int, route string) (bool, []string) {
	if !adminCircuitEnabled {
		return false, nil
	}

	now := time.Now()

	adminCircuit.Lock()
	defer adminCircuit.Unlock()

	entry, ok := adminCircuit.entries[userID]
	if !ok {
		entry = &adminCircuitEntry{routes: make(map[string]struct{}, adminCircuitRouteThreshold)}
		adminCircuit.entries[userID] = entry
	}
	if entry.windowStart.IsZero() || now.Sub(entry.windowStart) > adminCircuitWindow {
		entry.routes = make(map[string]struct{}, adminCircuitRouteThreshold)
		entry.windowStart = now
	}
	entry.routes[route] = struct{}{}

	if len(entry.routes) < adminCircuitRouteThreshold || !entry.openedAt.IsZero() {
		return false, nil
	}
	entry.openedAt = now

	routes := make([]string, 0, len(entry.routes))
	for r := range entry.routes {
		routes = append(routes, r)
	}
	slices.Sort(routes)

	if len(adminCircuit.entries) > adminCircuitCapacity {
		for k, v := range adminCircuit.entries {
			expired := now.Sub(v.openedAt) > adminCircuitTTL
			if v.openedAt.IsZero() {
				expired = now.Sub(v.windowStart) > adminCircuitWindow
			}
			if expired {
				delete(adminCircuit.entries, k)
			}
		}
	}
	return true, routes
}
