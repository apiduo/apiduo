package controller

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// ApiduoImageAsyncTaskFetch answers GET /v1/images/generations/:task_id for a
// task submitted through the OpenAI image async shape.
//
// 一个任务只有一个终态，两种视图：
//
//	SUCCESS -> 标准 Images 响应 {task_id, created, data:[{url}]}，客户端直接把 data 当图片用；
//	其它    -> 任务状态视图 {task_id, status, progress}，客户端继续轮询。
//
// task_id 是权威字段：上游受理时回的就是这个名字，客户端从提交到轮询读同一个
// 键，不需要知道网关内部把它存成了什么。id 与它同值，只为已经按 id 解析的
// 客户端保留，新接入的不要依赖它。
//
// 失败也走状态视图而不是 HTTP 错误：轮询中的失败是任务的终态，不是这次查询
// 的错误，用 4xx/5xx 会让客户端的重试逻辑把「已经失败的任务」当成「查询失败」。
func ApiduoImageAsyncTaskFetch(c *gin.Context) {
	taskID := strings.TrimSpace(c.Param("task_id"))
	if taskID == "" {
		c.JSON(http.StatusBadRequest, apiduoImageAsyncErrorView("invalid_request", "task id is required"))
		return
	}
	userID := c.GetInt("id")
	task, exists, err := model.GetByTaskId(userID, taskID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, apiduoImageAsyncErrorView("get_task_failed", err.Error()))
		return
	}
	// 只有高级异步渠道服务这个入口。别的平台的同号任务对客户端不可见，
	// 否则一次猜号就能读到别人的图像任务。
	if !exists || !task.ResultRetrievable() || !apiduoImageAsyncOwnsTask(task) {
		c.JSON(http.StatusNotFound, apiduoImageAsyncErrorView("task_not_found", "task not found"))
		return
	}

	code, view := apiduoImageAsyncView(task)
	c.JSON(code, view)
}

// apiduoImageAsyncView builds the body and the status code the Images async
// shape answers with.
//
// A running task is 202, a terminal one is 200: vendors implementing this shape
// answer their own poll half that way, and a client gating on 202 would read a
// finished request as still-pending. The failure word is "failure" and the id
// field is "task_id" for the same reason — those are the spellings the upstreams
// use, and a client should not have to accept two vocabularies.
func apiduoImageAsyncView(task *model.Task) (int, gin.H) {
	created := task.CreatedAt
	if created == 0 {
		created = task.SubmitTime
	}
	switch task.Status {
	case model.TaskStatusSuccess:
		url := strings.TrimSpace(task.GetResultURL())
		if url == "" {
			return http.StatusOK, gin.H{
				"task_id": task.TaskID,
				"id":      task.TaskID,
				"created": created,
				"status":  "failure",
				"error": gin.H{
					"message": "upstream reported success without a result url",
					"code":    "empty_result",
				},
			}
		}
		return http.StatusOK, gin.H{
			"task_id": task.TaskID,
			"id":      task.TaskID,
			"created": created,
			"data":    []gin.H{{"url": url}},
		}
	case model.TaskStatusFailure:
		reason := strings.TrimSpace(task.FailReason)
		if reason == "" {
			reason = "the task failed"
		}
		return http.StatusOK, gin.H{
			"task_id": task.TaskID,
			"id":      task.TaskID,
			"object":  "image.generation",
			"created": created,
			"status":  "failure",
			"error": gin.H{
				"message": reason,
				"code":    "task_failed",
			},
		}
	default:
		view := gin.H{
			"task_id": task.TaskID,
			"id":      task.TaskID,
			"object":  "image.generation",
			"created": created,
			"status":  apiduoImageAsyncStatus(task.Status),
		}
		if progress := strings.TrimSpace(task.Progress); progress != "" {
			view["progress"] = progress
		}
		return http.StatusAccepted, view
	}
}

// apiduoImageAsyncOwnsTask reports whether this task belongs to the Images
// asynchronous entry.
//
// Two kinds do. A relayed task names its channel type as the platform, and only
// an advanced-async channel speaks this protocol. A locally executed task has no
// vendor platform at all — the gateway is the platform — and its only reason to
// exist is the promise this entry made, so excluding it would accept a
// submission and then answer every poll with 404.
func apiduoImageAsyncOwnsTask(task *model.Task) bool {
	if task == nil {
		return false
	}
	if task.ExecutionMode == model.TaskExecutionModeLocal {
		return true
	}
	return string(task.Platform) == strconv.Itoa(constant.ChannelTypeAdvancedAsync)
}

// apiduoImageAsyncStatus maps a gateway task status onto the words an image
// client polls for. Not started and queued are both "queued" from the client's
// point of view: neither has begun producing pixels.
//
// The words come from the upstreams that implement this shape, measured rather
// than guessed: a running task answers "in_progress", a failed one "failure",
// and a list row reports "succeeded". A successful *poll* answers no status at
// all — it answers {data, created}, and the presence of data is the terminal
// evidence — so the success branch here is only reachable if a caller maps a
// status it already knows is terminal.
func apiduoImageAsyncStatus(status model.TaskStatus) string {
	switch status {
	case model.TaskStatusInProgress:
		return "in_progress"
	case model.TaskStatusSuccess:
		return "succeeded"
	case model.TaskStatusFailure:
		return "failure"
	default:
		return "queued"
	}
}

func apiduoImageAsyncErrorView(code, message string) gin.H {
	return gin.H{
		"error": gin.H{
			"message": message,
			"type":    "new_api_error",
			"code":    code,
		},
	}
}
