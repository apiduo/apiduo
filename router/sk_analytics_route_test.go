package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The SK analytics endpoint aggregates every user's spend, so it must never be
// reachable without an admin session.
func TestSkAnalyticsRouteRejectsAnonymous(t *testing.T) {
	engine := newAccessTokenScopeTestEngine(t)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/data/sk?start_timestamp=1&end_timestamp=2", nil)
	engine.ServeHTTP(recorder, request)

	// A 404 would mean the route is not registered at all, which would make the
	// "no data leaked" assertion pass for the wrong reason.
	require.NotEqual(t, http.StatusNotFound, recorder.Code, recorder.Body.String())
	require.NotEqual(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.False(t, strings.Contains(recorder.Body.String(), `"success":true`), recorder.Body.String())
}

// The self-scoped variant is readable by any logged-in user, but still must not
// be reachable anonymously.
func TestSelfSkAnalyticsRouteRejectsAnonymous(t *testing.T) {
	engine := newAccessTokenScopeTestEngine(t)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/data/sk/self?start_timestamp=1&end_timestamp=2", nil)
	engine.ServeHTTP(recorder, request)

	require.NotEqual(t, http.StatusNotFound, recorder.Code, recorder.Body.String())
	require.NotEqual(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.False(t, strings.Contains(recorder.Body.String(), `"success":true`), recorder.Body.String())
}
