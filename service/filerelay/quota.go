package filerelay

import (
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
)

// The two refusal reasons are kept apart so the handler can answer with a
// status the client can act on: 429 means wait, 403 means delete something.
var (
	ErrDailyLimit  = errors.New("daily upload limit reached")
	ErrStorageFull = errors.New("storage quota reached")
)

const uploadWindowSeconds = int64(24 * 3600)

// CheckAllowance decides whether this upload may be forwarded at all.
//
// It runs before the provider is contacted, because a rejected upload that has
// already cost a provider round trip still occupies that provider's storage.
func CheckAllowance(userId int, size int64, settings *system_setting.FileRelaySetting) error {
	if userId <= 0 {
		return fmt.Errorf("upload requires an authenticated user")
	}
	if settings == nil {
		return nil
	}
	// Expired rows keep occupying the storage ceiling until something removes
	// them. The upload path is the only place that runs often enough to sweep
	// without adding a background worker, and it is cheap because it is scoped
	// to this account.
	if _, err := model.DeleteExpiredFileObjectsOfUser(userId, time.Now().Unix()); err != nil {
		common.SysError(common.LogText("file relay: failed to prune expired files for user %d: %s", userId, err.Error()))
	}
	if settings.UserStorageLimitBytes > 0 {
		usage, err := model.GetFileUsage(userId, 0)
		if err != nil {
			return err
		}
		if usage.TotalBytes+size > settings.UserStorageLimitBytes {
			return fmt.Errorf("%w: %d of %d bytes in use", ErrStorageFull, usage.TotalBytes, settings.UserStorageLimitBytes)
		}
	}
	if settings.DailyUploadLimit > 0 {
		usage, err := model.GetFileUsage(userId, time.Now().Unix()-uploadWindowSeconds)
		if err != nil {
			return err
		}
		if usage.Count >= int64(settings.DailyUploadLimit) {
			return fmt.Errorf("%w: %d uploads in the last 24 hours", ErrDailyLimit, usage.Count)
		}
	}
	return nil
}

// UploadQuota converts a byte count into quota, rounding up to whole
// megabytes: a 1 byte file still costs one unit, otherwise a client could park
// unlimited tiny files for free.
func UploadQuota(size int64, settings *system_setting.FileRelaySetting) int {
	if settings == nil || settings.QuotaPerMB <= 0 || size <= 0 {
		return 0
	}
	const megabyte = int64(1) << 20
	units := (size + megabyte - 1) / megabyte
	perMB := int64(settings.QuotaPerMB)
	// Clamp before multiplying: a misconfigured price must not wrap the product
	// into a negative charge that credits the caller instead of billing them.
	const maxQuota = int64(1) << 40
	if perMB <= 0 {
		return 0
	}
	if units > maxQuota/perMB {
		return int(maxQuota)
	}
	return int(units * perMB)
}
