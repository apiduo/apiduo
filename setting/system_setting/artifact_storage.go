package system_setting

import (
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/blobstore"
	"github.com/QuantumNous/new-api/setting/config"
)

// Artifact storage providers.
//
// All four speak the S3 protocol; they differ only in endpoint shape and
// addressing style. Alibaba OSS exposes an S3-compatible endpoint
// (https://s3.oss-{region}.aliyuncs.com) and Tencent COS does the same
// (https://cos.{region}.myqcloud.com), so one backend implementation serves
// every provider - the field exists to pick sane defaults and to label the
// target in the console.
const (
	ArtifactStorageProviderMinIO      = "minio"
	ArtifactStorageProviderAliyunOSS  = "aliyun-oss"
	ArtifactStorageProviderTencentCOS = "tencent-cos"
	ArtifactStorageProviderVolcengine = "volcengine-tos"
	ArtifactStorageProviderS3         = "s3"
)

// ArtifactStorageTarget is one destination in the gateway-wide pool.
//
// The pool is ordered: a write walks it from first to last and stops at the
// first success, so position is priority. Nothing is replicated - a stored
// artifact lives in exactly one target.
type ArtifactStorageTarget struct {
	Provider  string `json:"provider,omitempty"`
	Endpoint  string `json:"endpoint"`
	Bucket    string `json:"bucket"`
	Region    string `json:"region,omitempty"`
	AccessKey string `json:"access_key,omitempty"`
	SecretKey string `json:"secret_key,omitempty"`
	Prefix    string `json:"prefix,omitempty"`
	// PathStyle is a pointer so "unset" can mean "follow the provider default"
	// while an explicit false still survives a round trip.
	PathStyle *bool `json:"path_style,omitempty"`
	// PublicBaseURL is the base address clients reach this bucket at. It is what
	// makes a rewritten link useful: without it the only address the gateway can
	// produce is a presigned URL, which expires and would break the response it
	// was just written into.
	PublicBaseURL string `json:"public_base_url,omitempty"`
}

// EffectivePathStyle resolves the addressing style for this target.
//
// MinIO serves buckets as a path segment and breaks on virtual-host addressing;
// every other provider here expects virtual-host by default.
func (t ArtifactStorageTarget) EffectivePathStyle() bool {
	if t.PathStyle != nil {
		return *t.PathStyle
	}
	return t.Provider == ArtifactStorageProviderMinIO
}

