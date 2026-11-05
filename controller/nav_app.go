package controller

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
)

const (
	navAppDisplayExternal = "external"
	navAppDisplayIframe   = "iframe"

	navAppPlacementSide = "side"
	navAppPlacementTop  = "top"
	navAppPlacementBoth = "both"

	// maxNavApps bounds a configuration surface a human maintains by hand.
	// Fifty entries already exceed any usable sidebar; the cap exists so an
	// accidental bulk paste cannot grow the option value without limit.
	maxNavApps = 50
)

var navAppSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

type navAppSaveRequest struct {
	Apps []model.NavApp `json:"apps"`
	// BaseRevision is the revision the client loaded. An empty value skips the
	// check so scripted callers keep working; the console always sends it.
	BaseRevision string `json:"base_revision"`
}

// navAppError writes the shared error envelope plus a stable machine code.
// Agents branch on `code`, never on `message`: NAV_APP_INVALID means fix the
// request, NAV_APP_CONFLICT means re-read then retry, NAV_APP_STORE_* means the
// server failed and the same request may be retried later.
func navAppError(c *gin.Context, status int, code, key string, params map[string]any) {
	common.ApiErrorStatus(c, status, common.NewMessage(key, params), gin.H{"code": code})
}

// navAppTargetHost returns the host of rawURL, or "" when it cannot be parsed.
func navAppTargetHost(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Host)
}

// navAppTargetsThisSite reports whether rawURL points at this deployment.
//
// Two independent sources are consulted because neither alone is reliable:
// ServerAddress is a configured value that may be unset or stale, while the
// request Host reflects how the admin actually reached the console. Rejecting
// on either match is intentional: an embedded page served from our own origin
// can read our cookies and local storage, which would turn a navigation entry
// into a persistent XSS channel. The frontend repeats the check against
// window.location.origin so multi-domain deployments stay covered.
func navAppTargetsThisSite(rawURL string, c *gin.Context) bool {
	target := navAppTargetHost(rawURL)
	if target == "" {
		return false
	}
	if address := strings.TrimSpace(system_setting.ServerAddress); address != "" {
		if configured := navAppTargetHost(address); configured != "" && configured == target {
			return true
		}
	}
	if c != nil && c.Request != nil {
		if requestHost := strings.ToLower(c.Request.Host); requestHost != "" && requestHost == target {
			return true
		}
	}
	return false
}

// navAppField names the request field an agent has to fix. Together with the
// stable error code it is what makes a rejection actionable without parsing
// prose.
type navAppField = string

// normalizeNavApp trims input and applies defaults, returning the message key
// of the first violated invariant and the field that caused it.
func normalizeNavApp(c *gin.Context, app *model.NavApp) (navAppField, string, map[string]any) {
	app.Slug = strings.ToLower(strings.TrimSpace(app.Slug))
	app.Name = strings.TrimSpace(app.Name)
	app.Href = strings.TrimSpace(app.Href)
	app.Icon = strings.TrimSpace(app.Icon)
	app.NameI18n = strings.TrimSpace(app.NameI18n)

	if app.DisplayType == "" {
		app.DisplayType = navAppDisplayExternal
	}
	if app.Placement == "" {
		app.Placement = navAppPlacementSide
	}
	if app.MinRole == 0 {
		app.MinRole = common.RoleCommonUser
	}

	if !navAppSlugPattern.MatchString(app.Slug) {
		return "slug", "Slug must use lowercase letters, digits and hyphens, and start with a letter or digit", nil
	}
	if nameLen := utf8.RuneCountInString(app.Name); nameLen == 0 || nameLen > 100 {
		return "name", "Name is required and must be at most 100 characters", nil
	}
	if app.NameI18n != "" && !jsonLooksLikeObject(app.NameI18n) {
		return "name_i18n", "Localized names must be a JSON object of language tag to text", nil
	}

	// Length caps keep one entry from bloating the single option value, which is
	// rewritten in full on every save.
	if len(app.Href) > 2048 {
		return "href", "Target address must be 2048 characters or fewer", nil
	}
	if len(app.NameI18n) > 4096 {
		return "name_i18n", "Localized names must be 4096 characters or fewer", nil
	}

	parsed, err := url.Parse(app.Href)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "href", "Target address must be an absolute http(s) URL", nil
	}
	if navAppTargetsThisSite(app.Href, c) {
		return "href", "A navigation entry cannot point at this site. Use another host, or open it in a new tab instead of embedding it", nil
	}

	switch app.DisplayType {
	case navAppDisplayExternal, navAppDisplayIframe:
	default:
		return "display_type", "Display type must be a new tab or an embedded page", nil
	}

	switch app.Placement {
	case navAppPlacementSide, navAppPlacementTop, navAppPlacementBoth:
	default:
		return "placement", "Placement must be sidebar, header, or both", nil
	}

	if app.MinRole < common.RoleCommonUser || app.MinRole > common.RoleRootUser {
		return "min_role", "Minimum role must be between 1 and 100", nil
	}
	if app.Sort < 0 || app.Sort > 9999 {
		return "sort", "Sort weight must be between 0 and 9999", nil
	}

	return "", "", nil
}

