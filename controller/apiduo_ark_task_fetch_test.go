package controller

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/model"
)

// 方舟客户端只看这几个字段：id / status / content.video_url。形状写错不会
// 报错，只会一直轮询到超时 —— 所以逐项钉死。
func TestApiduoArkTaskPayloadKeepsUpstreamDetail(t *testing.T) {
	task := &model.Task{
		TaskID:    "task_local",
		Status:    model.TaskStatusSuccess,
		CreatedAt: 1700000000,
		Data: json.RawMessage(`{
			"id": "cgt-upstream",
			"model": "seedance-1-5-pro-250528",
			"status": "succeeded",
			"content": {"video_url": "https://upstream/v.mp4"},
			"usage": {"completion_tokens": 12, "total_tokens": 30}
		}`),
	}

	payload := apiduoArkTaskPayload(task)

	if payload["id"] != "task_local" {
		t.Fatalf("id = %v, want the local task id (upstream id must not leak)", payload["id"])
	}
	if payload["status"] != "succeeded" {
		t.Fatalf("status = %v, want succeeded", payload["status"])
	}
	content, _ := payload["content"].(map[string]any)
	if content["video_url"] != "https://upstream/v.mp4" {
		t.Fatalf("content.video_url = %v", content["video_url"])
	}
	usage, _ := payload["usage"].(map[string]any)
	if usage["total_tokens"] != float64(30) {
		t.Fatalf("usage must survive: %v", payload["usage"])
	}
	if payload["model"] != "seedance-1-5-pro-250528" {
		t.Fatalf("model must survive: %v", payload["model"])
	}
}

// 上游响应丢了 content 时用本地结果地址补齐；否则客户端拿到空 URL 却以为成功。
func TestApiduoArkTaskPayloadFillsResultURL(t *testing.T) {
	task := &model.Task{
		TaskID:    "task_local",
		Status:    model.TaskStatusSuccess,
		CreatedAt: 1700000000,
		Data:      json.RawMessage(`{"id":"cgt-upstream","status":"succeeded"}`),
	}
	task.PrivateData.ResultURL = "https://gateway/proxy/v.mp4"

	payload := apiduoArkTaskPayload(task)
	content, _ := payload["content"].(map[string]any)
	if content["video_url"] != "https://gateway/proxy/v.mp4" {
		t.Fatalf("content.video_url = %v, want the local result URL", content["video_url"])
	}
}

// 没有上游数据（刚提交就被查）也要给出可用形状，而不是空对象。
func TestApiduoArkTaskPayloadWithoutUpstreamData(t *testing.T) {
	task := &model.Task{TaskID: "task_local", Status: model.TaskStatusQueued, CreatedAt: 1700000000}

	payload := apiduoArkTaskPayload(task)
	if payload["id"] != "task_local" || payload["status"] != "queued" {
		t.Fatalf("unexpected payload: %v", payload)
	}
	if payload["created_at"] != int64(1700000000) {
		t.Fatalf("created_at = %v", payload["created_at"])
	}
	if _, exists := payload["content"]; exists {
		t.Fatalf("a queued task must not carry content: %v", payload)
	}
}

// 上游顶层不是对象时不能 panic，也不能把数组原样回给客户端。
func TestApiduoArkTaskPayloadToleratesNonObjectData(t *testing.T) {
	task := &model.Task{TaskID: "task_local", Status: model.TaskStatusInProgress, Data: json.RawMessage(`[1,2,3]`)}

	payload := apiduoArkTaskPayload(task)
	if payload["id"] != "task_local" || payload["status"] != "running" {
		t.Fatalf("unexpected payload: %v", payload)
	}
}

// 失败任务要带 error，客户端才知道不用再轮询了。
func TestApiduoArkTaskPayloadFailure(t *testing.T) {
	task := &model.Task{
		TaskID:     "task_local",
		Status:     model.TaskStatusFailure,
		FailReason: "upstream rejected the prompt",
	}

	payload := apiduoArkTaskPayload(task)
	if payload["status"] != "failed" {
		t.Fatalf("status = %v, want failed", payload["status"])
	}
	failure, _ := payload["error"].(map[string]any)
	if failure["message"] != "upstream rejected the prompt" {
		t.Fatalf("error = %v", payload["error"])
	}
}

