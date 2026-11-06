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

	// navAppKindWeb is an ordinary http(s) address. The browser can render it,
	// so it may be opened in a tab or embedded in a frame.
	navAppKindWeb = "web"
	// navAppKindApp is a client deep link such as
	// cherrystudio://providers/api-keys. The browser cannot render it: it hands
	// the address to the operating system, which starts the application
	// registered for that scheme. There is no document to frame.
	navAppKindApp = "app"

	// maxNavAppGroups bounds the headings. Twenty is already more than a
	// sidebar can show before the user has to scroll past the feature.
	maxNavAppGroups = 20
	// maxNavApps bounds a configuration surface a human maintains by hand.
	// Fifty entries already exceed any usable sidebar; the cap exists so an
	// accidental bulk paste cannot grow the option value without limit.
	maxNavApps = 50
)

var navAppSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// navAppSchemePattern is the RFC 3986 scheme grammar, used to read the scheme of
// a value before the rest of it is validated.
var navAppSchemePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*$`)

// navAppAppLinkPattern matches a client deep link: a scheme followed by an
// authority marker and an opaque remainder.
//
// The `//` is required on purpose. Every value that runs script in this origin
// when placed in an anchor href — `javascript:alert(1)`,
// `data:text/html,...` — is an opaque URL without an authority, so demanding
// the marker excludes that whole family before any name-based check runs.
// `url.Parse` cannot make this decision: it accepts those values and only
// reports the scheme afterwards.
var navAppAppLinkPattern = regexp.MustCompile(`(?i)^([a-z][a-z0-9+.-]{1,31})://\S*$`)

// navAppBlockedSchemes would execute script in this console's origin or read
// local resources once the value lands in an anchor href.
//
// The pattern above already rejects `javascript:alert(1)`, but
// `javascript://example.com/%0Aalert(1)` carries an authority and browsers have
// historically executed that shape. Denying these by name is the part that does
// not depend on parser quirks. The list also covers schemes that can never work
// from a page (`chrome://`, `about://`), keeping entries that would silently do
// nothing on click out of the configuration.
var navAppBlockedSchemes = map[string]struct{}{
	"about":                {},
	"blob":                 {},
	"chrome":               {},
	"chrome-extension":     {},
	"data":                 {},
	"file":                 {},
	"filesystem":           {},
	"jar":                  {},
	"javascript":           {},
	"mozilla":              {},
	"ms-browser-extension": {},
	"resource":             {},
	"vbscript":             {},
	"view-source":          {},
}

// navAppTemplateTokens are the placeholders the browser replaces at click time.
//
// The list mirrors `resolveChatUrl` in the frontend: `{key}` and the client
// presets carry the viewer's own API key, `{address}` and `{base_url}` carry
// this deployment's address. They are matched by name rather than by a generic
// `{...}` pattern because a template may legitimately contain JSON braces.
var navAppTemplateTokens = []string{
	"{key}",
	"{address}",
	"{base_url}",
	"{cherryConfig}",
	"{aionuiConfig}",
	"{deepchatConfig}",
	"{aqbotConfig}",
}

// navAppUsesTemplate reports whether a target is resolved in the browser rather
// than used verbatim.
func navAppUsesTemplate(rawURL string) bool {
	for _, token := range navAppTemplateTokens {
		if strings.Contains(rawURL, token) {
			return true
		}
	}
	return false
}

// navAppScheme returns the lowercased scheme rawURL claims, without validating
// the rest of the value.
//
// Needed because url.Parse rejects some inputs this function must still
// attribute to a scheme: `https://{host}/x` fails to parse, and letting it fall
// through to the deep-link pattern would classify a malformed web address as a
// client link — skipping the same-origin rule that only applies to web targets.
func navAppScheme(rawURL string) string {
	end := strings.IndexByte(rawURL, ':')
	if end <= 0 {
		return ""
	}
	candidate := rawURL[:end]
	if !navAppSchemePattern.MatchString(candidate) {
		return ""
	}
	return strings.ToLower(candidate)
}

// navAppHrefKind classifies a target address, returning "" when the value is
// neither an absolute http(s) URL nor an allowed client deep link.
func navAppHrefKind(rawURL string) string {
	if scheme := navAppScheme(rawURL); scheme == "http" || scheme == "https" {
		// A web address must parse and carry a host. It never falls through to
		// the deep-link pattern below, so a malformed value cannot borrow a
		// scheme it does not really have.
		parsed, err := url.Parse(rawURL)
		if err != nil || parsed.Host == "" {
			return ""
		}
		return navAppKindWeb
	}
	match := navAppAppLinkPattern.FindStringSubmatch(rawURL)
	if match == nil {
		return ""
	}
	if _, blocked := navAppBlockedSchemes[strings.ToLower(match[1])]; blocked {
		return ""
	}
	return navAppKindApp
}

