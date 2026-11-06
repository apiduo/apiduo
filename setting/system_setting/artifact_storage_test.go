package system_setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestArtifactStorageFromEnvUsesPathStyle pins a regression that would break
// the most common deployment: the variables describe a self-hosted MinIO, and
// MinIO rejects virtual-host addressing outright.
func TestArtifactStorageFromEnvUsesPathStyle(t *testing.T) {
	t.Setenv(TaskArtifactStoreModeEnv, "s3")
	t.Setenv(TaskArtifactStoreS3EndpointEnv, "http://minio:9000")
	t.Setenv(TaskArtifactStoreS3BucketEnv, "apiduo-artifacts")
	t.Setenv(TaskArtifactStoreS3RegionEnv, "us-east-1")
	t.Setenv(TaskArtifactStoreS3AccessKeyEnv, "minioadmin")
	t.Setenv(TaskArtifactStoreS3SecretKeyEnv, "minioadmin")

	setting, ok := artifactStorageFromEnv()
	require.True(t, ok)
	require.Len(t, setting.Targets, 1)
	assert.True(t, setting.Targets[0].EffectivePathStyle(),
		"an environment-configured target is MinIO and needs path addressing")
	assert.Equal(t, "http://minio:9000", setting.Targets[0].Endpoint)
}

func TestArtifactStorageFromEnvIgnoresOtherModes(t *testing.T) {
	t.Setenv(TaskArtifactStoreModeEnv, "upstream")
	_, ok := artifactStorageFromEnv()
	assert.False(t, ok)
}

func TestArtifactStorageTargetEffectivePathStyle(t *testing.T) {
	explicit := true
	target := ArtifactStorageTarget{Provider: ArtifactStorageProviderAliyunOSS, PathStyle: &explicit}
	assert.True(t, target.EffectivePathStyle(), "an explicit choice wins over the provider default")

	assert.True(t, ArtifactStorageTarget{Provider: ArtifactStorageProviderMinIO}.EffectivePathStyle())
	assert.False(t, ArtifactStorageTarget{Provider: ArtifactStorageProviderAliyunOSS}.EffectivePathStyle())
	assert.False(t, ArtifactStorageTarget{Provider: ArtifactStorageProviderTencentCOS}.EffectivePathStyle())
	assert.False(t, ArtifactStorageTarget{Provider: ArtifactStorageProviderVolcengine}.EffectivePathStyle())
	// An unset provider is treated as a generic S3 endpoint.
	assert.False(t, ArtifactStorageTarget{}.EffectivePathStyle())
}
