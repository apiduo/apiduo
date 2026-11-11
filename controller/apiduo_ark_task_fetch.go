package controller

// APIDUO:ark-task-fetch BEGIN
//
// 方舟形状的下游查询。客户端照方舟文档读 {"id","status","content":
// {"video_url"}}，所以方舟入口不能回网关的 {code,data} 包装 —— 那个形状
// 方舟 SDK 读不到状态，只会一直轮询到超时。
//
// 响应以上游最近一次查询结果（task.Data，轮询时原样落库）为基础，
// usage / seed / resolution 这类字段因此原样保留；只覆盖三处：
//
//	id        换成本地任务号。上游 id 不能透出：客户端拿它回来查不到东西。
//	status    归一成方舟词汇，不看上游是否用了别的写法。
//	content   终态补齐 video_url，失败补 error。
//
// 图片异步（images 协议）走自己的 controller/apiduo_image_async.go，
// 两条入口按路径分开，互不影响。

import (
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// ApiduoArkTaskFetch answers GET <ark entry>/{task_id}.
func ApiduoArkTaskFetch(c *gin.Context) {
	taskID := c.Param("task_id")
	if taskID == "" {
		taskID = c.GetString("task_id")
	}
	task, exist, err := model.GetByTaskId(c.GetInt("id"), taskID)
	if err != nil {
		apiduoArkTaskError(c, http.StatusInternalServerError, err.Error())
		return
	}
	if !exist || !task.ResultRetrievable() {
		apiduoArkTaskError(c, http.StatusNotFound, "task not found")
		return
	}
	c.JSON(http.StatusOK, apiduoArkTaskPayload(task))
}

func apiduoArkTaskPayload(task *model.Task) map[string]any {
	payload := map[string]any{}
	if len(task.Data) > 0 {
		// 上游响应的顶层不是对象（数组、字符串）时保持空对象，
		// 只由下面几行补出最小可用形状。
		_ = common.Unmarshal(task.Data, &payload)
	}
	payload["id"] = task.TaskID
	// model 回显客户端请求的那个名字：重定向场景下上游任务对象里是映射后的名字
	// （hailuo-video），客户端看到自己没请求过的模型会以为拿错了任务。
	if name := strings.TrimSpace(task.Properties.OriginModelName); name != "" {
		payload["model"] = name
	}
	status := apiduoArkTaskStatusFor(task, payload)
	payload["status"] = status
	if _, ok := payload["created_at"]; !ok {
		payload["created_at"] = task.CreatedAt
	}
	if url := task.GetResultURL(); url != "" {
		content, _ := payload["content"].(map[string]any)
		if content == nil {
			content = map[string]any{}
		}
		if existing, _ := content["video_url"].(string); existing == "" {
			content["video_url"] = url
		}
		payload["content"] = content
	}
	// 与 doubao 插件一致：只有网关判定失败、上游还没给出终态时才补 error。
	// 上游已经回 cancelled / expired 时照原样透出，客户端从 status 就能停。
	if status == "failed" {
		if _, exists := payload["error"]; !exists {
			message := task.FailReason
			if message == "" {
				message = "task failed"
			}
			payload["error"] = map[string]any{"code": "InternalServiceError", "message": message}
		}
	}
	return payload
}

// apiduoArkTaskStatusFor 输出方舟词汇的任务状态。
//
// 上游最近一次查询结果里的 status 更权威：Ark 会回 cancelled / expired
// 这类本地状态枚举没有的终态，一律改写成 queued 会让客户端永远轮询。
// 只有本地已经判定的终态才优先：网关成功必须让客户端拿到结果，网关失败
// （轮询失败、任务超时）必须让客户端停止轮询 —— 这两种情况下上游数据
// 可能还停在 queued / running。
func apiduoArkTaskStatusFor(task *model.Task, payload map[string]any) string {
	upstream, _ := payload["status"].(string)
	upstream = strings.TrimSpace(upstream)
	switch task.Status {
	case model.TaskStatusSuccess:
		return "succeeded"
	case model.TaskStatusFailure:
		switch upstream {
		case "", "queued", "running":
			return "failed"
		}
		return upstream
	}
	if upstream != "" {
		return upstream
	}
	return apiduoArkTaskStatus(task.Status)
}

// 方舟的状态词汇：queued / running / succeeded / failed / cancelled。
// 没识别出来的一律按 queued 回，客户端会继续轮询而不是误判失败。
func apiduoArkTaskStatus(status model.TaskStatus) string {
	switch status {
	case model.TaskStatusSuccess:
		return "succeeded"
	case model.TaskStatusFailure:
		return "failed"
	case model.TaskStatusInProgress:
		return "running"
	default:
		return "queued"
	}
}

// apiduoArkTaskError answers in Ark's error envelope. The codes follow
// docs.volcengine.com/docs/ark/error-codes, the same table the doubao plugin
// uses; an unknown status falls back to InternalServiceError.
func apiduoArkTaskError(c *gin.Context, status int, message string) {
	c.JSON(status, map[string]any{
		"error": map[string]any{
			"code":    apiduoArkErrorCode(status),
			"message": message,
		},
	})
}

func apiduoArkErrorCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "InvalidParameter"
	case http.StatusUnauthorized:
		return "AuthenticationError"
	case http.StatusForbidden:
		return "AccessDenied"
	case http.StatusNotFound:
		return "NotFound"
	case http.StatusRequestTimeout:
		return "RequestTimeout"
	case http.StatusTooManyRequests:
		return "AccountRateLimitExceeded"
	default:
		return "InternalServiceError"
	}
}

// APIDUO:ark-task-fetch END
