package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/*
任务日志筛选的契约测试。跑在发版流水线的冒烟之前（见 .github/workflows/brand-release.yml），
也可以在本地用 TEST_TASK_DB_DIALECT 切到 MySQL / PostgreSQL 跑同一份样本。

为什么从 handler 进、断言 HTTP 响应，而不是直接调 model 层：
`GET /api/task` 的筛选参数是纯 query 名约定（model / status），前后端之间没有任何
编译期信号。把 `model` 改名成 `model_name`、或把 status 从逗号串改回单值，
model 层的单测照旧全绿，只有这条链路会红。

不测权限：直接调用 handler 绕过了中间件，这里回答的是「筛选还认不认」，
「谁能调」由路由与中间件那套用例负责。
*/

type taskFilterRow struct {
	taskID string
	model  string
	status model.TaskStatus
}

// newTaskFilterContractFixture 造一组能互相区分的样本：任意两条在
// 「模型 / 状态 / 有没有 properties」上都不同，断言写错就会撞车。
func newTaskFilterContractFixture(t *testing.T) {
	t.Helper()
	originalDB := model.DB
	previousRedis := common.RedisEnabled
	common.RedisEnabled = false
	database, _ := openTaskDialectDatabase(t, &model.Task{}, &model.Channel{}, &model.User{})
	model.DB = database
	t.Cleanup(func() {
		model.DB = originalDB
		common.RedisEnabled = previousRedis
	})

	require.NoError(t, database.Create(&model.User{
		Id: 7, Username: "task-filter-owner", Status: common.UserStatusEnabled,
		Role: common.RoleCommonUser, Group: "default",
	}).Error)
	baseURL := "https://example.com"
	require.NoError(t, database.Create(&model.Channel{
		Id: 1, Name: "filter", Key: "key", BaseURL: &baseURL, Status: common.ChannelStatusEnabled,
	}).Error)

	rows := []taskFilterRow{
		{taskID: "filter-hailuo-success", model: "hailuo-video", status: model.TaskStatusSuccess},
		{taskID: "filter-hailuo-failure", model: "hailuo-2.3", status: model.TaskStatusFailure},
		{taskID: "filter-nano-success", model: "nano-banana-2", status: model.TaskStatusSuccess},
		// 模型名里带 LIKE 元字符：用它区分「字面量匹配」和「野生通配符」。
		{taskID: "filter-literal-percent", model: "wan_2.5%preview", status: model.TaskStatusQueued},
	}
	for i, row := range rows {
		require.NoError(t, database.Create(&model.Task{
			TaskID: row.taskID, Platform: constant.TaskPlatform("kling"), UserId: 7, ChannelId: 1,
			Action: "text_to_video", Status: row.status, SubmitTime: int64(100 + i),
			Progress:   "100%",
			Properties: model.Properties{OriginModelName: row.model},
		}).Error)
	}
	// 没有 properties 的历史行：任何模型筛选都不该把它捞进来，也不该让查询报错。
	require.NoError(t, database.Create(&model.Task{
		TaskID: "filter-no-properties", Platform: constant.TaskPlatform("suno"), UserId: 7, ChannelId: 1,
		Action: "music", Status: model.TaskStatusInProgress, SubmitTime: 200, Progress: "10%",
	}).Error)
}

// callTaskList 走一遍真实 handler，返回 (HTTP 状态, 总数, task_id 集合)。
func callTaskList(t *testing.T, query string) (int, int64, []string) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/task?p=1&page_size=100"+query, nil)
	c.Set("role", common.RoleAdminUser)
	c.Set("id", 99)
	GetAllTask(c)

	var body struct {
		Success bool `json:"success"`
		Data    struct {
			Total int64 `json:"total"`
			Items []struct {
				TaskID string `json:"task_id"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), "body: %s", rec.Body.String())
	require.True(t, body.Success, "handler failed: %s", rec.Body.String())

	ids := make([]string, 0, len(body.Data.Items))
	for _, item := range body.Data.Items {
		ids = append(ids, item.TaskID)
	}
	require.Equal(t, int(body.Data.Total), len(ids), "total must agree with the page content")
	return rec.Code, body.Data.Total, ids
}

func TestTaskLogFilterContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newTaskFilterContractFixture(t)

	cases := []struct {
		name  string
		query string
		want  int64
	}{
		{name: "no filter returns every task", query: "", want: 5},
		{name: "model substring", query: "&model=hailuo", want: 2},
		{name: "model is case insensitive", query: "&model=HAILUO", want: 2},
		{name: "model unknown is empty", query: "&model=gpt-4o", want: 0},
		// 野生通配符会命中全部 5 条；字面量只命中名字里真有 % 的那条。
		{name: "percent is literal, not a wildcard", query: "&model=%25", want: 1},
		{name: "escape char is literal", query: "&model=!", want: 0},
		{name: "status equality", query: "&status=FAILURE", want: 1},
		{name: "status union", query: "&status=SUCCESS,FAILURE", want: 3},
		// 「排队中」在前端是一个选项，后端是三个库内状态。
		{name: "queued group", query: "&status=SUBMITTED,QUEUED,NOT_START", want: 1},
		{name: "unknown status matches nothing", query: "&status=GARBAGE", want: 0},
		{name: "model and status are ANDed", query: "&model=hailuo&status=FAILURE", want: 1},
		{name: "model never widens the set", query: "&model=hailuo&status=SUCCESS,FAILURE", want: 2},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			status, total, ids := callTaskList(t, test.query)
			assert.Equal(t, http.StatusOK, status)
			assert.Equal(t, test.want, total, "query %q → %v", test.query, ids)
		})
	}
}

func TestTaskLogFilterPaginationStaysConsistent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newTaskFilterContractFixture(t)

	// 筛选不该改写总数：第 2 页、每页 1 条，总数仍是 5。
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/task?p=2&page_size=1", nil)
	c.Set("role", common.RoleAdminUser)
	GetAllTask(c)

	var body struct {
		Data struct {
			Total int64 `json:"total"`
			Items []any `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), "body: %s", rec.Body.String())
	assert.Equal(t, int64(5), body.Data.Total)
	assert.Len(t, body.Data.Items, 1, fmt.Sprintf("page 2 of page_size 1 must carry one row"))
}
