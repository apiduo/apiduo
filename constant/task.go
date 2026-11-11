package constant

type TaskPlatform string

const (
	TaskPlatformSuno       TaskPlatform = "suno"
	TaskPlatformMidjourney              = "mj"
)

const (
	TaskActionImageToVideo     = "image_to_video"
	TaskActionTextToVideo      = "text_to_video"
	TaskActionFirstTailToVideo = "first_tail_to_video"
	TaskActionReferenceToVideo = "reference_to_video"
	TaskActionRemix            = "remix"
	// APIDUO:image-async:BEGIN
	// TaskActionImageGeneration is an image task submitted through an
	// advanced-async channel, whose entry is the OpenAI images shape rather
	// than a video one. Keeping it distinct stops image work from being
	// counted as text-to-video in the task log and per-action statistics.
	TaskActionImageGeneration = "image_generation"
	// APIDUO:image-async:END
)

var legacyTaskActionAliases = map[string]string{
	"generate":          TaskActionImageToVideo,
	"textGenerate":      TaskActionTextToVideo,
	"firstTailGenerate": TaskActionFirstTailToVideo,
	"referenceGenerate": TaskActionReferenceToVideo,
	"remixGenerate":     TaskActionRemix,
}

// TaskPluginEnabled is the master switch for the whole task-plugin system.
// When disabled, factory and override plugins both stop serving.
var TaskPluginEnabled = true

// NormalizeTaskAction maps persisted legacy action names to the canonical task
// action vocabulary. Unknown platform-specific actions pass through unchanged.
func NormalizeTaskAction(action string) string {
	if canonical, ok := legacyTaskActionAliases[action]; ok {
		return canonical
	}
	return action
}
