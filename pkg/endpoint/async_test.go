package endpoint

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLookupAsyncResolvesBuiltInImageEndpoint(t *testing.T) {
	spec, ok := LookupAsync(AsyncImagePath)
	require.True(t, ok)
	assert.Equal(t, "images.async", spec.ID)
	assert.Equal(t, AsyncProtocolImages, spec.Protocol)
	assert.Equal(t, AsyncImageSubmitPath, spec.Submit,
		"前缀入口要重入到图像自己的提交路径，不能借用视频中继")
}

// The submit landing route is an Images path, and it is its own destination:
// rewriting it again would re-enter the same handler.
func TestLookupAsyncResolvesSubmitLandingRoute(t *testing.T) {
	spec, ok := LookupAsync(AsyncImageSubmitPath)
	require.True(t, ok)
	assert.Equal(t, AsyncProtocolImages, spec.Protocol)
	assert.Equal(t, AsyncImageSubmitPath, spec.Submit)
}

// A submit landing route that pointed at the video relay path would make the
// task log, the presented body and the per-action counters describe an image
// job as video work.
func TestAsyncImageSubmitPathStaysOnImages(t *testing.T) {
	assert.NotContains(t, AsyncImageSubmitPath, "/video/")
}

// The synchronous Images entry must never resolve here. Taking it over would
// turn every ordinary image request into a task id, which is exactly the
// collision the /async prefix exists to prevent.
func TestLookupAsyncRejectsSynchronousAndForeignPaths(t *testing.T) {
	cases := []struct {
		path    string
		comment string
	}{
		{"/v1/images/generations", "同步入口"},
		{"/v1/images/edits", "同步编辑入口"},
		{AsyncImagePath + "/", "尾斜杠不是端点本身"},
		{AsyncImagePath + "/task_1", "带任务号的路径要先切分"},
		{"/async/v1/video/generations", "视频不在这张表里"},
		{"", "空路径"},
	}
	for _, testCase := range cases {
		if _, ok := LookupAsync(testCase.path); !ok {
			continue
		}
		t.Errorf("%s 不该被内置异步端点接管", testCase.comment)
	}
}

// The protocol name is shared with the advanced-async channel configuration,
// so both must stay one definition.
func TestAsyncProtocolImagesMatchesChannelVocabulary(t *testing.T) {
	assert.Equal(t, "images", AsyncProtocolImages)
}
