package model

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/endpoint"
)

// MigrateFileUploadPurposes moves the /v1/files opt-in out of the retired
// per-channel purpose list and into the channel's model list.
//
// Background: a channel used to declare file purposes in
// other_settings.file_relay.purposes, which was a second capability table read
// only by the file handler. Routing now uses the model list like every other
// endpoint, so an upgrade that skipped this step would leave every existing
// file channel unable to receive an upload — the ability row for
// "@files/upload/<purpose>" would simply not exist.
//
// The column is named settings while the Go field is OtherSettings, so the
// condition and the update name the column, not the field.
//
// It rewrites through map[string]any rather than the typed settings struct so
// keys this build does not know about survive untouched. The stale purpose list
// is removed in the same write; dropping only the read path would leave a field
// in the console's JSON that no longer means anything.
func MigrateFileUploadPurposes() (int, error) {
	var channels []*Channel
	if err := DB.Where("settings LIKE ?", "%file_relay%").Find(&channels).Error; err != nil {
		return 0, err
	}
	migrated := 0
	for _, channel := range channels {
		settings := map[string]any{}
		if err := common.UnmarshalJsonStr(channel.OtherSettings, &settings); err != nil {
			common.SysError(common.LogText("file relay migration: channel #%d has unreadable settings: %s", channel.Id, err.Error()))
			continue
		}
		block, ok := settings["file_relay"].(map[string]any)
		if !ok {
			continue
		}
		purposes := stringSlice(block["purposes"])
		if len(purposes) == 0 && block["purposes"] == nil {
			continue
		}

		names := channel.GetModels()
		existing := make(map[string]struct{}, len(names))
		for _, name := range names {
			existing[name] = struct{}{}
		}
		added := false
		for _, purpose := range purposes {
			name := endpoint.FileUploadModel(purpose)
			if _, ok := existing[name]; ok {
				continue
			}
			names = append(names, name)
			existing[name] = struct{}{}
			added = true
		}

		delete(block, "purposes")
		settings["file_relay"] = block
		encoded, err := common.Marshal(settings)
		if err != nil {
			common.SysError(common.LogText("file relay migration: channel #%d settings could not be encoded: %s", channel.Id, err.Error()))
			continue
		}
		updates := map[string]any{"settings": string(encoded)}
		if added {
			updates["models"] = strings.Join(names, ",")
		}
		if err := DB.Model(&Channel{}).Where("id = ?", channel.Id).Updates(updates).Error; err != nil {
			common.SysError(common.LogText("file relay migration: channel #%d could not be updated: %s", channel.Id, err.Error()))
			continue
		}
		migrated++
	}
	return migrated, nil
}

// stringSlice accepts the values JSON can produce for a list of strings and
// ignores anything else, so one malformed entry cannot abort the migration.
func stringSlice(raw any) []string {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if value, ok := item.(string); ok && value != "" {
			out = append(out, value)
		}
	}
	return out
}
