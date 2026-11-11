package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 方舟 / 图片异步的别名入口在分发前把 URL.Path 改写成内部中继路径
// （/v1/video/generations）。日志必须回显客户端真正打的地址，否则控制台会
// 显示一个客户端从未用过的路径。
func TestAppendRequestPathPrefersClientPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, constant.ApiduoTaskRelayPath, nil)
	c.Set(constant.ApiduoClientPathContextKey, "/api/v3/contents/generations/tasks")

	other := model.NewLogOther()
	appendRequestPath(c, nil, other)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(other.JSONString()), &decoded))
	assert.Equal(t, "/api/v3/contents/generations/tasks", decoded["request_path"])
}

// 没有别名改写时保持原行为：直接读 Request.URL.Path。
func TestAppendRequestPathFallsBackToURLPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	other := model.NewLogOther()
	appendRequestPath(c, nil, other)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(other.JSONString()), &decoded))
	assert.Equal(t, "/v1/chat/completions", decoded["request_path"])
}

// 任务执行快照（任务详情的「请求转换」）同样要留客户端路径。
func TestTaskExecutionSnapshotPrefersClientPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, constant.ApiduoTaskRelayPath, nil)
	c.Set(constant.ApiduoClientPathContextKey, "/api/v3/contents/generations/tasks")

	snapshot := TaskExecutionSnapshotFromContext(c)
	require.NotNil(t, snapshot)
	assert.Equal(t, "/api/v3/contents/generations/tasks", snapshot.RequestPath)
}
