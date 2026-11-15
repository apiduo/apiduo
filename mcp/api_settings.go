package mcp

import (
	"encoding/json"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

// apiGetSettings 返回当前生效的 MCP 参数。
//
// 回的是「合并后的有效值」而不是库里那行原文：运维需要知道某个键现在到底
// 是多少，包括他没写、走默认值的那些。
func apiGetSettings(c *gin.Context) {
	common.ApiSuccess(c, LoadSettings())
}

// apiUpdateSettings 局部更新。只改请求里出现的键，其余保持。
//
// 写成 patch 而不是整体替换：漏传一个字段就回默认值，配置会被无声改坏。
func apiUpdateSettings(c *gin.Context) {
	var patch map[string]json.RawMessage
	if err := c.ShouldBindJSON(&patch); err != nil {
		common.ApiErrorMsg(c, "请求体不是合法 JSON: "+err.Error())
		return
	}
	next, err := SaveSettings(patch)
	if err != nil {
		apiWriteErr(c, "保存 MCP 设置", err)
		return
	}
	// AdminAuth 的兜底审计会记录这次写操作；这里只回新值，方便调用方立刻确认。
	common.ApiSuccess(c, next)
}
