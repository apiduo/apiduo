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
//	SUCCESS -> 标准 Images 响应 {created, data:[{url}]}，客户端直接把 data 当图片用；
//	其它    -> 任务状态视图 {id, status, progress}，客户端继续轮询。
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
	if !exists || !task.ResultRetrievable() || !apiduoImageAsyncOwnsTask(task.Platform) {
		c.JSON(http.StatusNotFound, apiduoImageAsyncErrorView("task_not_found", "task not found"))
		return
	}

	created := task.CreatedAt
	if created == 0 {
		created = task.SubmitTime
	}
	switch task.Status {
	case model.TaskStatusSuccess:
		url := strings.TrimSpace(task.GetResultURL())
		if url == "" {
			c.JSON(http.StatusOK, gin.H{
				"id":      task.TaskID,
				"created": created,
				"status":  "failed",
				"error": gin.H{
					"message": "upstream reported success without a result url",
					"code":    "empty_result",
				},
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"created": created,
			"data":    []gin.H{{"url": url}},
		})
	case model.TaskStatusFailure:
		reason := strings.TrimSpace(task.FailReason)
		if reason == "" {
			reason = "the task failed"
		}
		c.JSON(http.StatusOK, gin.H{
			"id":      task.TaskID,
			"object":  "image.generation",
			"created": created,
			"status":  "failed",
			"error": gin.H{
				"message": reason,
				"code":    "task_failed",
			},
		})
	default:
		view := gin.H{
			"id":      task.TaskID,
			"object":  "image.generation",
			"created": created,
			"status":  apiduoImageAsyncStatus(task.Status),
		}
		if progress := strings.TrimSpace(task.Progress); progress != "" {
			view["progress"] = progress
		}
		c.JSON(http.StatusOK, view)
	}
}

// apiduoImageAsyncOwnsTask reports whether the task platform is the
// advanced-async channel type, which is the only producer of this entry shape.
func apiduoImageAsyncOwnsTask(platform constant.TaskPlatform) bool {
	return string(platform) == strconv.Itoa(constant.ChannelTypeAdvancedAsync)
}

// apiduoImageAsyncStatus maps a gateway task status onto the words an image
// client polls for. Not started and queued are both "queued" from the client's
// point of view: neither has begun producing pixels.
func apiduoImageAsyncStatus(status model.TaskStatus) string {
	switch status {
	case model.TaskStatusInProgress:
		return "in_progress"
	case model.TaskStatusSuccess:
		return "completed"
	case model.TaskStatusFailure:
		return "failed"
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
