package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 客户端的轮询循环靠这三个词判断「还要不要等」。把它们映射错，客户端会在
// 任务其实已经跑起来的时候一直认为它在排队，或者反过来把失败当成完成。
func TestApiduoImageAsyncStatus(t *testing.T) {
	cases := map[model.TaskStatus]string{
		model.TaskStatusNotStart:   "queued",
		model.TaskStatusSubmitted:  "queued",
		model.TaskStatusQueued:     "queued",
		model.TaskStatusUnknown:    "queued",
		model.TaskStatusInProgress: "in_progress",
		// 上游实测：列表行用 "succeeded"，不是 "completed"。
		model.TaskStatusSuccess: "succeeded",
		// 上游实测用 "failure"：客户端按这个拼词表，网关不能回 "failed"。
		model.TaskStatusFailure: "failure",
	}
	for status, want := range cases {
		if got := apiduoImageAsyncStatus(status); got != want {
			t.Fatalf("apiduoImageAsyncStatus(%q) = %q, want %q", status, got, want)
		}
	}
}

// 同号任务可能属于别的平台。开放这条入口之后，一次猜号就能读到别人的图，
// 所以归属判断必须只认高级异步渠道 —— 以及网关自己执行的任务。
func TestApiduoImageAsyncOwnsTask(t *testing.T) {
	if !apiduoImageAsyncOwnsTask(&model.Task{Platform: constant.TaskPlatform("8001")}) {
		t.Fatal("advanced-async platform must own the image entry")
	}
	if !apiduoImageAsyncOwnsTask(&model.Task{Platform: "local-image-async", ExecutionMode: model.TaskExecutionModeLocal}) {
		t.Fatal("a task the gateway executes itself belongs to this entry too")
	}
	for _, platform := range []constant.TaskPlatform{"suno", "mj", "doubao", ""} {
		if apiduoImageAsyncOwnsTask(&model.Task{Platform: platform}) {
			t.Fatalf("platform %q must not own the image entry", platform)
		}
	}
	if apiduoImageAsyncOwnsTask(nil) {
		t.Fatal("a missing task owns nothing")
	}
}

// 轮询两半的词表必须与上游一致：实测 hailuo 的异步图像端点回
// {"task_id":...,"status":"in_progress"} / {"status":"failure"}，受理是 202。
// 这条测试守的就是「网关不能回 id/failed/200」这三点。
func TestApiduoImageAsyncViewMatchesUpstreamVocabulary(t *testing.T) {
	running := &model.Task{TaskID: "task_abc", Status: model.TaskStatusInProgress, Progress: "30%", SubmitTime: 1700000000}
	code, view := apiduoImageAsyncView(running)
	assert.Equal(t, 202, code, "进行中必须 202，与上游一致")
	assert.Equal(t, "task_abc", view["task_id"])
	assert.Equal(t, "task_abc", view["id"], "id 保留兼容")
	assert.Equal(t, "in_progress", view["status"])
	assert.Equal(t, "30%", view["progress"])

	failed := &model.Task{TaskID: "task_abc", Status: model.TaskStatusFailure, FailReason: "boom", SubmitTime: 1700000000}
	code, view = apiduoImageAsyncView(failed)
	assert.Equal(t, 200, code, "终态 200")
	assert.Equal(t, "failure", view["status"], "上游用 failure，不是 failed")
	assert.Equal(t, "task_abc", view["task_id"])

	done := &model.Task{TaskID: "task_abc", Status: model.TaskStatusSuccess, PrivateData: model.TaskPrivateData{ResultURL: "https://cdn/x.png"}, SubmitTime: 1700000000}
	code, view = apiduoImageAsyncView(done)
	assert.Equal(t, 200, code)
	assert.Equal(t, "task_abc", view["task_id"])
	data, ok := view["data"].([]gin.H)
	require.True(t, ok, "成功必须回 data 数组")
	require.Len(t, data, 1)
	assert.Equal(t, "https://cdn/x.png", data[0]["url"])
}
