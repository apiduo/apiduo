package controller

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// These tests pin the invariants the renderer relies on. The same-origin
// rejection in particular is a security boundary: an embedded page served from
// our own origin could read our cookies and local storage, turning a
// navigation entry into a persistent script channel.

const navAppTestHost = "gateway.example.com"

func navAppTestContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("PUT", "http://"+navAppTestHost+"/api/nav_apps", nil)
	c.Request.Host = navAppTestHost
	return c
}

func validNavApp() model.NavApp {
	return model.NavApp{
		Slug:        "langfuse",
		Name:        "Langfuse",
		Href:        "https://langfuse.other.example",
		DisplayType: navAppDisplayIframe,
		Placement:   navAppPlacementSide,
		MinRole:     common.RoleCommonUser,
		Sort:        0,
		Enabled:     true,
	}
}

func TestNormalizeNavAppAcceptsExternalTarget(t *testing.T) {
	app := validNavApp()
	_, key, _ := normalizeNavApp(navAppTestContext(), &app)
	assert.Empty(t, key)
	assert.Equal(t, navAppDisplayIframe, app.DisplayType)
}

func TestNormalizeNavAppRejectsRelativeTarget(t *testing.T) {
	cases := []string{
		"/profile",
		"profile",
		"//gateway.example.com/profile",
		"javascript:alert(1)",
		"data:text/html,<script>alert(1)</script>",
		"",
	}
	for _, href := range cases {
		t.Run(href, func(t *testing.T) {
			app := validNavApp()
			app.Href = href
			_, key, _ := normalizeNavApp(navAppTestContext(), &app)
			assert.Equal(t, "Target address must be an absolute http(s) URL", key)
		})
	}
}

func TestNormalizeNavAppRejectsSameOriginTarget(t *testing.T) {
	// The check compares the target host with both ServerAddress and the
	// request host, so a deployment reachable on several names is still covered
	// as long as the admin uses one of them.
	for _, href := range []string{
		"https://" + navAppTestHost + "/profile",
		"http://" + navAppTestHost + "/admin",
	} {
		t.Run(href, func(t *testing.T) {
			app := validNavApp()
			app.Href = href
			_, key, _ := normalizeNavApp(navAppTestContext(), &app)
			assert.Equal(
				t,
				"A navigation entry cannot point at this site. Use another host, or open it in a new tab instead of embedding it",
				key,
			)
		})
	}
}

func TestNormalizeNavAppRejectsInvalidSlug(t *testing.T) {
	for _, slug := range []string{"", "-lead", "has space", "has_underscore", "x/y"} {
		t.Run(slug, func(t *testing.T) {
			app := validNavApp()
			app.Slug = slug
			_, key, _ := normalizeNavApp(navAppTestContext(), &app)
			assert.NotEmpty(t, key)
		})
	}
}

func TestNormalizeNavAppLowercasesSlug(t *testing.T) {
	// Case is normalized rather than rejected: the slug is an identifier, and
	// silently fixing the case is friendlier than refusing the save.
	app := validNavApp()
	app.Slug = "LangFuse"
	_, key, _ := normalizeNavApp(navAppTestContext(), &app)
	assert.Empty(t, key)
	assert.Equal(t, "langfuse", app.Slug)
}

func TestNormalizeNavAppRejectsUnknownEnums(t *testing.T) {
	app := validNavApp()
	app.DisplayType = "popup"
	_, key, _ := normalizeNavApp(navAppTestContext(), &app)
	assert.Equal(t, "Display type must be a new tab or an embedded page", key)

	app = validNavApp()
	app.Placement = "footer"
	_, key, _ = normalizeNavApp(navAppTestContext(), &app)
	assert.Equal(t, "Placement must be sidebar, header, or both", key)
}

func TestNormalizeNavAppRejectsRoleOutOfRange(t *testing.T) {
	app := validNavApp()
	app.MinRole = common.RoleRootUser + 1
	_, key, _ := normalizeNavApp(navAppTestContext(), &app)
	assert.Equal(t, "Minimum role must be between 1 and 100", key)
}

func TestNormalizeNavAppAppliesDefaults(t *testing.T) {
	app := validNavApp()
	app.DisplayType = ""
	app.Placement = ""
	app.MinRole = 0
	_, key, _ := normalizeNavApp(navAppTestContext(), &app)
	assert.Empty(t, key)
	assert.Equal(t, navAppDisplayExternal, app.DisplayType)
	assert.Equal(t, navAppPlacementSide, app.Placement)
	assert.Equal(t, common.RoleCommonUser, app.MinRole)
}
