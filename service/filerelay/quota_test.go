package filerelay

import (
	"testing"

	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/stretchr/testify/assert"
)

func TestUploadQuotaRoundsUpToWholeMegabytes(t *testing.T) {
	priced := &system_setting.FileRelaySetting{QuotaPerMB: 10}

	// A one byte file still costs a unit, otherwise tiny files are free storage.
	assert.Equal(t, 10, UploadQuota(1, priced))
	assert.Equal(t, 10, UploadQuota(1<<20, priced))
	assert.Equal(t, 20, UploadQuota((1<<20)+1, priced))
	assert.Equal(t, 0, UploadQuota(0, priced))

	// Zero price is the documented way to disable charging.
	assert.Equal(t, 0, UploadQuota(1<<30, &system_setting.FileRelaySetting{}))
	assert.Equal(t, 0, UploadQuota(1<<30, nil))
}

// TestUploadQuotaClampsInsteadOfOverflowing pins the guard against a
// misconfigured price: units * perMB would otherwise wrap into a negative
// number, crediting the caller for uploading.
func TestUploadQuotaClampsInsteadOfOverflowing(t *testing.T) {
	settings := &system_setting.FileRelaySetting{QuotaPerMB: 1 << 40}
	const maxQuota = int64(1) << 40

	quota := UploadQuota(512<<20, settings)
	assert.Equal(t, int(maxQuota), quota)
	assert.Positive(t, quota)

	// An ordinary price is untouched by the clamp.
	normal := &system_setting.FileRelaySetting{QuotaPerMB: 10}
	assert.Equal(t, 20, UploadQuota((1<<20)+1, normal))
}