// jsonLooksLikeObject is a cheap shape check. Full validation happens when the
// frontend parses the value; here we only reject obvious non-objects so a bad
// paste cannot reach the renderer.
func jsonLooksLikeObject(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	return strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}")
}

// GetNavApps lists the entries the current user may see. This endpoint backs
// the sidebar and header renderers, so it is authenticated: entries may point
// at internal hosts that must not be exposed to anonymous visitors.
func GetNavApps(c *gin.Context) {
	apps, err := model.LoadNavApps()
	if err != nil {
		// Corrupted configuration must not break navigation for end users.
		common.SysError(common.LogText("failed to parse navigation entries: %s", err.Error()))
		common.ApiSuccess(c, []model.NavApp{})
		return
	}

	viewerRole := c.GetInt("role")
	visible := make([]model.NavApp, 0, len(apps))
	for _, app := range apps {
		if !app.Enabled || app.MinRole > viewerRole {
			continue
		}
		visible = append(visible, app)
	}
	common.ApiSuccess(c, visible)
}

// GetAllNavApps returns every entry regardless of role or enabled state.
// Admin console only.
func GetAllNavApps(c *gin.Context) {
	apps, err := model.LoadNavApps()
	if err != nil {
		navAppError(c, http.StatusInternalServerError, "NAV_APP_STORE_CORRUPT", "The stored navigation entries cannot be parsed. Fix or clear them before editing", nil)
		return
	}
	// The revision travels alongside the list so the console can submit it back
	// and detect a concurrent edit. It is a separate top-level field rather than
	// part of `data`, which stays a plain array for existing consumers.
	c.JSON(http.StatusOK, gin.H{
		"success":  true,
		"message":  "",
		"data":     apps,
		"revision": model.NavAppsRevision(),
	})
}

// SaveNavApps replaces the whole list. A single write keeps the option value
// internally consistent and matches the console's draft-then-save flow.
func SaveNavApps(c *gin.Context) {
	var req navAppSaveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		navAppError(c, http.StatusBadRequest, "NAV_APP_INVALID", "Invalid parameters", map[string]any{"field": "body"})
		return
	}

	// Optimistic concurrency: the whole list is one option value, so a save
	// based on a stale read would silently drop someone else's change.
	if req.BaseRevision != "" && req.BaseRevision != model.NavAppsRevision() {
		navAppError(
			c,
			http.StatusConflict,
			"NAV_APP_CONFLICT",
			"Navigation entries were changed by another session. Reload the page and try again.",
			nil,
		)
		return
	}

	if len(req.Apps) > maxNavApps {
		navAppError(c, http.StatusBadRequest, "NAV_APP_LIMIT", "Navigation entries are limited to {{max}}. Delete an unused entry first.", map[string]any{"max": maxNavApps})
		return
	}

	seen := make(map[string]struct{}, len(req.Apps))
	normalized := make([]model.NavApp, 0, len(req.Apps))
	for i := range req.Apps {
		app := req.Apps[i]
		if field, key, params := normalizeNavApp(c, &app); key != "" {
			if params == nil {
				params = map[string]any{}
			}
			params["field"] = field
			navAppError(c, http.StatusBadRequest, "NAV_APP_INVALID", key, params)
			return
		}
		if _, duplicated := seen[app.Slug]; duplicated {
			navAppError(c, http.StatusBadRequest, "NAV_APP_INVALID", "Slug {{slug}} is already in use", map[string]any{"slug": app.Slug, "field": "slug"})
			return
		}
		seen[app.Slug] = struct{}{}
		normalized = append(normalized, app)
	}

	sort.SliceStable(normalized, func(i, j int) bool {
		if normalized[i].Sort == normalized[j].Sort {
			return normalized[i].Slug < normalized[j].Slug
		}
		return normalized[i].Sort < normalized[j].Sort
	})

	if err := model.SaveNavApps(normalized); err != nil {
		common.SysError(common.LogText("failed to save navigation entries: %s", err.Error()))
		navAppError(c, http.StatusInternalServerError, "NAV_APP_STORE_FAILED", "Failed to save navigation entries", nil)
		return
	}

	// These entries are global navigation for every account, the same class of
	// change as SidebarModulesAdmin, so the change is recorded in the admin audit
	// trail. Only slugs are kept: names and addresses are not needed to identify
	// what changed and would bloat the entry.
	recordManageAudit(c, "nav_app.update", map[string]any{
		"count": len(normalized),
		"slugs": navAppAuditSlugs(normalized),
	})

	c.JSON(http.StatusOK, gin.H{
		"success":  true,
		"message":  "",
		"data":     normalized,
		"revision": model.NavAppsRevision(),
	})
}

// navAppAuditSlugs caps the identifier list so one audit row stays readable.
func navAppAuditSlugs(apps []model.NavApp) string {
	const limit = 10
	slugs := make([]string, 0, limit+1)
	for index, app := range apps {
		if index == limit {
			slugs = append(slugs, fmt.Sprintf("+%d more", len(apps)-limit))
			break
		}
		slugs = append(slugs, app.Slug)
	}
	return strings.Join(slugs, ", ")
}
