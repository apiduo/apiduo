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
package controller

import (
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/apiduo/discount"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// GetUserDiscounts 返回指定用户的个人折扣规则（管理员）。
//
// 直接复用 users.setting 的 apiduo_discounts 字段，不单独建表：
// 折扣是低频配置，放进 setting 不需要 migration，也不会改变 UserBase 的
// 结构体形状（cacheMAC 因此不变，不会造成升级瞬间的缓存击穿）。
func GetUserDiscounts(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}

	user, err := model.GetUserById(id, false)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !canManageTargetRole(c.GetInt("role"), user.Role) {
		common.ApiErrorT(c, "No permission to access users of same or higher level")
		return
	}

	// usable_groups 按被编辑的用户算，而不是按当前登录的管理员算：
	// controller.GetUserGroups 取的是 c.GetInt("id")，用在这里会给出管理员的可用组。
	usableGroups := make([]string, 0, 8)
	for group := range service.GetUserUsableGroups(user.Group) {
		usableGroups = append(usableGroups, group)
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"rules":         discount.Parse(user.GetSetting().ApiduoDiscounts),
			"usable_groups": usableGroups,
		},
	})
}

type updateUserDiscountsRequest struct {
	Rules []discount.Rule `json:"rules"`
}

// UpdateUserDiscounts 覆盖式写入指定用户的个人折扣规则（管理员）。
//
// 先读现有 setting 再只改这一个字段：语言、通知、侧栏等配置不受影响。
// model.UpdateUserSetting 会顺带 PublishUserAuthCache，改完立刻生效，
// 不需要等用户缓存 TTL 过期。
func UpdateUserDiscounts(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}

	var req updateUserDiscountsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}

	rules, err := discount.Normalize(req.Rules)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	user, err := model.GetUserById(id, false)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !canManageTargetRole(c.GetInt("role"), user.Role) {
		common.ApiErrorT(c, "No permission to update users of same or higher level")
		return
	}

	settings := user.GetSetting()
	// 改前快照：审计要能回答「从几折改成几折」，只记路由和用户 ID 不够。
	beforeRules := discount.Parse(settings.ApiduoDiscounts)
	if len(rules) == 0 {
		// 空规则等于取消折扣，让 key 从 setting 里消失，避免留下 "[]"。
		settings.ApiduoDiscounts = nil
	} else {
		raw, marshalErr := common.Marshal(rules)
		if marshalErr != nil {
			common.ApiError(c, marshalErr)
			return
		}
		settings.ApiduoDiscounts = raw
	}

	if err := model.UpdateUserSetting(user.Id, settings); err != nil {
		common.ApiErrorT(c, "Update failed")
		return
	}

	// APIDUO:discount:BEGIN
	// 手动埋点并跳过中间件的 generic 兜底。
	// 折扣是资金敏感操作：「谁在什么时候动了哪个用户」还不够，
	// 必须能回答「把哪条规则从几折改成了几折」，否则事后无法定责。
	authMethod := "session"
	if c.GetBool("use_access_token") {
		authMethod = "access_token"
	}
	model.RecordOperationAuditLog(
		c.GetInt("id"), c.GetInt("role"),
		"PUT /api/user/:id/discounts",
		c.ClientIP(),
		"user.discount_update",
		map[string]any{
			"id":       user.Id,
			"username": user.Username,
			"before":   beforeRules,
			"after":    rules,
		},
		&model.AuditAdminInfo{
			AdminID:       c.GetInt("id"),
			AdminUsername: c.GetString("username"),
			AdminRole:     c.GetInt("role"),
			AuthMethod:    authMethod,
		},
		&model.AuditRequestInfo{
			Method:  c.Request.Method,
			Route:   c.FullPath(),
			Path:    c.FullPath(),
			Status:  http.StatusOK,
			Success: true,
			Params:  map[string]string{"id": strconv.Itoa(user.Id)},
		},
		c,
	)
	common.SetContextKey(c, constant.ContextKeyAuditLogged, true)
	// APIDUO:discount:END

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    rules,
	})
}