// ArtifactStorageSetting is the gateway-wide storage configuration.
//
// Targets holds the ordered pool. The scalar fields below it are the original
// single-target shape: they are still honoured when Targets is empty so an
// existing environment file or an already-saved option row keeps working
// without a migration step.
type ArtifactStorageSetting struct {
	Enabled bool `json:"enabled"`
	// Targets is the ordered fallback pool.
	Targets []ArtifactStorageTarget `json:"targets"`
	// Deprecated single-target fields, kept for compatibility.
	Endpoint  string `json:"endpoint"`
	Bucket    string `json:"bucket"`
	Region    string `json:"region"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
	Prefix    string `json:"prefix"`
	PathStyle bool   `json:"path_style"`
	// PresignTTLSeconds bounds how long a fallback signed link stays valid. It
	// only matters for a target without PublicBaseURL.
	PresignTTLSeconds int `json:"presign_ttl_seconds"`
	// MaxObjectBytes refuses to store a single object larger than this. Zero
	// means the gateway constant is used.
	MaxObjectBytes int64 `json:"max_object_bytes"`
	// RewritePaths is the gateway-wide default selector list. A channel that
	// declares its own list replaces it; a channel that declares none inherits
	// this one. Empty here means the built-in field matching applies.
	//
	// Selector syntax (one per line in the console):
	//
	//	$.data[*].url              replace the value in place
	//	$.data[*].b64 => url       store it and write the address to a sibling
	//	$.data[*].b64 => url clear store it, write the address, blank the source
	RewritePaths []string `json:"rewrite_paths"`
	// FieldNames is the key set used by the built-in matching. Empty means the
	// default list; listing names here is how a new provider gets supported
	// without a code change.
	FieldNames []string `json:"field_names"`
}

const artifactStorageSettingPrefix = "artifact_storage_setting."

var defaultArtifactStorageSetting = ArtifactStorageSetting{
	PathStyle:         true, // self-hosted MinIO is the common case
	PresignTTLSeconds: int(blobstore.DefaultPresignTTL.Seconds()),
	MaxObjectBytes:    0,
}

func init() {
	config.GlobalConfig.Register("artifact_storage_setting", &defaultArtifactStorageSetting)
}

var artifactStorageSettingOnce sync.Once

// GetArtifactStorageSetting returns the live configuration.
//
// Persisted option rows are replayed on first use because registration alone
// only covers live edits: an admin update reaches the struct by reflection, but
// nothing restores the rows at boot. Deployments that never opened the settings
// page fall back to TASK_ARTIFACT_STORE_* so an env-only setup keeps working.
func GetArtifactStorageSetting() *ArtifactStorageSetting {
	artifactStorageSettingOnce.Do(func() {
		common.OptionMapRWMutex.RLock()
		options := make(map[string]string, 12)
		for key, value := range common.OptionMap {
			if field, ok := strings.CutPrefix(key, artifactStorageSettingPrefix); ok {
				options[field] = value
			}
		}
		common.OptionMapRWMutex.RUnlock()

		if len(options) > 0 {
			if err := config.UpdateConfigFromMap(&defaultArtifactStorageSetting, options); err != nil {
				common.SysError(common.LogText("failed to load artifact storage setting: %s", err.Error()))
			}
			return
		}
		if fromEnv, ok := artifactStorageFromEnv(); ok {
			defaultArtifactStorageSetting = fromEnv
		}
	})
	return &defaultArtifactStorageSetting
}

// artifactStorageFromEnv maps the pre-existing TASK_ARTIFACT_STORE_* variables
// onto the settings struct. Those names shipped with the upstream reservation
// for S3 storage, so an operator who already wrote them into a compose file
// must not have to configure everything twice. The variables describe one
// target, which becomes the only entry of the pool.
func artifactStorageFromEnv() (ArtifactStorageSetting, bool) {
	mode := strings.TrimSpace(common.GetEnvOrDefaultString(TaskArtifactStoreModeEnv, ""))
	if !strings.EqualFold(mode, "s3") {
		return ArtifactStorageSetting{}, false
	}
	setting := defaultArtifactStorageSetting
	setting.Enabled = true
	// Path style is forced on: these variables exist for a self-hosted MinIO,
	// which only speaks path addressing. A cloud bucket reached this way would
	// need the page (or a provider-specific target) instead.
	pathStyle := true
	setting.Targets = []ArtifactStorageTarget{{
		Provider:  ArtifactStorageProviderMinIO,
		Endpoint:  strings.TrimSpace(common.GetEnvOrDefaultString(TaskArtifactStoreS3EndpointEnv, "")),
		Bucket:    strings.TrimSpace(common.GetEnvOrDefaultString(TaskArtifactStoreS3BucketEnv, "")),
		Region:    strings.TrimSpace(common.GetEnvOrDefaultString(TaskArtifactStoreS3RegionEnv, "")),
		AccessKey: common.GetEnvOrDefaultString(TaskArtifactStoreS3AccessKeyEnv, ""),
		SecretKey: common.GetEnvOrDefaultString(TaskArtifactStoreS3SecretKeyEnv, ""),
		Prefix:    strings.TrimSpace(common.GetEnvOrDefaultString(TaskArtifactStoreS3PrefixEnv, "")),
		PathStyle: &pathStyle,
	}}
	if ttl := common.GetEnvOrDefault(TaskArtifactStoreS3PresignTTLEnv, 0); ttl > 0 {
		setting.PresignTTLSeconds = ttl
	}
	return setting, true
}

// EffectiveTargets returns the ordered pool, projecting the deprecated
// single-target fields into a one-element pool when no list is configured.
func (s ArtifactStorageSetting) EffectiveTargets() []ArtifactStorageTarget {
	if !s.Enabled {
		return nil
	}
	if len(s.Targets) > 0 {
		return s.Targets
	}
	if strings.TrimSpace(s.Endpoint) == "" && strings.TrimSpace(s.Bucket) == "" {
		return nil
	}
	return []ArtifactStorageTarget{{
		Provider:  ArtifactStorageProviderS3,
		Endpoint:  s.Endpoint,
		Bucket:    s.Bucket,
		Region:    s.Region,
		AccessKey: s.AccessKey,
		SecretKey: s.SecretKey,
		Prefix:    s.Prefix,
		PathStyle: &s.PathStyle,
	}}
}

// EffectiveFieldNames is the key set the built-in matching uses. An operator can
// extend it for a provider the defaults do not know; an empty record would match
// nothing, so it falls back instead of silently disabling the feature.
func (s ArtifactStorageSetting) EffectiveFieldNames() map[string]struct{} {
	names := make(map[string]struct{}, len(s.FieldNames))
	for _, name := range s.FieldNames {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			names[trimmed] = struct{}{}
		}
	}
	return names
}

// BlobConfigs projects the pool onto the storage contract, dropping entries
// that do not validate. A target with a broken endpoint is skipped rather than
// poisoning the whole pool: the remaining targets, and eventually the original
// upstream link, still work.
func (s ArtifactStorageSetting) BlobConfigs() []blobstore.Config {
	targets := s.EffectiveTargets()
	configs := make([]blobstore.Config, 0, len(targets))
	for _, target := range targets {
		config, ok := target.BlobConfig(s.PresignTTLSeconds)
		if !ok {
			continue
		}
		configs = append(configs, config)
	}
	return configs
}

// BlobConfig validates one target into a usable storage config.
func (t ArtifactStorageTarget) BlobConfig(presignTTLSeconds int) (blobstore.Config, bool) {
	config := blobstore.Config{
		Endpoint:  strings.TrimSpace(t.Endpoint),
		Bucket:    strings.TrimSpace(t.Bucket),
		Region:    strings.TrimSpace(t.Region),
		AccessKey: strings.TrimSpace(t.AccessKey),
		SecretKey: strings.TrimSpace(t.SecretKey),
		Prefix:    strings.TrimSpace(t.Prefix),
		PathStyle: t.EffectivePathStyle(),
	}
	if config.Endpoint == "" || config.Bucket == "" {
		return blobstore.Config{}, false
	}
	if presignTTLSeconds > 0 {
		config.PresignTTL = time.Duration(presignTTLSeconds) * time.Second
	}
	normalized, err := config.Normalize()
	if err != nil {
		return blobstore.Config{}, false
	}
	return normalized, true
}
