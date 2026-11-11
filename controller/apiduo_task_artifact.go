package controller

// APIDUO:legacy-image-artifact
//
// 高级异步渠道（8001，protocol=images）的图片任务没有插件执行记录，
// projectTaskArtifacts 因此返回空数组 —— 任务日志的「制品」弹窗永远空白，
// 而视频任务还有 legacy_content_url 兜底。这里给图片任务补同一条兜底：
// 只要任务成功、结果 URL 还在、动作是图片生成，就投影出一个 image 制品。

import (
	"strings"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
)

// legacyImageArtifactAvailable reports whether a non-plugin image task has a
// retrievable result worth exposing as a single "image" artifact.
func legacyImageArtifactAvailable(task *model.Task) bool {
	if task == nil || task.Status != model.TaskStatusSuccess ||
		taskHasPluginExecution(task) || strings.TrimSpace(task.GetResultURL()) == "" {
		return false
	}
	return constant.NormalizeTaskAction(task.Action) == constant.TaskActionImageGeneration
}
