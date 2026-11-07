package controller

import (
	"encoding/json"
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

// These tests pin the invariants the renderer relies on. The same-origin
// rejection in particular is a security boundary: an embedded page served from
// our own origin could read our cookies and local storage, turning a
// navigation entry into a persistent script channel.

const navAppTestHost = "gateway.example.com"

// The two rejections below are shared by several cases, so the exact wording
// lives here: the same string is the frontend translation key, and a silent
// divergence between the two would surface as an untranslated toast.
const (
	navAppInvalidTargetKey = "Target address must be an absolute http(s) URL or a client link such as cherrystudio://"
	navAppCannotEmbedKey   = "A client link cannot be embedded. Choose the new tab option instead"
	navAppTemplateEmbedKey = "A template cannot be embedded. Choose the new tab option or a client link instead"
)

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
		// Claims a web scheme but carries no host: malformed, and must not be
		// reclassified as a deep link just because the scheme passes the
		// pattern.
		"https://",
		// An empty target is deliberately absent here: it makes the entry a
		// group heading, and validateNavAppTree rejects a heading that nothing
		// is nested under.
	}
	for _, href := range cases {
		t.Run(href, func(t *testing.T) {
			app := validNavApp()
			app.Href = href
			_, key, _ := normalizeNavApp(navAppTestContext(), &app)
			assert.Equal(t, navAppInvalidTargetKey, key)
		})
	}
}

// A client deep link launches an installed application instead of opening a
// document. It is what makes a "Cherry Studio" sidebar entry work in one click.
func TestNormalizeNavAppAcceptsClientLink(t *testing.T) {
	cases := []string{
		"cherrystudio://providers/api-keys?v=1&data=aGVsbG8=",
		"cherrystudio://",
		"vscode://file/tmp/project",
		"obsidian://open?vault=notes",
	}
	for _, href := range cases {
		t.Run(href, func(t *testing.T) {
			app := validNavApp()
			app.Href = href
			app.DisplayType = navAppDisplayExternal
			_, key, _ := normalizeNavApp(navAppTestContext(), &app)
			assert.Empty(t, key)
		})
	}
}

// A placeholder is resolved in the browser, using the viewer's own key, so the
// stored value never contains a credential.
func TestNormalizeNavAppAcceptsTemplate(t *testing.T) {
	cases := []struct {
		href        string
		displayType string
	}{
		{"cherrystudio://providers/api-keys?v=1&data={cherryConfig}", navAppDisplayExternal},
		{"cherrystudio://providers/api-keys?v=1&data={cherryConfig}&x=1", navAppDisplayExternal},
		{"https://chat.example.com/?key={key}", navAppDisplayExternal},
		{"https://example.com/set?provider={\"type\":\"openai\",\"apiKey\":\"{key}\"}", navAppDisplayExternal},
		{"https://example.com/?base={address}", navAppDisplayExternal},
	}
	for _, tc := range cases {
		t.Run(tc.href, func(t *testing.T) {
			app := validNavApp()
			app.Href = tc.href
			app.DisplayType = tc.displayType
			_, key, _ := normalizeNavApp(navAppTestContext(), &app)
			assert.Empty(t, key)
		})
	}
}

func TestNormalizeNavAppRejectsTemplateWhenEmbedded(t *testing.T) {
	for _, href := range []string{
		"https://chat.example.com/?key={key}",
		"https://example.com/?base={address}",
	} {
		t.Run(href, func(t *testing.T) {
			// validNavApp is an iframe entry.
			app := validNavApp()
			app.Href = href
			_, key, _ := normalizeNavApp(navAppTestContext(), &app)
			assert.Equal(t, navAppTemplateEmbedKey, key)
		})
	}
}

// A `{` in the host makes url.Parse fail. Without attributing the value to the
// scheme it claims, it would fall through to the deep-link pattern and skip the
// same-origin rule that only applies to web targets.
func TestNormalizeNavAppRejectsPlaceholderInHost(t *testing.T) {
	for _, href := range []string{
		"https://{host}/path?k={key}",
		"https://{gateway.example.com}/admin",
	} {
		t.Run(href, func(t *testing.T) {
			app := validNavApp()
			app.Href = href
			app.DisplayType = navAppDisplayExternal
			_, key, _ := normalizeNavApp(navAppTestContext(), &app)
			assert.Equal(t, navAppInvalidTargetKey, key)
		})
	}
}

func TestNormalizeNavAppRejectsClientLinkWhenEmbedded(t *testing.T) {
	// validNavApp is an iframe entry. A deep link resolves to no document, so
	// framing it could only ever render an error page.
	app := validNavApp()
	app.Href = "cherrystudio://providers/api-keys"
	_, key, _ := normalizeNavApp(navAppTestContext(), &app)
	assert.Equal(t, navAppCannotEmbedKey, key)
}

