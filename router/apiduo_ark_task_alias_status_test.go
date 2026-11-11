package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// 方舟原生路径没有显式路由，靠 NoRoute 兜进来。gin 在进入 NoRoute 处理器之前
// 就把状态码预置成 404，重入的处理器只写 body 不碰状态码，客户端于是收到
// 「正常的 JSON + 404」。这组用例钉住「重入必须把状态码清回 200」。
func TestApiduoArkReenterClearsNoRouteStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST(apiduoArkRelaySubmitPath, func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"id": "task-1"})
	})
	engine.NoRoute(ApiduoArkAliasDispatcher(engine, func(c *gin.Context) { c.Next() }))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, apiduoArkAliasSubmitPath,
		strings.NewReader(`{"model":"nobody-claims-this"}`))
	engine.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code, "重入后的响应状态码不能沿用 NoRoute 预设的 404")
	assert.Contains(t, rec.Body.String(), "task-1")
}

// 重入的目标路由自己设置错误码时，不能被这里覆盖回 200。
func TestApiduoArkReenterKeepsTargetStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST(apiduoArkRelaySubmitPath, func(c *gin.Context) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad"})
	})
	engine.NoRoute(ApiduoArkAliasDispatcher(engine, func(c *gin.Context) { c.Next() }))

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, apiduoArkAliasSubmitPath,
		strings.NewReader(`{"model":"nobody-claims-this"}`)))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
