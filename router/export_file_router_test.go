package router

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// 导出下载必须挂在自有的注册表里，不走 api-router.go 的手工块。
func TestExportFileRouteRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)

	registered := false
	for _, module := range apiduoModules {
		if module.name == "export-file" {
			registered = true
		}
	}
	assert.True(t, registered, "export-file must be listed in apiduoModules")

	engine := gin.New()
	registerApiduoExportFile(engine.Group(""))

	found := false
	for _, route := range engine.Routes() {
		if route.Method == http.MethodGet && route.Path == "/export/files/*key" {
			found = true
		}
	}
	assert.True(t, found, "GET /export/files/*key should be registered")
}
