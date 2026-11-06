package system_setting

import (
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
)

// FileRelaySetting is the usage guard for /v1/files.
//
// An upload consumes no tokens, so without a limit the gateway is simply free
// storage: a client can park hundreds of megabytes on a provider account and
// the relay pays for it. Every knob here defaults to "off" except the master
// switch, which is safe because a channel still has to declare the purpose it
// serves before anything is routed.
type FileRelaySetting struct {
	// Enabled is the master switch. When false every /v1/files call answers 503
	// regardless of channel configuration.
	Enabled bool `json:"enabled"`
	// QuotaPerMB is charged per started megabyte of an accepted upload. Zero
	// disables charging entirely.
	QuotaPerMB int `json:"quota_per_mb"`
	// DailyUploadLimit caps uploads per user in a rolling 24 hour window. Zero
	// means unlimited. A rolling window avoids the timezone question a calendar
	// day would raise.
	DailyUploadLimit int `json:"daily_upload_limit"`
	// UserStorageLimitBytes caps the total bytes a user currently holds. Zero
	// means unlimited. Deleting a file frees the room again.
	UserStorageLimitBytes int64 `json:"user_storage_limit_bytes"`
}

var defaultFileRelaySetting = FileRelaySetting{
	Enabled:               true,
	QuotaPerMB:            0,
	DailyUploadLimit:      0,
	UserStorageLimitBytes: 0,
}

func init() {
	config.GlobalConfig.Register("file_relay_setting", &defaultFileRelaySetting)
}

const fileRelaySettingPrefix = "file_relay_setting."

var fileRelaySettingOnce sync.Once

// GetFileRelaySetting returns the live configuration.
//
// The first call replays the persisted option rows into the struct. Registration
// alone only covers live edits: an admin update goes through
// model.handleConfigUpdate, which reaches the struct by reflection, but nothing
// replays option rows at boot. Without this pass a restart would silently
// restore the defaults and start charging zero again.
func GetFileRelaySetting() *FileRelaySetting {
	fileRelaySettingOnce.Do(func() {
		common.OptionMapRWMutex.RLock()
		options := make(map[string]string, 4)
		for key, value := range common.OptionMap {
			if field, ok := strings.CutPrefix(key, fileRelaySettingPrefix); ok {
				options[field] = value
			}
		}
		common.OptionMapRWMutex.RUnlock()
		if len(options) == 0 {
			return
		}
		if err := config.UpdateConfigFromMap(&defaultFileRelaySetting, options); err != nil {
			common.SysError(common.LogText("failed to load file relay setting: %s", err.Error()))
		}
	})
	return &defaultFileRelaySetting
}