// Each value here would either run script in this console's origin once placed
// in an anchor href or read local resources. The first three carry an authority
// marker, so the `scheme://` shape check alone would let them through — only
// the scheme denial stops them.
func TestNormalizeNavAppRejectsDangerousSchemes(t *testing.T) {
	for _, href := range []string{
		"javascript://example.com/%0Aalert(1)",
		"data://text/html,<script>alert(1)</script>",
		"file:///etc/passwd",
		"vbscript://msgbox(1)",
		"about://blank",
		"chrome://settings",
		"view-source://https://example.com",
		"cherrystudio:providers",
	} {
		t.Run(href, func(t *testing.T) {
			app := validNavApp()
			app.Href = href
			app.DisplayType = navAppDisplayExternal
			_, key, _ := normalizeNavApp(navAppTestContext(), &app)
			assert.Equal(t, navAppInvalidTargetKey, key)
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

func TestValidateNavAppTreeAcceptsTwoLevels(t *testing.T) {
	apps := []model.NavApp{
		{Slug: "tools", Name: "Tools"},
		{Slug: "probe", Name: "Probe", Href: "https://probe.internal", ParentSlug: "tools"},
	}
	field, key, _ := validateNavAppTree(apps)
	assert.Empty(t, field, "a parent without a target and one child must be valid")
	assert.Empty(t, key)
}

func TestValidateNavAppTreeRejectsSelfParent(t *testing.T) {
	apps := []model.NavApp{{Slug: "tools", Name: "Tools", ParentSlug: "tools"}}
	field, key, _ := validateNavAppTree(apps)
	assert.Equal(t, "parent_slug", field)
	assert.NotEmpty(t, key)
}

func TestValidateNavAppTreeRejectsMissingParent(t *testing.T) {
	apps := []model.NavApp{
		{Slug: "probe", Name: "Probe", Href: "https://probe.internal", ParentSlug: "ghost"},
	}
	field, key, params := validateNavAppTree(apps)
	assert.Equal(t, "parent_slug", field)
	assert.NotEmpty(t, key)
	assert.Equal(t, "ghost", params["slug"])
}

func TestValidateNavAppTreeRejectsThirdLevel(t *testing.T) {
	apps := []model.NavApp{
		{Slug: "tools", Name: "Tools"},
		{Slug: "probe", Name: "Probe", Href: "https://probe.internal", ParentSlug: "tools"},
		{Slug: "deep", Name: "Deep", Href: "https://deep.internal", ParentSlug: "probe"},
	}
	field, key, params := validateNavAppTree(apps)
	assert.Equal(t, "parent_slug", field)
	assert.NotEmpty(t, key)
	assert.Equal(t, "probe", params["slug"])
}

func TestValidateNavAppTreeRejectsParentWithTarget(t *testing.T) {
	apps := []model.NavApp{
		{Slug: "tools", Name: "Tools", Href: "https://tools.internal"},
		{Slug: "probe", Name: "Probe", Href: "https://probe.internal", ParentSlug: "tools"},
	}
	field, key, params := validateNavAppTree(apps)
	assert.Equal(t, "parent_slug", field)
	assert.NotEmpty(t, key)
	assert.Equal(t, "tools", params["slug"])
}

func TestValidateNavAppTreeRejectsOrphanHeading(t *testing.T) {
	apps := []model.NavApp{{Slug: "tools", Name: "Tools"}}
	field, key, params := validateNavAppTree(apps)
	assert.Equal(t, "href", field, "a heading with nothing under it is not usable")
	assert.NotEmpty(t, key)
	assert.Equal(t, "tools", params["slug"])
}

func TestValidateNavAppGroupsAcceptsGroupsAndLinks(t *testing.T) {
	groups := []model.NavAppGroup{
		{Slug: "tools", Name: "Tools"},
		{Slug: "ops", Name: "Ops"},
	}
	apps := []model.NavApp{
		{Slug: "probe", Name: "Probe", Href: "https://probe.internal", GroupSlug: "ops"},
	}
	normalized, field, key, _ := validateNavAppGroups(groups, apps)
	assert.Empty(t, field, key)
	assert.Len(t, normalized, 2)
}

func TestValidateNavAppGroupsRejectsReservedSlug(t *testing.T) {
	for _, reserved := range []string{"chat", "general", "personal", "admin"} {
		groups := []model.NavAppGroup{{Slug: reserved, Name: "Clash"}}
		_, field, key, params := validateNavAppGroups(groups, nil)
		assert.Equal(t, "slug", field, reserved)
		assert.NotEmpty(t, key)
		assert.Equal(t, reserved, params["slug"])
	}
}

func TestValidateNavAppGroupsRejectsDuplicateSlug(t *testing.T) {
	groups := []model.NavAppGroup{
		{Slug: "tools", Name: "Tools"},
		{Slug: "tools", Name: "Tools again"},
	}
	_, field, key, params := validateNavAppGroups(groups, nil)
	assert.Equal(t, "slug", field)
	assert.NotEmpty(t, key)
	assert.Equal(t, "tools", params["slug"])
}

func TestValidateNavAppGroupsRejectsMissingGroupReference(t *testing.T) {
	apps := []model.NavApp{
		{Slug: "probe", Name: "Probe", Href: "https://probe.internal", GroupSlug: "ghost"},
	}
	_, field, key, params := validateNavAppGroups(nil, apps)
	assert.Equal(t, "group_slug", field)
	assert.NotEmpty(t, key)
	assert.Equal(t, "ghost", params["slug"])
}

func TestValidateNavAppGroupsIgnoresUnassignedEntries(t *testing.T) {
	apps := []model.NavApp{{Slug: "probe", Name: "Probe", Href: "https://probe.internal"}}
	_, field, key, _ := validateNavAppGroups(nil, apps)
	assert.Empty(t, field, "an entry with no group is valid and uses the default heading")
	assert.Empty(t, key)
}

// The admin editor seeds its sortable group rows from this endpoint. Omitting
// groups or group_order here makes a saved drag order disappear on the next
// page load even though SaveNavApps stored it correctly.
func TestGetAllNavAppsReturnsGroupConfiguration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/nav_apps/all", nil)

	common.OptionMapRWMutex.Lock()
	if common.OptionMap == nil {
		common.OptionMap = make(map[string]string)
	}
	previousGroups, hadGroups := common.OptionMap[model.NavAppGroupsOptionKey]
	previousOrder, hadOrder := common.OptionMap[model.NavAppGroupOrderOptionKey]
	common.OptionMap[model.NavAppGroupsOptionKey] = `[{"slug":"team","name":"Team","name_i18n":"","sort":0}]`
	common.OptionMap[model.NavAppGroupOrderOptionKey] = `["apiduo-default","chat"]`
	common.OptionMapRWMutex.Unlock()

	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		defer common.OptionMapRWMutex.Unlock()
		if hadGroups {
			common.OptionMap[model.NavAppGroupsOptionKey] = previousGroups
		} else {
			delete(common.OptionMap, model.NavAppGroupsOptionKey)
		}
		if hadOrder {
			common.OptionMap[model.NavAppGroupOrderOptionKey] = previousOrder
		} else {
			delete(common.OptionMap, model.NavAppGroupOrderOptionKey)
		}
	})

	GetAllNavApps(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	assert.Equal(t, []any{"apiduo-default", "chat"}, body["group_order"])
	groups, ok := body["groups"].([]any)
	require.True(t, ok, "groups must be a JSON array, not absent or null")
	assert.Len(t, groups, 1)
}

// A custom icon is fetched by every viewer's browser. Reject anything that
// could turn a navigation entry into a probe of their private network, and
// any markup scheme that could carry script.
func TestValidateNavAppIconURL(t *testing.T) {
	for _, raw := range []string{
		"https://cdn.example.com/logo.png",
		"https://example.com/favicon.ico?v=2",
		"https://example.com:443/a.png",
		"https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/workbuddy-color.svg",
	} {
		assert.Empty(t, validateNavAppIconURL(raw), raw)
	}

	for _, raw := range []string{
		"http://cdn.example.com/logo.png",
		"data:image/svg+xml,<svg onload=alert(1)>",
		"javascript:alert(1)",
		"https://127.0.0.1/a.png",
		"https://192.168.1.1/a.png",
		"https://10.0.0.1/a.png",
		"https://169.254.169.254/latest/meta-data",
		"https://[::1]/a.png",
		"https://localhost/a.png",
		"https://printer.local/a.png",
		"https://user:pass@cdn.example.com/a.png",
		"https://cdn.example.com:8443/a.png",
		"https://example.com/" + strings.Repeat("a", 600),
	} {
		assert.Equal(t, navAppIconURLError, validateNavAppIconURL(raw), raw)
	}
}

func TestNormalizeNavAppKeepsSafeIconURL(t *testing.T) {
	app := validNavApp()
	app.IconURL = "  https://cdn.example.com/logo.png  "
	_, key, _ := normalizeNavApp(navAppTestContext(), &app)
	assert.Empty(t, key)
	assert.Equal(t, "https://cdn.example.com/logo.png", app.IconURL)
}

func TestNormalizeNavAppRejectsUnsafeIconURL(t *testing.T) {
	app := validNavApp()
	app.IconURL = "https://127.0.0.1/a.png"
	_, key, _ := normalizeNavApp(navAppTestContext(), &app)
	assert.Equal(t, navAppIconURLError, key)
}
