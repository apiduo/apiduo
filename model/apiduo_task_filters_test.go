package model

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newApiduoTaskFilterFixture 清空 tasks 表并写入固定的样本。
// 这些样本刻意覆盖：只有 origin、origin 与 upstream 不同、properties 为空、
// 以及名字里带 LIKE 元字符的模型名。
func newApiduoTaskFilterFixture(t *testing.T) {
	t.Helper()
	require.NoError(t, DB.Where("1 = 1").Delete(&Task{}).Error)

	tasks := []*Task{
		{
			TaskID: "origin-only", Platform: constant.TaskPlatform("kling"), UserId: 1, ChannelId: 11,
			Action: "text_to_video", Status: TaskStatusSuccess, SubmitTime: 100,
			Properties: Properties{OriginModelName: "MiniMax-Hailuo-2.3"},
		},
		{
			TaskID: "mapped", Platform: constant.TaskPlatform("kling"), UserId: 1, ChannelId: 11,
			Action: "text_to_video", Status: TaskStatusFailure, SubmitTime: 200,
			Properties: Properties{OriginModelName: "flux-3-image", UpstreamModelName: "bfl/flux-3"},
		},
		{
			TaskID: "empty-props", Platform: constant.TaskPlatform("suno"), UserId: 2, ChannelId: 12,
			Action: "music", Status: TaskStatusInProgress, SubmitTime: 300,
			Properties: Properties{},
		},
		{
			TaskID: "literal-percent", Platform: constant.TaskPlatform("suno"), UserId: 2, ChannelId: 12,
			Action: "music", Status: TaskStatusQueued, SubmitTime: 400,
			Properties: Properties{OriginModelName: "wan_2.5%preview"},
		},
	}
	for _, task := range tasks {
		require.NoError(t, DB.Create(task).Error)
	}
}

func listApiduoTaskIDs(t *testing.T, params SyncTaskQueryParams) []string {
	t.Helper()
	found := TaskGetAllTasks(0, 50, params)
	ids := make([]string, 0, len(found))
	for _, task := range found {
		ids = append(ids, task.TaskID)
	}
	require.Equal(t, int(TaskCountAllTasks(params)), len(ids), "count must agree with the list")
	return ids
}

func TestApiduoTaskModelFilter(t *testing.T) {
	newApiduoTaskFilterFixture(t)

	tests := []struct {
		name  string
		model string
		want  []string
	}{
		{name: "origin only", model: "MiniMax-Hailuo-2.3", want: []string{"origin-only"}},
		{name: "origin substring and case insensitive", model: "hailuo", want: []string{"origin-only"}},
		{name: "upstream name", model: "bfl/flux", want: []string{"mapped"}},
		{name: "origin wins over upstream for the same row", model: "flux-3-image", want: []string{"mapped"}},
		{name: "wildcard is a literal", model: "%", want: []string{"literal-percent"}},
		{name: "underscore is a literal", model: "wan_2.5", want: []string{"literal-percent"}},
		{name: "underscore does not match other characters", model: "wan.2.5", want: []string{}},
		{name: "no match", model: "gpt-4o", want: []string{}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, listApiduoTaskIDs(t, SyncTaskQueryParams{Model: test.model}))
		})
	}
}

func TestApiduoTaskModelFilterScopedToUser(t *testing.T) {
	newApiduoTaskFilterFixture(t)

	items := TaskGetAllUserTask(2, 0, 50, SyncTaskQueryParams{Model: "hailuo"})
	assert.Empty(t, items, "another user's task must not leak through the model filter")
	assert.Equal(t, int64(0), TaskCountAllUserTask(2, SyncTaskQueryParams{Model: "hailuo"}))

	items = TaskGetAllUserTask(1, 0, 50, SyncTaskQueryParams{Model: "hailuo"})
	require.Len(t, items, 1)
	assert.Equal(t, "origin-only", items[0].TaskID)
}

func TestApiduoTaskStatusFilter(t *testing.T) {
	newApiduoTaskFilterFixture(t)

	// 前端把「排队中」展开成三个库内状态，等值 Status 被清空后走集合。
	queued := ParseApiduoTaskStatuses("NOT_START,QUEUED,SUBMITTED")
	require.Equal(t, []string{"NOT_START", "QUEUED", "SUBMITTED"}, queued)
	assert.Equal(t,
		[]string{"literal-percent"},
		listApiduoTaskIDs(t, SyncTaskQueryParams{Statuses: queued}),
	)

	assert.Equal(t,
		[]string{"mapped"},
		listApiduoTaskIDs(t, SyncTaskQueryParams{Statuses: ParseApiduoTaskStatuses("FAILURE")}),
	)
}

func TestApiduoTaskStatusFilterAndModelCombine(t *testing.T) {
	newApiduoTaskFilterFixture(t)

	combined := SyncTaskQueryParams{
		Model:    "flux",
		Statuses: ParseApiduoTaskStatuses("SUCCESS"),
	}
	assert.Empty(t, listApiduoTaskIDs(t, combined), "model and status must be ANDed")
}

func TestParseApiduoTaskStatusesDropsNoise(t *testing.T) {
	assert.Empty(t, ParseApiduoTaskStatuses(""))
	assert.Empty(t, ParseApiduoTaskStatuses(" , ,"))
	assert.Empty(t, ParseApiduoTaskStatuses("DROP TABLE tasks"), "unknown statuses are dropped, not passed through")
	assert.Equal(t,
		[]string{"SUCCESS", "FAILURE"},
		ParseApiduoTaskStatuses(" success , FAILURE ,SUCCESS, "),
		"whitespace, case and duplicates are normalized",
	)
}

func TestApiduoTaskModelJSONExprPerDialect(t *testing.T) {
	origin, upstream, valid := apiduoTaskModelJSONExpr("mysql")
	assert.Contains(t, origin, "JSON_UNQUOTE(JSON_EXTRACT(properties, '$.origin_model_name'))")
	assert.Contains(t, upstream, "JSON_UNQUOTE(JSON_EXTRACT(properties, '$.upstream_model_name'))")
	assert.Contains(t, valid, "JSON_VALID(properties)", "one malformed row must not break the whole page")

	origin, upstream, valid = apiduoTaskModelJSONExpr("postgres")
	assert.Equal(t, "properties->>'origin_model_name'", origin)
	assert.Equal(t, "properties->>'upstream_model_name'", upstream)
	assert.Equal(t, "properties IS NOT NULL", valid)

	origin, upstream, valid = apiduoTaskModelJSONExpr("sqlite")
	assert.Contains(t, origin, "json_extract(properties, '$.origin_model_name')")
	assert.Contains(t, upstream, "json_extract(properties, '$.upstream_model_name')")
	assert.Contains(t, valid, "json_valid(properties)")

	origin, upstream, valid = apiduoTaskModelJSONExpr("sqlserver")
	assert.Empty(t, origin)
	assert.Empty(t, upstream)
	assert.Empty(t, valid)
}

func TestApiduoTaskModelLikeLiteral(t *testing.T) {
	assert.Equal(t, "abc", apiduoTaskModelLikeLiteral("abc"))
	assert.Equal(t, "!%", apiduoTaskModelLikeLiteral("%"))
	assert.Equal(t, "!_", apiduoTaskModelLikeLiteral("_"))
	assert.Equal(t, "!!", apiduoTaskModelLikeLiteral("!"), "the escape char itself must be escaped first")
}
