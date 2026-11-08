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
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// 权限不足的访问审计。
//
// 为什么需要单独埋点：审计兜底（beginAdminAudit）挂在 authHelper 的成功路径上，
// 角色校验不通过时直接 403 返回，一条记录都不会留。普通账号反复探测管理接口
// （比如尝试给别人改折扣）在审计里完全隐形，事后分不清是误点还是有人在试权限。
//
// 为什么必须节流：apiRouter 的全局限流很宽（默认 60 万/180 秒），不节流的话
// 一个账号循环打管理接口就能把审计表灌满，反而淹掉真正需要看的事件。
// 这里按「用户 × 方法 × 路由」每分钟只落一条，重复次数累加到 repeated，
// 命中窗口内直接返回，不写库也不占连接。
const deniedAccessAuditWindow = time.Minute

// deniedAccessAuditCapacity 触发过期清理的水位，防止长时间运行后 map 无界增长。
const deniedAccessAuditCapacity = 2048

type deniedAccessRecord struct {
	at    time.Time
	count int
}

var deniedAccessSeen = struct {
	sync.Mutex
	entries map[string]*deniedAccessRecord
}{entries: make(map[string]*deniedAccessRecord, 32)}

// recordDeniedAccessAudit 记录一次「已通过身份认证但权限不足」的访问。
//
// 只在 user 非空时记录：401（未登录、令牌无效）没有可信身份，记下来只会是
// 一堆无法归因的 IP，且极易被刷；那类事件由 AccessTokenAudit 覆盖。
func recordDeniedAccessAudit(c *gin.Context, user *model.UserBase, requiredRole int) {
	if user == nil {
		return
	}

	method := c.Request.Method
	route := c.FullPath()
	if route == "" {
		route = c.Request.URL.Path
	}
	key := strconv.Itoa(user.Id) + "|" + method + "|" + route

	now := time.Now()
	deniedAccessSeen.Lock()
	record, exists := deniedAccessSeen.entries[key]
	if exists && now.Sub(record.at) < deniedAccessAuditWindow {
		record.count++
		deniedAccessSeen.Unlock()
		return
	}
	repeated := 0
	if exists {
		// 上一个窗口内被吞掉的次数，一并写进本次记录，避免节流把事实抹平。
		repeated = record.count
	}
	deniedAccessSeen.entries[key] = &deniedAccessRecord{at: now}
	if len(deniedAccessSeen.entries) > deniedAccessAuditCapacity {
		for k, v := range deniedAccessSeen.entries {
			if now.Sub(v.at) > deniedAccessAuditWindow {
				delete(deniedAccessSeen.entries, k)
			}
		}
	}
	deniedAccessSeen.Unlock()

	params := model.AuditFields{
		"method":   method,
		"route":    route,
		"role":     user.Role,
		"required": requiredRole,
	}
	if repeated > 0 {
		params["repeated"] = repeated
	}

	model.RecordAuditLog(c, model.AuditLog{
		UserId:    user.Id,
		Username:  user.Username,
		ActorRole: user.Role,
		Category:  model.AuditCategorySecurity,
		Action:    "access.denied",
		Content:   "Denied access to " + method + " " + route + " (insufficient privileges)",
		Status:    http.StatusForbidden,
		Success:   false,
		Other: model.AuditOther{
			Op: &model.AuditOperation{Action: "access.denied", Params: params},
		},
	})
}