type navAppSaveRequest struct {
	Apps []model.NavApp `json:"apps"`
	// GroupTitle renames the sidebar group that holds these entries. Empty
	// restores the translated default.
	GroupTitle string `json:"group_title"`
	// Groups replaces the sidebar group list.
	Groups []model.NavAppGroup `json:"groups"`
	// GroupOrder is the top-level sidebar order as group ids.
	GroupOrder []string `json:"group_order"`
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
// Callers must have classified rawURL as a web address first. A client deep
// link also parses into a host (`cherrystudio://providers` yields `providers`),
// and comparing that against the deployment host would be meaningless.
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
	app.ParentSlug = strings.ToLower(strings.TrimSpace(app.ParentSlug))

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

	// A target is either a web address or a client deep link, and the two
	// differ in what the browser may do with them: only a web address can be
	// framed. Classifying first keeps the same-origin rule below limited to the
	// case it was written for — a deep link has no origin to compare.
	// An empty target makes the entry a group heading for nested entries. The
	// cross-entry pass rejects it when nothing is nested under it, so an empty
	// row can never be saved by accident.
	if app.Href == "" {
		app.DisplayType = navAppDisplayExternal
	} else {
		hrefKind := navAppHrefKind(app.Href)
		if hrefKind == "" {
			return "href", "Target address must be an absolute http(s) URL or a client link such as cherrystudio://", nil
		}
		if hrefKind == navAppKindWeb && navAppTargetsThisSite(app.Href, c) {
			return "href", "A navigation entry cannot point at this site. Use another host, or open it in a new tab instead of embedding it", nil
		}

		switch app.DisplayType {
		case navAppDisplayExternal:
		case navAppDisplayIframe:
			// A deep link resolves to no document, so a frame around it could only
			// ever show an error. Rejecting the combination here keeps the renderer
			// from having to guess what the operator meant.
			if hrefKind == navAppKindApp {
				return "display_type", "A client link cannot be embedded. Choose the new tab option instead", nil
			}
			// An embedded page loads before any click, so a placeholder could not be
			// filled by the viewer's own key — and a key in a frame URL would leak
			// through the target's server logs and Referer.
			if navAppUsesTemplate(app.Href) {
				return "display_type", "A template cannot be embedded. Choose the new tab option or a client link instead", nil
			}
		default:
			return "display_type", "Display type must be a new tab or an embedded page", nil
		}
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
	groups, _ := model.LoadNavAppGroups()
	c.JSON(http.StatusOK, gin.H{
		"success":     true,
		"message":     "",
		"data":        visible,
		"groups":      groups,
		"group_order": model.LoadNavAppGroupOrder(),
		"group_title": model.LoadNavAppGroupTitle(),
	})
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
		"data":        apps,
		"revision":    model.NavAppsRevision(),
		"group_title": model.LoadNavAppGroupTitle(),
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

	if field, key, params := validateNavAppTree(normalized); key != "" {
		if params == nil {
			params = map[string]any{}
		}
		params["field"] = field
		navAppError(c, http.StatusBadRequest, "NAV_APP_INVALID", key, params)
		return
	}

	normalizedGroups, field, key, params := validateNavAppGroups(req.Groups, normalized)
	if key != "" {
		if params == nil {
			params = map[string]any{}
		}
		params["field"] = field
		navAppError(c, http.StatusBadRequest, "NAV_APP_INVALID", key, params)
		return
	}

	sort.SliceStable(normalized, func(i, j int) bool {
		if normalized[i].Sort == normalized[j].Sort {
			return normalized[i].Slug < normalized[j].Slug
		}
		return normalized[i].Sort < normalized[j].Sort
	})

	if err := model.SaveNavAppGroups(normalizedGroups); err != nil {
		common.SysError(common.LogText("failed to save navigation groups: %s", err.Error()))
		navAppError(c, http.StatusInternalServerError, "NAV_APP_STORE_FAILED", "Failed to save navigation entries", nil)
		return
	}

	if err := model.SaveNavAppGroupOrder(req.GroupOrder); err != nil {
		common.SysError(common.LogText("failed to save navigation group order: %s", err.Error()))
		navAppError(c, http.StatusInternalServerError, "NAV_APP_STORE_FAILED", "Failed to save navigation entries", nil)
		return
	}

	if err := model.SaveNavAppGroupTitle(req.GroupTitle); err != nil {
		common.SysError(common.LogText("failed to save navigation group title: %s", err.Error()))
		navAppError(c, http.StatusInternalServerError, "NAV_APP_STORE_FAILED", "Failed to save navigation entries", nil)
		return
	}

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

	groups, _ := model.LoadNavAppGroups()
	c.JSON(http.StatusOK, gin.H{
		"success":     true,
		"message":     "",
		"data":        normalized,
		"groups":      groups,
		"group_order": model.LoadNavAppGroupOrder(),
		"revision":    model.NavAppsRevision(),
		"group_title": model.LoadNavAppGroupTitle(),
	})
}

// validateNavAppTree enforces the two-level menu shape across the whole list.
//
// It is separated from the handler so the rules can be tested without an HTTP
// round trip: every rejection here is a data-shape problem, not a request
// problem.
func validateNavAppTree(apps []model.NavApp) (field, key string, params map[string]any) {
	bySlug := make(map[string]*model.NavApp, len(apps))
	for index := range apps {
		bySlug[apps[index].Slug] = &apps[index]
	}

	for index := range apps {
		parentSlug := apps[index].ParentSlug
		if parentSlug == "" {
			continue
		}
		if parentSlug == apps[index].Slug {
			return "parent_slug", "An entry cannot be its own parent", map[string]any{"slug": parentSlug}
		}
		parentApp, exists := bySlug[parentSlug]
		if !exists {
			return "parent_slug", "Parent entry {{slug}} does not exist", map[string]any{"slug": parentSlug}
		}
		if parentApp.ParentSlug != "" {
			return "parent_slug", "Nesting is limited to two levels: {{slug}} is already a child entry", map[string]any{"slug": parentSlug}
		}
		if strings.TrimSpace(parentApp.Href) != "" {
			return "parent_slug", "Parent entry {{slug}} has a target address; clear it to use the entry as a group heading", map[string]any{"slug": parentSlug}
		}
	}

	// A heading with nothing under it is a dead row the operator cannot see the
	// point of, so it is rejected rather than saved.
	referenced := make(map[string]struct{}, len(apps))
	for index := range apps {
		if parentSlug := apps[index].ParentSlug; parentSlug != "" {
			referenced[parentSlug] = struct{}{}
		}
	}
	for index := range apps {
		if strings.TrimSpace(apps[index].Href) != "" {
			continue
		}
		if _, used := referenced[apps[index].Slug]; used {
			continue
		}
		return "href", "Entry {{slug}} has no target and nothing is nested under it", map[string]any{"slug": apps[index].Slug}
	}

	return "", "", nil
}

// reservedNavAppGroupSlugs are the ids the sidebar renders from code. A custom
// group taking one of them would collide with a built-in heading, and the
// order list could not tell them apart.
var reservedNavAppGroupSlugs = map[string]struct{}{
	"chat":     {},
	"general":  {},
	"personal": {},
	"admin":    {},
}

// validateNavAppGroups normalises the group list and enforces the entry links.
//
// It returns the normalised list plus the first problem in the same shape as
// the other validators, so the handler reports them uniformly.
func validateNavAppGroups(groups []model.NavAppGroup, apps []model.NavApp) ([]model.NavAppGroup, string, string, map[string]any) {
	if len(groups) > maxNavAppGroups {
		return nil, "groups", "Groups are limited to {{max}}", map[string]any{"max": maxNavAppGroups}
	}

	seen := make(map[string]struct{}, len(groups))
	normalized := make([]model.NavAppGroup, 0, len(groups))
	for _, group := range groups {
		group.Slug = strings.ToLower(strings.TrimSpace(group.Slug))
		group.Name = strings.TrimSpace(group.Name)
		group.NameI18n = strings.TrimSpace(group.NameI18n)

		if !navAppSlugPattern.MatchString(group.Slug) {
			return nil, "slug", "Group slug must use lowercase letters, digits and hyphens", map[string]any{"slug": group.Slug, "scope": "groups"}
		}
		if _, reserved := reservedNavAppGroupSlugs[group.Slug]; reserved {
			return nil, "slug", "Group slug {{slug}} is reserved by a built-in sidebar group", map[string]any{"slug": group.Slug, "scope": "groups"}
		}
		if _, duplicated := seen[group.Slug]; duplicated {
			return nil, "slug", "Group slug {{slug}} is already in use", map[string]any{"slug": group.Slug, "scope": "groups"}
		}
		if nameLen := utf8.RuneCountInString(group.Name); nameLen == 0 || nameLen > 100 {
			return nil, "name", "Group name is required and must be at most 100 characters", map[string]any{"slug": group.Slug, "scope": "groups"}
		}
		if group.NameI18n != "" && !jsonLooksLikeObject(group.NameI18n) {
			return nil, "name_i18n", "Localized names must be a JSON object of language tag to text", map[string]any{"slug": group.Slug, "scope": "groups"}
		}
		seen[group.Slug] = struct{}{}
		normalized = append(normalized, group)
	}

	for _, app := range apps {
		if app.GroupSlug == "" {
			continue
		}
		if _, exists := seen[app.GroupSlug]; !exists {
			return nil, "group_slug", "Group {{slug}} does not exist", map[string]any{"slug": app.GroupSlug, "scope": "entries"}
		}
	}

	return normalized, "", "", nil
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
