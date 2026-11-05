package middleware

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A feature added after this point is expected to be agent-callable, and the
// fail-closed rule in enforceAccessTokenRoute means "callable" starts with
// "declared here". Forgetting the declaration is silent at compile time and
// only shows up as a 403 at runtime, so it is pinned by a test.

func TestNavAppRoutesDeclareScopes(t *testing.T) {
	cases := map[string]string{
		"GET /api/nav_apps":     "nav_app:read",
		"GET /api/nav_apps/all": "nav_app:read",
		"PUT /api/nav_apps":     "nav_app:write",
	}

	for key, want := range cases {
		rule, declared := AccessTokenRouteRule(key)
		assert.True(t, declared, "%s must declare an access token rule", key)
		assert.Equal(t, want, rule.Scope(), key)
	}
}

func TestUndeclaredRoutesStayClosed(t *testing.T) {
	// A route nobody declared must not be reachable by a scoped token. This is
	// the behaviour the declaration table depends on.
	_, declared := AccessTokenRouteRule("GET /api/definitely-not-declared")
	assert.False(t, declared)
}
