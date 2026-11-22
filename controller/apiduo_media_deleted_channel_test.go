package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// APIDUO:artifact-deleted-channel regression: a finished task's artifact must
// stay viewable after its channel is deleted. The channel only supplies the
// proxy and the trusted origin here; without it the fetch goes direct and the
// SSRF check still applies. Returning 503 instead means deleting one channel
// takes every video it ever produced with it.
func TestTaskArtifactSurvivesChannelDeletion(t *testing.T) {
	task := setupGenericTaskTest(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", "4")
		_, _ = w.Write([]byte("data"))
	}))
	defer upstream.Close()
	allowPrivateTaskMediaTest(t)

	// The in-memory channel cache would keep serving the deleted row; a direct
	// database read is what production does when the cache is cold or disabled.
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() { common.MemoryCacheEnabled = previousMemoryCache })

	task.Action = constant.TaskActionTextToVideo
	task.PrivateData.ResultURL = upstream.URL + "/video.mp4"
	require.NoError(t, model.DB.Save(task).Error)

	fetchArtifact := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Set(middleware.TaskArtifactAccessContextKey, true)
		c.Params = gin.Params{
			{Key: "key", Value: task.TaskID},
			{Key: "artifact_key", Value: "video"},
		}
		c.Request = httptest.NewRequest(http.MethodGet, "/v1/tasks/"+task.TaskID+"/artifacts/video/content", nil)
		TaskArtifactContent(c)
		return recorder
	}

	require.Equal(t, http.StatusOK, fetchArtifact().Code, "the channel still exists")

	require.NoError(t, model.DB.Delete(&model.Channel{}, task.ChannelId).Error)
	model.InitChannelCache()

	recorder := fetchArtifact()
	assert.Equal(t, http.StatusOK, recorder.Code, "a deleted channel must not hide its finished artifacts")
	assert.Equal(t, "data", recorder.Body.String())
}
