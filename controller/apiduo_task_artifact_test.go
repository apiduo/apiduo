package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
)

func TestLegacyImageArtifactAvailable(t *testing.T) {
	success := &model.Task{
		TaskID:   "task_img",
		Status:   model.TaskStatusSuccess,
		Action:   constant.TaskActionImageGeneration,
		Platform: constant.TaskPlatform("8001"),
	}
	success.PrivateData.ResultURL = "https://cdn.example.com/a.png"

	if !legacyImageArtifactAvailable(success) {
		t.Fatal("a finished image task with a result URL must expose one image artifact")
	}

	failed := *success
	failed.Status = model.TaskStatusFailure
	if legacyImageArtifactAvailable(&failed) {
		t.Fatal("a failed task must not expose artifacts")
	}

	emptyURL := *success
	emptyURL.PrivateData.ResultURL = ""
	if legacyImageArtifactAvailable(&emptyURL) {
		t.Fatal("a task without a result URL must not expose artifacts")
	}

	videoAction := *success
	videoAction.Action = constant.TaskActionTextToVideo
	if legacyImageArtifactAvailable(&videoAction) {
		t.Fatal("video tasks keep the legacy video path, not the image artifact")
	}
}
