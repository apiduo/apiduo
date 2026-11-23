package controller

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const exportTestKey = "2026-10/BILL-2026-10-alice-admin.xlsx"

func newExportFileTestRouter(role int) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/export/files/*key", func(c *gin.Context) {
		c.Set("role", role)
	}, DownloadExportFile)
	return router
}

func seedExportFile(t *testing.T) {
	t.Helper()
	t.Setenv(service.ExportFileDirEnv, t.TempDir())
	_, _, err := service.WriteExportFile(exportTestKey, func(w io.Writer) error {
		_, err := w.Write([]byte("payload-1234"))
		return err
	})
	require.NoError(t, err)
}

func TestDownloadExportFileRequiresCapabilityForUsers(t *testing.T) {
	seedExportFile(t)
	router := newExportFileTestRouter(common.RoleCommonUser)

	anonymous := httptest.NewRecorder()
	router.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/api/export/files/"+exportTestKey, nil))
	assert.Equal(t, http.StatusForbidden, anonymous.Code, "a session alone must not open someone else's statement")

	token, err := service.IssueExportFileAccess(exportTestKey, time.Hour)
	require.NoError(t, err)

	signed := httptest.NewRecorder()
	router.ServeHTTP(signed, httptest.NewRequest(http.MethodGet,
		"/api/export/files/"+exportTestKey+"?access="+url.QueryEscape(token), nil))
	require.Equal(t, http.StatusOK, signed.Code)
	assert.Equal(t, "payload-1234", signed.Body.String())
	assert.Equal(t,
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		signed.Header().Get("Content-Type"))
	assert.Contains(t, signed.Header().Get("Content-Disposition"), "BILL-2026-10-alice-admin.xlsx")
	assert.NotEmpty(t, signed.Header().Get("ETag"))
}

func TestDownloadExportFileAllowsAdministrators(t *testing.T) {
	seedExportFile(t)
	router := newExportFileTestRouter(common.RoleAdminUser)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/export/files/"+exportTestKey, nil))
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "payload-1234", recorder.Body.String())
}

func TestDownloadExportFileRejectsBadTokensAndKeys(t *testing.T) {
	seedExportFile(t)
	router := newExportFileTestRouter(common.RoleCommonUser)

	wrongKey := httptest.NewRecorder()
	token, err := service.IssueExportFileAccess(exportTestKey, time.Hour)
	require.NoError(t, err)
	router.ServeHTTP(wrongKey, httptest.NewRequest(http.MethodGet,
		"/api/export/files/2026-10/other.xlsx?access="+url.QueryEscape(token), nil))
	assert.Equal(t, http.StatusForbidden, wrongKey.Code, "a token is bound to one key")

	missing := httptest.NewRecorder()
	router.ServeHTTP(missing, httptest.NewRequest(http.MethodGet,
		"/api/export/files/2026-10/missing.xlsx?access="+url.QueryEscape(token), nil))
	assert.Equal(t, http.StatusForbidden, missing.Code, "no token for a missing key either")
}

func TestDownloadExportFileRejectsTraversal(t *testing.T) {
	t.Setenv(service.ExportFileDirEnv, t.TempDir())
	router := newExportFileTestRouter(common.RoleAdminUser)

	for _, key := range []string{"../../etc/passwd", "2026-10/../../etc/passwd", "2026-10/.."} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/export/files/x", nil)
		context, _ := gin.CreateTestContext(recorder)
		context.Request = request
		context.Set("role", common.RoleAdminUser)
		context.Params = gin.Params{{Key: "key", Value: "/" + key}}
		DownloadExportFile(context)
		assert.Equal(t, http.StatusBadRequest, recorder.Code, "key %q", key)
	}
	_ = router
}

func TestDownloadExportFileServesRanges(t *testing.T) {
	seedExportFile(t)
	router := newExportFileTestRouter(common.RoleAdminUser)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/export/files/"+exportTestKey, nil)
	request.Header.Set("Range", "bytes=0-6")
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusPartialContent, recorder.Code)
	assert.Equal(t, "payload", recorder.Body.String())
}
