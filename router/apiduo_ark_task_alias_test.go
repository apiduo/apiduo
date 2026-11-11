package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 方舟形状的别名入口有三条容易踩空的分支，e2e 覆盖不到（要真插件 / 真任务库）：
//  1. 路径解析：`/tasks/{id}` 只收一层，尾斜杠要归一；
//  2. 归属判定：GET 按任务的 platform 决定谁来服务，不能猜模型名；
//  3. 重入后报文必须还在 —— 窥探请求体读走了 body，重路由过去就变成空 body。
func TestApiduoArkAliasTaskID(t *testing.T) {
	cases := []struct {
		path string
		id   string
		ok   bool
	}{
		{"/api/v3/contents/generations/tasks/task-1", "task-1", true},
		{"/api/v3/contents/generations/tasks/task-1/", "task-1", true},
		{"/api/v3/contents/generations/tasks/", "", false},
		{"/api/v3/contents/generations/tasks", "", false},
		{"/api/v3/contents/generations/tasks/a/b", "", false},
		{"/v1/contents/generations/tasks/task-1", "", false},
	}
	for _, tc := range cases {
		id, ok := apiduoArkAliasTaskID(tc.path)
		assert.Equal(t, tc.ok, ok, tc.path)
		assert.Equal(t, tc.id, id, tc.path)
	}
}

func TestApiduoArkFetchPathRewriteKeepsQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	var seenPath, seenURI string
	engine.GET("/api/v3/contents/generations/tasks/:task_id", apiduoArkFetchPathRewrite(), func(c *gin.Context) {
		seenPath = c.Request.URL.Path
		seenURI = c.Request.RequestURI
		c.Status(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v3/contents/generations/tasks/t-9?x=1", nil)
	engine.ServeHTTP(rec, req)

	assert.Equal(t, "/v1/video/generations/t-9", seenPath)
	assert.Equal(t, "/v1/video/generations/t-9?x=1", seenURI)
}

func TestApiduoArkDispatcherLeavesForeignPathsAlone(t *testing.T) {
	engine := gin.New()
	nextHit := ""
	engine.Use(ApiduoArkAliasDispatcher(engine, func(c *gin.Context) {
		nextHit = c.Request.Method + " " + c.Request.URL.Path
		c.Status(http.StatusOK)
	}))

	for _, target := range []string{"/v1/chat/completions", "/api/v3/contents/generations/tasks-x"} {
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		assert.Equal(t, "GET "+target, nextHit, target)
	}
}

// GET 归属：任务的 platform 是高级异步（8001）才转交中继；别的平台一律交回插件。
func TestApiduoArkDispatcherRoutesTaskByPlatform(t *testing.T) {
	setupRelayRouterTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Task{}))

	relayTasks := []string{}
	engine := gin.New()
	engine.Use(ApiduoArkAliasDispatcher(engine, func(c *gin.Context) {
		c.String(http.StatusOK, "plugin:"+c.Request.URL.Path)
	}))
	// 查询重入的是方舟形状的路由（不是 /v1/video/generations）：客户端打的是
	// 方舟路径，响应形状必须跟着路径走，所以别名把 :task_id 改写成这条。
	engine.GET(apiduoArkRelayRootPath+"/:task_id", func(c *gin.Context) {
		relayTasks = append(relayTasks, c.Param("task_id"))
		c.Status(http.StatusOK)
	})

	require.NoError(t, model.DB.Create(&model.Task{
		TaskID:   "relay-task",
		Platform: constant.TaskPlatform("8001"),
		Status:   model.TaskStatusInProgress,
	}).Error)
	require.NoError(t, model.DB.Create(&model.Task{
		TaskID:   "plugin-task",
		Platform: constant.TaskPlatform("suno"),
		Status:   model.TaskStatusInProgress,
	}).Error)

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, apiduoArkAliasSubmitPath+"/relay-task", nil))
	assert.Contains(t, relayTasks, "relay-task")

	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, apiduoArkAliasSubmitPath+"/plugin-task", nil))
	assert.Equal(t, "plugin:"+apiduoArkAliasSubmitPath+"/plugin-task", rec.Body.String())
	assert.NotContains(t, relayTasks, "plugin-task")

	// 不存在的任务交给插件，由它回自己的 404 —— 别名不替别人决定错误文案。
	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, apiduoArkAliasSubmitPath+"/missing", nil))
	assert.Equal(t, "plugin:"+apiduoArkAliasSubmitPath+"/missing", rec.Body.String())
}

// POST：没有任务插件声称这个模型时转交中继，并且**报文必须完好**。
func TestApiduoArkDispatcherReroutesSubmitKeepingBody(t *testing.T) {
	setupRelayRouterTestDB(t)

	var gotBody string
	engine := gin.New()
	engine.Use(ApiduoArkAliasDispatcher(engine, func(c *gin.Context) {
		c.String(http.StatusOK, "plugin")
	}))
	engine.POST(apiduoArkRelaySubmitPath, func(c *gin.Context) {
		raw, _ := c.GetRawData()
		gotBody = string(raw)
		c.Status(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	body := `{"model":"seedance-x","duration":5}`
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, apiduoArkAliasSubmitPath, strings.NewReader(body)))

	assert.Equal(t, body, gotBody, "重入后请求体被吞了，上游会收到空 body")
}

// 重入标记防止无限自转：带标记进来必须直接放行，且**不再改写路径**。
func TestApiduoArkDispatcherStopsOnReentry(t *testing.T) {
	engine := gin.New()
	var seen string
	engine.Use(func(c *gin.Context) {
		c.Set(apiduoArkAliasReentryKey, true)
		c.Next()
	})
	engine.Use(ApiduoArkAliasDispatcher(engine, func(c *gin.Context) {
		seen = c.Request.URL.Path
		c.Status(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, apiduoArkAliasSubmitPath+"/t-1", nil))
	assert.Equal(t, apiduoArkAliasSubmitPath+"/t-1", seen)
}
