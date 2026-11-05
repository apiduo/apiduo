package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
)

// The revision is the only thing standing between two administrators saving at
// the same time and one of them silently losing their work, so it must be
// derived from the stored text and must change whenever that text changes.

func withNavAppOption(t *testing.T, value string) {
	t.Helper()
	common.OptionMapRWMutex.Lock()
	if common.OptionMap == nil {
		// Unit tests that never boot the option loader start with a nil map.
		common.OptionMap = make(map[string]string)
	}
	original, existed := common.OptionMap[NavAppOptionKey]
	common.OptionMap[NavAppOptionKey] = value
	common.OptionMapRWMutex.Unlock()

	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		defer common.OptionMapRWMutex.Unlock()
		if existed {
			common.OptionMap[NavAppOptionKey] = original
			return
		}
		delete(common.OptionMap, NavAppOptionKey)
	})
}

func TestNavAppsRevisionIsStableForIdenticalContent(t *testing.T) {
	withNavAppOption(t, `[{"slug":"alpha"}]`)
	first := NavAppsRevision()
	second := NavAppsRevision()
	assert.Equal(t, first, second)
	assert.Len(t, first, 16)
}

func TestNavAppsRevisionChangesWithContent(t *testing.T) {
	withNavAppOption(t, "")
	empty := NavAppsRevision()
	assert.NotEmpty(t, empty, "an empty configuration still needs a revision")

	withNavAppOption(t, `[{"slug":"alpha"}]`)
	alpha := NavAppsRevision()
	assert.NotEqual(t, empty, alpha)

	// Re-seeding with different content must produce a different revision even
	// though the row count is unchanged.
	withNavAppOption(t, `[{"slug":"beta"}]`)
	assert.NotEqual(t, alpha, NavAppsRevision())
}
