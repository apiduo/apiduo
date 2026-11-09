package model

// ApiduoTaskPlatform returns the platform recorded on a task. It exists only
// to decide which router serves a task query: the relay reloads the task for
// the caller afterwards, so this lookup grants no access on its own.
func ApiduoTaskPlatform(taskID string) (string, bool) {
	if DB == nil || taskID == "" {
		return "", false
	}
	var task Task
	if err := DB.Select("platform").Where("task_id = ?", taskID).First(&task).Error; err != nil {
		return "", false
	}
	return string(task.Platform), true
}
