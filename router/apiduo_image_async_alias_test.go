package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// 入站端点由渠道自己声明，所以最容易出的事故是「抢错流量」：把本该走同步
// 图像中继、插件分发或干脆 404 的请求接走，客户端拿到一个任务号而不是图片。
func TestApiduoAsyncIncomingDispatcherLeavesOtherPathsAlone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	nextHit := ""
	engine.Use(ApiduoAsyncIncomingDispatcher(engine, func(c *gin.Context) {
		nextHit = c.Request.Method + " " + c.Request.URL.Path
		c.Status(http.StatusOK)
	}))

	targets := []struct{ method, path string }{
		// 同步图像入口一个字节都不能碰
		{http.MethodPost, constant.ApiduoImageAsyncFetchPath},
		{http.MethodPost, "/v1/images/edits"},
		{http.MethodPost, "/v1/chat/completions"},
		// 没有任务号时不是轮询
		{http.MethodGet, constant.ApiduoImageAsyncIncomingPath},
		// 没有人认领的轮询路径
		{http.MethodGet, "/v1/images/tasks/task_1"},
	}
	for _, target := range targets {
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(target.method, target.path, strings.NewReader(`{"model":"m"}`)))
		assert.Equal(t, target.method+" "+target.path, nextHit, target.path)
	}
}

// 没有渠道接管时必须原样放行：此时请求仍然该由同步图像中继或插件处理。
func TestApiduoAsyncIncomingDispatcherPassesWhenNoChannelClaims(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	nextHit := false
	engine.Use(ApiduoAsyncIncomingDispatcher(engine, func(c *gin.Context) {
		nextHit = true
		c.Status(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, constant.ApiduoImageAsyncIncomingPath,
		strings.NewReader(`{"model":"nobody-claims-this","prompt":"x"}`)))

	assert.True(t, nextHit, "没有任何渠道声明时不应该改道")
	assert.Empty(t, rec.Header().Get("Location"))
}

// 重入标记防止无限自转：带标记进来必须直接放行。
func TestApiduoAsyncIncomingDispatcherStopsOnReentry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	var seen string
	engine.Use(func(c *gin.Context) {
		c.Set(apiduoArkAliasReentryKey, true)
		c.Next()
	})
	engine.Use(ApiduoAsyncIncomingDispatcher(engine, func(c *gin.Context) {
		seen = c.Request.URL.Path
		c.Status(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, constant.ApiduoImageAsyncIncomingPath,
		strings.NewReader(`{"model":"m"}`)))
	assert.Equal(t, constant.ApiduoImageAsyncIncomingPath, seen)
}

// 读不出来的报文一律放行：改道会把「参数错误」变成「未知模型」，
// 客户端看到的错误就不再指向真正的问题。
func TestApiduoAsyncIncomingDispatcherPassesUnreadableBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	nextHit := false
	engine.Use(ApiduoAsyncIncomingDispatcher(engine, func(c *gin.Context) {
		nextHit = true
		c.Status(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, constant.ApiduoImageAsyncIncomingPath,
		strings.NewReader(`not-json`)))
	assert.True(t, nextHit)
}

// 入站路径可以有任意多级，任务号本身不含斜杠：从最后一个斜杠切。
func TestApiduoAsyncPollPath(t *testing.T) {
	cases := []struct {
		path    string
		base    string
		taskID  string
		want    bool
		comment string
	}{
		{"/v1/images/generations/async/task_1", "/v1/images/generations/async", "task_1", true, "默认图像入口"},
		{"/v1/images/tasks/task_2", "/v1/images/tasks", "task_2", true, "自定义入口"},
		{"/v1/images/generations", "/v1/images", "generations", true, "切出来没人认领，由调用方再判"},
		{"/v1/images/generations/async/", "", "", false, "结尾斜杠不是任务号"},
		{"/", "", "", false, "根路径"},
		{"", "", "", false, "空路径"},
	}
	for _, testCase := range cases {
		base, taskID, ok := apiduoAsyncPollPath(testCase.path)
		assert.Equal(t, testCase.want, ok, testCase.comment)
		assert.Equal(t, testCase.base, base, testCase.comment)
		assert.Equal(t, testCase.taskID, taskID, testCase.comment)
	}
}

// 动作靠请求头跨过 HandleContext 的 context 重置。桥必须在进入 relay 之前
// 把它塞回 context，并且**删掉头**：这一层是网关内部约定，不该跟着请求
// 一起发到上游。
func TestApiduoTaskActionHeaderBridge(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/bridge", ApiduoTaskActionHeaderBridge(), func(c *gin.Context) {
		c.String(http.StatusOK, c.GetString("task_action")+"|"+c.Request.Header.Get(apiduoTaskActionHeader))
	})

	req := httptest.NewRequest(http.MethodPost, "/bridge", nil)
	req.Header.Set(apiduoTaskActionHeader, constant.TaskActionImageGeneration)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	assert.Equal(t, constant.TaskActionImageGeneration+"|", rec.Body.String(),
		"动作要进 context，头要从请求上摘掉")
}
