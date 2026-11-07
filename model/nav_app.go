package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// NavAppOptionKey is the option row holding administrator-configured
// navigation entries. Storing them in the existing option table keeps the
// database schema untouched: no new table, no new column, no migration, and
// therefore nothing to conflict with when upstream is rebased.
const NavAppOptionKey = "CustomNavApps"

// NavAppGroupTitleOptionKey holds the heading shown above the configured
// entries. It is a separate row on purpose: the list option must stay a plain
// JSON array so an older client can still parse it.
const NavAppGroupTitleOptionKey = "NavAppGroupTitle"

// LoadNavAppGroupTitle returns the configured group heading, or "" when the
// administrator has not renamed it.
func LoadNavAppGroupTitle() string {
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	return strings.TrimSpace(common.OptionMap[NavAppGroupTitleOptionKey])
}

// SaveNavAppGroupTitle stores the group heading. An empty value means "use the
// translated default".
func SaveNavAppGroupTitle(title string) error {
	return UpdateOption(NavAppGroupTitleOptionKey, strings.TrimSpace(title))
}

// NavAppGroupsOptionKey holds the administrator-defined sidebar groups.
//
// A separate row from the entry list so an older client that only knows the
// array still parses it.
const NavAppGroupsOptionKey = "NavAppGroups"

// NavAppGroupOrderOptionKey holds the top-level sidebar order, as a JSON array
// of group ids. Ids that are absent from the array keep their default relative
// order and sort after the listed ones, which is what makes a newly added
// built-in group appear instead of vanishing.
const NavAppGroupOrderOptionKey = "NavAppGroupOrder"

// NavAppGroup is one administrator-defined sidebar heading.
//
// Groups live apart from entries: a heading needs its own name, localization and
// position, and an empty group is a valid state (created but not filled yet).
type NavAppGroup struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
	// NameI18n is an optional JSON object of language tag to display name.
	NameI18n string `json:"name_i18n"`
	Sort     int    `json:"sort"`
}

// LoadNavAppGroups reads the configured groups.
func LoadNavAppGroups() ([]NavAppGroup, error) {
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap[NavAppGroupsOptionKey]
	common.OptionMapRWMutex.RUnlock()

	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var groups []NavAppGroup
	if err := json.Unmarshal([]byte(raw), &groups); err != nil {
		return nil, err
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].Sort == groups[j].Sort {
			return groups[i].Slug < groups[j].Slug
		}
		return groups[i].Sort < groups[j].Sort
	})
	return groups, nil
}

// SaveNavAppGroups replaces the whole group list in one option write.
func SaveNavAppGroups(groups []NavAppGroup) error {
	data, err := json.Marshal(groups)
	if err != nil {
		return err
	}
	return UpdateOption(NavAppGroupsOptionKey, string(data))
}

// LoadNavAppGroupOrder reads the configured top-level order.
func LoadNavAppGroupOrder() []string {
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap[NavAppGroupOrderOptionKey]
	common.OptionMapRWMutex.RUnlock()

	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var order []string
	if err := json.Unmarshal([]byte(raw), &order); err != nil {
		return nil
	}
	return order
}

// SaveNavAppGroupOrder stores the top-level order.
func SaveNavAppGroupOrder(order []string) error {
	data, err := json.Marshal(order)
	if err != nil {
		return err
	}
	return UpdateOption(NavAppGroupOrderOptionKey, string(data))
}

// NavApp is one administrator-configured navigation entry.
//
// Design constraints (deliberate):
//
//  1. Href is either an absolute http(s) URL or a client deep link such as
//     cherrystudio://providers/api-keys. Dynamic configuration cannot create an
//     in-app page, so allowing site-relative paths would only collide with the
//     file-based router and with the URL map used by SidebarModulesAdmin,
//     producing entries no existing switch can hide. A deep link is accepted
//     because the browser hands it to the operating system, which lets the
//     console launch an installed client in one click; such an entry is
//     external-only, since there is no document to frame. The controller
//     rejects relative and same-origin targets, and denies every scheme that
//     would run script in this origin.
//  2. Slug is the stable identifier. The frontend keys per-entry UI state on
//     it, so it is immutable once created.
//  3. MinRole reuses the existing role constants. User groups in this project
//     are a billing concept (channel/token routing), not an authorization
//     dimension, so visibility is expressed as a minimum role.
type NavApp struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
	// NameI18n is an optional JSON object of language tag to display name.
	// Empty means "use Name for every language".
	NameI18n string `json:"name_i18n"`
	Icon     string `json:"icon"`
	// IconURL is an optional https image shown instead of the preset icon.
	// Only a URL string is stored: administrator input must never become
	// markup in the render tree. The controller restricts it to public hosts.
	IconURL string `json:"icon_url,omitempty"`
	Href    string `json:"href"`
	// DisplayType is one of: external (new tab), iframe (embedded). A client
	// deep link is always external.
	DisplayType string `json:"display_type"`
	// Placement is one of: side, top, both.
	Placement string `json:"placement"`
	MinRole   int    `json:"min_role"`
	Sort      int    `json:"sort"`
	Enabled   bool   `json:"enabled"`
	// ParentSlug nests this entry under another one, forming a two-level menu.
	//
	// It references Slug rather than Name so renaming the parent cannot break
	// the relationship. The controller enforces the shape: the parent must
	// exist, must not itself have a parent, and must not carry a target - a
	// parent is a heading, not a link.
	ParentSlug string `json:"parent_slug,omitempty"`
	// GroupSlug places this entry under a configured group. Empty means the
	// default group, which keeps pre-grouping data working without a migration.
	GroupSlug string `json:"group_slug,omitempty"`
}

// LoadNavApps reads and parses the configured entries.
//
// An unparsable value returns an error for the admin console to surface; the
// renderer ignores it and shows no custom entries, so a corrupted option can
// never break navigation for end users.
func LoadNavApps() ([]NavApp, error) {
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap[NavAppOptionKey]
	common.OptionMapRWMutex.RUnlock()

	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}

	var apps []NavApp
	if err := json.Unmarshal([]byte(raw), &apps); err != nil {
		return nil, err
	}
	sort.SliceStable(apps, func(i, j int) bool {
		if apps[i].Sort == apps[j].Sort {
			return apps[i].Slug < apps[j].Slug
		}
		return apps[i].Sort < apps[j].Sort
	})
	return apps, nil
}

// SaveNavApps replaces the whole list in one option write.
//
// A full replace rather than per-entry writes is intentional: the option table
// stores a single value, so committing the entire list in one write keeps the
// stored configuration internally consistent and matches how the admin form
// operates (draft locally, save once).
func SaveNavApps(apps []NavApp) error {
	data, err := json.Marshal(apps)
	if err != nil {
		return err
	}
	return UpdateOption(NavAppOptionKey, string(data))
}

// NavAppsRevision returns a short content hash of the stored value.
//
// It backs optimistic concurrency control: the console submits the revision it
// loaded, and a save whose base no longer matches is rejected instead of
// silently overwriting another administrator's edit. The value is derived from
// the stored text, so no schema change or extra option row is involved.
func NavAppsRevision() string {
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap[NavAppOptionKey]
	common.OptionMapRWMutex.RUnlock()

	return navAppsRevisionOf(raw)
}

// navAppsRevisionOf hashes one stored value. Split out so tests and benchmarks
// can exercise it without touching the shared option map.
func navAppsRevisionOf(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:8])
}
