package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFileRoutesBypassModelDistributor pins the routing decision that makes
// /v1/files usable at all: the endpoint has no model, so it must not sit behind
// the model distributor. Before this route was moved, every call answered 400
// "model name is required" and the RelayNotImplemented handlers were dead code.
func TestFileRoutesBypassModelDistributor(t *testing.T) {
	setupRelayRouterTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.FileObject{}))

	user := model.User{
		Username: "files-user",
		Status:   common.UserStatusEnabled,
		Group:    "default",
		Quota:    100,
	}
	require.NoError(t, model.DB.Create(&user).Error)
	require.NoError(t, model.DB.Create(&model.Token{
		UserId:         user.Id,
		Key:            "filestestkey",
		Status:         common.TokenStatusEnabled,
		ExpiredTime:    -1,
		UnlimitedQuota: true,
	}).Error)

	engine := gin.New()
	SetRelayRouter(engine)

	call := func(method, path, body, contentType string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer filestestkey")
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, req)
		return recorder
	}

	t.Run("list answers from gateway records", func(t *testing.T) {
		recorder := call(http.MethodGet, "/v1/files", "", "")
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Contains(t, recorder.Body.String(), `"object":"list"`)
		assert.Contains(t, recorder.Body.String(), `"data":[]`)
	})

	t.Run("upload without purpose reaches the handler", func(t *testing.T) {
		body := "--BOUND\r\nContent-Disposition: form-data; name=\"note\"\r\n\r\nx\r\n--BOUND--\r\n"
		recorder := call(http.MethodPost, "/v1/files", body, "multipart/form-data; boundary=BOUND")
		require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
		assert.Contains(t, recorder.Body.String(), "invalid_purpose")
	})

	t.Run("missing id answers 404 not a model error", func(t *testing.T) {
		recorder := call(http.MethodGet, "/v1/files/file-apiduo-missing", "", "")
		require.Equal(t, http.StatusNotFound, recorder.Code, recorder.Body.String())
		assert.Contains(t, recorder.Body.String(), "file_not_found")
	})

	t.Run("delete of unknown id answers 404", func(t *testing.T) {
		recorder := call(http.MethodDelete, "/v1/files/file-apiduo-missing", "", "")
		require.Equal(t, http.StatusNotFound, recorder.Code, recorder.Body.String())
		assert.Contains(t, recorder.Body.String(), "file_not_found")
	})
}
