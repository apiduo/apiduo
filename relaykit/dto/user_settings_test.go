package dto

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// IP logging is on unless the user explicitly turned it off: an absent field
// is a user who never chose, not a user who opted out.
func TestRecordsIPDefaultsToOnWhenTheUserNeverChose(t *testing.T) {
	t.Parallel()

	var unset UserSetting
	assert.True(t, unset.RecordsIP())

	off := false
	assert.False(t, UserSetting{RecordIpLog: &off}.RecordsIP())

	on := true
	assert.True(t, UserSetting{RecordIpLog: &on}.RecordsIP())
}

// An explicit opt-out has to survive the JSON round trip, otherwise the next
// settings save would revive a choice the user made.
func TestExplicitOptOutSurvivesRoundTrip(t *testing.T) {
	t.Parallel()

	off := false
	encoded, err := json.Marshal(UserSetting{RecordIpLog: &off})
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"record_ip_log":false`)

	var decoded UserSetting
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.NotNil(t, decoded.RecordIpLog)
	assert.False(t, decoded.RecordsIP())
}

// A user who never chose stays silent on disk, so the default keeps applying.
func TestUnsetChoiceStaysAbsent(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(UserSetting{Language: "zh"})
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "record_ip_log")
}
