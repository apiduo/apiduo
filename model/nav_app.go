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

// NavApp is one administrator-configured navigation entry.
//
// Design constraints (deliberate):
//
//  1. Href must be an absolute http(s) URL. Dynamic configuration cannot
//     create an in-app page, so allowing site-relative paths would only
//     collide with the file-based router and with the URL map used by
//     SidebarModulesAdmin, producing entries no existing switch can hide.
//     The controller rejects non-absolute and same-origin targets.
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
	Href     string `json:"href"`
	// DisplayType is one of: external (new tab), iframe (embedded).
	DisplayType string `json:"display_type"`
	// Placement is one of: side, top, both.
	Placement string `json:"placement"`
	MinRole   int    `json:"min_role"`
	Sort      int    `json:"sort"`
	Enabled   bool   `json:"enabled"`
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
