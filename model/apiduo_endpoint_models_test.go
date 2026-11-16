package model

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// setupEndpointMigrationDB installs an in-memory database as the global DB and
// restores the previous one when the test ends.
//
// SQLite is enough here: what this migration can get wrong is the column name
// and the JSON round trip, and both fail the same way on every dialect. The
// column name is exactly what broke the first version in production
// ("Unknown column 'other_settings'").
func setupEndpointMigrationDB(t testing.TB) *gorm.DB {
	t.Helper()
	previous := DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}))
	DB = db
	t.Cleanup(func() {
		DB = previous
	})
	return db
}

func TestMigrateFileUploadPurposesMovesTheDeclaration(t *testing.T) {
	db := setupEndpointMigrationDB(t)

	legacy := &Channel{
		Type:          1,
		Key:           "sk-legacy",
		Name:          "legacy",
		Models:        "mock-gpt",
		Group:         "default",
		OtherSettings: `{"file_relay":{"purposes":["file-extract","batch"],"max_upload_bytes":1048576},"keep_me":"yes"}`,
	}
	plain := &Channel{
		Type:          1,
		Key:           "sk-plain",
		Name:          "plain",
		Models:        "mock-gpt",
		Group:         "default",
		OtherSettings: `{}`,
	}
	require.NoError(t, db.Create(legacy).Error)
	require.NoError(t, db.Create(plain).Error)

	migrated, err := MigrateFileUploadPurposes()
	require.NoError(t, err)
	assert.Equal(t, 1, migrated, "only the channel that declared purposes should be rewritten")

	var got Channel
	require.NoError(t, db.First(&got, legacy.Id).Error)
	assert.Contains(t, got.Models, "mock-gpt")
	assert.Contains(t, got.Models, "@files/upload/file-extract")
	assert.Contains(t, got.Models, "@files/upload/batch")

	assert.NotContains(t, got.OtherSettings, "purposes", "the retired field must not survive")
	assert.Contains(t, got.OtherSettings, `"max_upload_bytes":1048576`, "the ceiling is still a channel setting")
	assert.Contains(t, got.OtherSettings, `"keep_me":"yes"`, "keys this build does not know must survive")

	var untouched Channel
	require.NoError(t, db.First(&untouched, plain.Id).Error)
	assert.Equal(t, "mock-gpt", untouched.Models)
	assert.Equal(t, "{}", untouched.OtherSettings)

	// A second start must find nothing to do; otherwise every boot rewrites rows
	// and rebuilds the whole ability table.
	again, err := MigrateFileUploadPurposes()
	require.NoError(t, err)
	assert.Equal(t, 0, again)
}

func TestMigrateFileUploadPurposesIgnoresUnreadableSettings(t *testing.T) {
	db := setupEndpointMigrationDB(t)

	broken := &Channel{
		Type:          1,
		Key:           "sk-broken",
		Name:          "broken",
		Models:        "mock-gpt",
		Group:         "default",
		OtherSettings: `file_relay: not json`,
	}
	require.NoError(t, db.Create(broken).Error)

	migrated, err := MigrateFileUploadPurposes()
	require.NoError(t, err)
	assert.Equal(t, 0, migrated)

	var got Channel
	require.NoError(t, db.First(&got, broken.Id).Error)
	assert.Equal(t, "mock-gpt", got.Models, "an unreadable blob must be left alone, not emptied")
	assert.Equal(t, `file_relay: not json`, got.OtherSettings)
}
