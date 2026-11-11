package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
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
		model.TaskStatusSuccess:    "completed",
		model.TaskStatusFailure:    "failed",
	}
	for status, want := range cases {
		if got := apiduoImageAsyncStatus(status); got != want {
			t.Fatalf("apiduoImageAsyncStatus(%q) = %q, want %q", status, got, want)
		}
	}
}

// 同号任务可能属于别的平台。开放这条入口之后，一次猜号就能读到别人的图，
// 所以归属判断必须只认高级异步渠道。
func TestApiduoImageAsyncOwnsTask(t *testing.T) {
	if !apiduoImageAsyncOwnsTask(constant.TaskPlatform("8001")) {
		t.Fatal("advanced-async platform must own the image entry")
	}
	for _, platform := range []constant.TaskPlatform{"suno", "mj", "doubao", ""} {
		if apiduoImageAsyncOwnsTask(platform) {
			t.Fatalf("platform %q must not own the image entry", platform)
		}
	}
}
