package middleware

import (
	"context"
	"strings"

	"github.com/QuantumNous/new-api/constant"

	"github.com/gin-gonic/gin"
)

// Playground 的 /pg 前缀只是另一种**凭据**：用浏览器会话替代 sk 令牌。
// 请求本身与 /v1 完全同形 —— /pg/chat/completions 就是一次标准 chat
// completion，/pg/images/edits 就是一次标准图片编辑。
//
// 这个中间件在会话鉴权之后把路径就地归一化成 /v1/...，于是下游每一个判定点
// （中继模式、渠道路径过滤、高级自定义路由表、日志路径）看到的都是同一条
// /v1 路径，不必各写一份 /pg 兼容分支。此前这些分支散在
// relay_mode / distributor / channel_constraint / channel_settings 四处，
// 每加一个 /pg 入口就要重新踩一遍「某处漏了」——漏掉时不会报「路径不支持」，
// 只会在渠道选择阶段报「无可用渠道」，极难定位。
//
// 🔴 必须挂在 UserAuth **之后**：PAT 白名单表（access_token_routes.go）登记的
// key 是 `METHOD + c.FullPath()`，而 c.FullPath() 是路由模板，不受 URL.Path
// 改写影响；但 UserAuth 仍以原始路径参与判定，提前改写会让诊断信息错位。
func PlaygroundPathAlias() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c == nil || c.Request == nil {
			c.Next()
			return
		}
		path := c.Request.URL.Path
		if !strings.HasPrefix(path, "/pg/") {
			c.Next()
			return
		}

		// 客户端真实路径留给日志与审计回显：改写之后 URL.Path 已经是内部形状，
		// 而用户看到自己调的是 /pg/...。
		//
		// 存 Request.Context 而不是 gin 的 Keys：别名入口会经
		// engine.HandleContext 重入，reset() 会清掉 c.Keys。
		ctx := context.WithValue(c.Request.Context(), constant.ApiduoClientPathKey{}, path)
		c.Request = c.Request.WithContext(ctx)

		c.Request.URL.Path = "/v1/" + strings.TrimPrefix(path, "/pg/")
		// RawPath 是编码前的原始形状，留着会让 url.String() 与 Path 打架。
		c.Request.URL.RawPath = ""
		c.Request.RequestURI = c.Request.URL.RequestURI()
		c.Next()
	}
}

// playgroundClientPath 返回浏览器实际调用的路径；非 playground 请求为空。
func playgroundClientPath(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	path, _ := c.Request.Context().Value(constant.ApiduoClientPathKey{}).(string)
	return path
}

// isPlaygroundChatRequest 判断这条请求是否来自 playground 的 chat 入口。
//
// 路径已被 PlaygroundPathAlias 改写成 /v1/chat/completions，光看 URL.Path
// 无法与对外 API 的同名入口区分，只能读客户端原始路径。
func isPlaygroundChatRequest(c *gin.Context) bool {
	return strings.HasPrefix(playgroundClientPath(c), "/pg/chat/completions")
}