func TestApiduoArkTaskStatus(t *testing.T) {
	cases := []struct {
		status model.TaskStatus
		want   string
	}{
		{model.TaskStatusNotStart, "queued"},
		{model.TaskStatusSubmitted, "queued"},
		{model.TaskStatusQueued, "queued"},
		{model.TaskStatusInProgress, "running"},
		{model.TaskStatusSuccess, "succeeded"},
		{model.TaskStatusFailure, "failed"},
	}
	for _, testCase := range cases {
		if got := apiduoArkTaskStatus(testCase.status); got != testCase.want {
			t.Fatalf("%s: got %q, want %q", testCase.status, got, testCase.want)
		}
	}
}

// 上游已经给出终态时不能改写：Ark 会回 cancelled / expired，这两个状态
// 本地枚举里没有。若按本地状态映射成 queued，客户端会一直轮询一个再也
// 不会变化的任务。
func TestApiduoArkTaskPayloadKeepsUpstreamTerminalStatus(t *testing.T) {
	task := &model.Task{
		TaskID:     "task_local",
		Status:     model.TaskStatusFailure,
		FailReason: "upstream cancelled it",
		Data:       json.RawMessage(`{"id":"cgt-upstream","status":"cancelled"}`),
	}

	payload := apiduoArkTaskPayload(task)
	if payload["status"] != "cancelled" {
		t.Fatalf("status = %v, want cancelled (the upstream terminal state)", payload["status"])
	}
	if _, exists := payload["error"]; exists {
		t.Fatalf("a cancelled task must not grow a synthetic error: %v", payload["error"])
	}
}

// 本地成功优先：轮询已经落地结果，但上游最近一次查询还停在 running 时，
// 客户端必须拿到 succeeded 与结果地址，而不是继续等。
func TestApiduoArkTaskPayloadLocalSuccessWins(t *testing.T) {
	task := &model.Task{
		TaskID: "task_local",
		Status: model.TaskStatusSuccess,
		Data:   json.RawMessage(`{"id":"cgt-upstream","status":"running"}`),
	}
	task.PrivateData.ResultURL = "https://gateway/proxy/v.mp4"

	payload := apiduoArkTaskPayload(task)
	if payload["status"] != "succeeded" {
		t.Fatalf("status = %v, want succeeded", payload["status"])
	}
	content, _ := payload["content"].(map[string]any)
	if content["video_url"] != "https://gateway/proxy/v.mp4" {
		t.Fatalf("content.video_url = %v", content["video_url"])
	}
}

// 网关判定的失败（轮询失败、超时）上游可能还停在 queued：必须改写成
// failed 并带 error，否则客户端永远等不到终态。
func TestApiduoArkTaskPayloadLocalFailureOverridesInFlightStatus(t *testing.T) {
	task := &model.Task{
		TaskID:     "task_local",
		Status:     model.TaskStatusFailure,
		FailReason: "task timed out",
		Data:       json.RawMessage(`{"id":"cgt-upstream","status":"queued"}`),
	}

	payload := apiduoArkTaskPayload(task)
	if payload["status"] != "failed" {
		t.Fatalf("status = %v, want failed", payload["status"])
	}
	failure, _ := payload["error"].(map[string]any)
	if failure["code"] != "InternalServiceError" || failure["message"] != "task timed out" {
		t.Fatalf("error = %v", payload["error"])
	}
}

// 错误码照方舟的错误码表，与 doubao 插件同一张表。
func TestApiduoArkErrorCode(t *testing.T) {
	cases := map[int]string{
		400: "InvalidParameter",
		401: "AuthenticationError",
		403: "AccessDenied",
		404: "NotFound",
		408: "RequestTimeout",
		429: "AccountRateLimitExceeded",
		500: "InternalServiceError",
		502: "InternalServiceError",
	}
	for status, want := range cases {
		if got := apiduoArkErrorCode(status); got != want {
			t.Fatalf("status %d: code = %q, want %q", status, got, want)
		}
	}
}
