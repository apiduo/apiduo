package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// TestFileRelayRoutesAreNotAnsweredByThePluginDispatcher pins the ownership
// split that lets both endpoints work.
//
// GET /v1/files/:id is a host route, so the plugin dispatcher must not answer
// it. GET /v1/files/retrieve stays with the hailuo task plugin and is reached
// through the handoff registered on that same host route. In both cases the
// distinguishing signal is that the answer is not the dispatcher's "Invalid
// URL": relay auth rejects the anonymous probe before the handler runs.
func TestFileRelayRoutesAreNotAnsweredByThePluginDispatcher(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	registerFileRelayRoutes(engine)
	engine.NoRoute(func(c *gin.Context) {
		c.String(http.StatusNotFound, "Invalid URL")
	})

	for _, path := range []string{
		"/v1/files/file_sample",
		"/v1/files/file_sample/content",
		"/v1/files/retrieve",
	} {
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		assert.NotEqual(t, "Invalid URL", recorder.Body.String(), path)
	}
}
