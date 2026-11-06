package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/blobstore"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/system_setting"
)

// ResolvedArtifactStorage is one usable destination in the fallback chain.
type ResolvedArtifactStorage struct {
	Backend blobstore.Backend
	// Source is "channel" or "global". It is reported in logs, never used for
	// control flow.
	Source string
	// TargetKey identifies the destination across a stored reference.
	TargetKey string
	// PublicBaseURL, when set, is the permanent address prefix the bucket is
	// reachable at. Without it a stored object only has a presigned URL, which
	// expires and would break the response it was written into.
	PublicBaseURL string
	// MaxObjectBytes caps one stored object. Zero means "use the gateway
	// constant".
	MaxObjectBytes int64
	// keyPrefix is the bucket prefix from the target config. Backend calls apply
	// it themselves; a public URL is built here and has to include it too.
	keyPrefix string
	// healthKey identifies this target's failure record.
	healthKey blobBackendKey
}

// ArtifactStorageChain is the ordered list of destinations a write tries, plus
// the rules that decide which response fields are worth storing.
type ArtifactStorageChain struct {
	Targets []*ResolvedArtifactStorage
	// RewritePaths is the channel's selector list, falling back to the
	// gateway-wide default. Empty means the built-in field matching applies.
	RewritePaths []string
	// FieldNames is the key set used when RewritePaths is empty.
	FieldNames map[string]struct{}
}

// Empty reports whether nothing is configured for this channel.
func (c *ArtifactStorageChain) Empty() bool {
	return c == nil || len(c.Targets) == 0
}

// StoredArtifact is the result of a successful write.
type StoredArtifact struct {
	// Key is the object key inside the bucket.
	Key string
	// URL is the address to write back into the response. It is a permanent
	// link when the target declares PublicBaseURL, otherwise a presigned URL.
	URL string
	// Size is the number of stored bytes.
	Size int64
	// Backend identifies which target accepted the write.
	Backend string
}

const (
	ArtifactStorageSourceChannel = "channel"
	ArtifactStorageSourceGlobal  = "global"

	// maxCachedBlobBackends bounds the client cache. Each entry holds an HTTP
	// connection pool, so the number is small on purpose.
	maxCachedBlobBackends = 64
)

// blobBackendKey identifies a target by every field that changes the signature
// or the address, and doubles as the identity of its failure record.
//
// The secret is hashed rather than stored: correcting a wrong secret must start
// a clean slate, but the plaintext does not belong in a key that may end up in
// a log line.
type blobBackendKey struct {
	endpoint      string
	bucket        string
	region        string
	accessKey     string
	secretKeyHash string
	pathStyle     bool
}

var (
	blobBackendMu    sync.Mutex
	blobBackendCache = make(map[blobBackendKey]blobstore.Backend, 4)
)

// ResolveArtifactStorageChainForChannelID is the entry point for a request that
// only carries the channel id.
func ResolveArtifactStorageChainForChannelID(channelID int) *ArtifactStorageChain {
	if channelID <= 0 {
		return nil
	}
	channel, err := model.CacheGetChannel(channelID)
	if err != nil || channel == nil {
		return nil
	}
	return ResolveArtifactStorageChain(channel)
}

// ResolveArtifactStorageChain builds the fallback chain for one channel.
//
// A channel that never opted in returns nil even when the gateway-wide pool is
// configured: leaving the box clear means "leave the response untouched", not
// "use the default". Opting in without an endpoint is the inherit case and
// yields the global pool alone.
func ResolveArtifactStorageChain(channel *model.Channel) *ArtifactStorageChain {
	channelConfig := channelArtifactStorage(channel)
	if !channelConfig.IsEnabled() {
		return nil
	}

	global := system_setting.GetArtifactStorageSetting()
	maxObjectBytes := global.MaxObjectBytes

	targets := make([]*ResolvedArtifactStorage, 0, len(global.Targets)+1)
	seen := make(map[string]struct{}, len(global.Targets)+1)

	if channelConfig.HasOverride() {
		if resolved := resolveChannelTarget(channelConfig, global, maxObjectBytes); resolved != nil {
			seen[resolved.TargetKey] = struct{}{}
			targets = append(targets, resolved)
		}
	}

	for _, target := range global.EffectiveTargets() {
		config, ok := target.BlobConfig(global.PresignTTLSeconds)
		if !ok {
			continue
		}
		key := storageTargetKey(config)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		if resolved := newResolvedArtifactStorage(config, target.PublicBaseURL, ArtifactStorageSourceGlobal, maxObjectBytes); resolved != nil {
			targets = append(targets, resolved)
		}
	}

	if len(targets) == 0 {
		return nil
	}
	// A channel's own selectors replace the default rather than extend it: a
	// provider-specific rule set is meant to be the whole truth for that channel.
	paths := channelConfig.RewritePaths
	if len(paths) == 0 {
		paths = global.RewritePaths
	}
	return &ArtifactStorageChain{
		Targets:      targets,
		RewritePaths: paths,
		FieldNames:   global.EffectiveFieldNames(),
	}
}

// resolveChannelTarget turns the channel override into a target, inheriting
// every field it leaves blank from the first global entry.
func resolveChannelTarget(
	channelConfig *dto.ChannelArtifactStorageConfig,
	global *system_setting.ArtifactStorageSetting,
	maxObjectBytes int64,
) *ResolvedArtifactStorage {
	inherited := system_setting.ArtifactStorageTarget{}
	if candidates := global.EffectiveTargets(); len(candidates) > 0 {
		inherited = candidates[0]
	}

	target := system_setting.ArtifactStorageTarget{
		Provider:  channelConfig.Provider,
		Endpoint:  strings.TrimSpace(channelConfig.Endpoint),
		Bucket:    firstNonEmpty(channelConfig.Bucket, inherited.Bucket),
		Region:    firstNonEmpty(channelConfig.Region, inherited.Region),
		AccessKey: firstNonEmpty(channelConfig.AccessKey, inherited.AccessKey),
		SecretKey: firstNonEmpty(channelConfig.SecretKey, inherited.SecretKey),
		Prefix:    firstNonEmpty(channelConfig.Prefix, inherited.Prefix),
	}
	// Addressing style: an explicit choice wins, then the provider's own
	// default, and only an unspecified provider falls back to the global target.
	switch {
	case channelConfig.PathStyle != nil:
		target.PathStyle = channelConfig.PathStyle
	case channelConfig.Provider != "":
		target.PathStyle = nil
	default:
		target.PathStyle = inherited.PathStyle
	}

	config, ok := target.BlobConfig(global.PresignTTLSeconds)
	if !ok {
		return nil
	}
	return newResolvedArtifactStorage(
		config,
		firstNonEmpty(channelConfig.PublicBaseURL, inherited.PublicBaseURL),
		ArtifactStorageSourceChannel,
		maxObjectBytes,
	)
}

func channelArtifactStorage(channel *model.Channel) *dto.ChannelArtifactStorageConfig {
	if channel == nil {
		return nil
	}
	return channel.GetOtherSettings().ArtifactStorage
}

func newResolvedArtifactStorage(config blobstore.Config, publicBaseURL, source string, maxObjectBytes int64) *ResolvedArtifactStorage {
	backend, err := cachedBlobBackend(config)
	if err != nil || backend == nil {
		return nil
	}
	return &ResolvedArtifactStorage{
		Backend:        backend,
		Source:         source,
		TargetKey:      storageTargetKey(config),
		PublicBaseURL:  strings.TrimSuffix(strings.TrimSpace(publicBaseURL), "/"),
		MaxObjectBytes: maxObjectBytes,
		keyPrefix:      strings.Trim(config.Prefix, "/"),
		healthKey:      blobBackendKeyOf(config),
	}
}

// storageTargetKey is the stable identity of a destination.
func storageTargetKey(config blobstore.Config) string {
	return config.Endpoint + "|" + config.Bucket
}

func cachedBlobBackend(config blobstore.Config) (blobstore.Backend, error) {
	key := blobBackendKeyOf(config)

	blobBackendMu.Lock()
	defer blobBackendMu.Unlock()
	if backend, ok := blobBackendCache[key]; ok {
		return backend, nil
	}
	if len(blobBackendCache) >= maxCachedBlobBackends {
		clear(blobBackendCache)
	}
	backend, err := blobstore.NewS3(config)
	if err != nil {
		return nil, err
	}
	blobBackendCache[key] = backend
	return backend, nil
}

// blobBackendKeyOf is the identity of a client, and also of its failure record.
func blobBackendKeyOf(config blobstore.Config) blobBackendKey {
	digest := sha256.Sum256([]byte(config.SecretKey))
	return blobBackendKey{
		endpoint:      config.Endpoint,
		bucket:        config.Bucket,
		region:        config.Region,
		accessKey:     config.AccessKey,
		secretKeyHash: hex.EncodeToString(digest[:8]),
		pathStyle:     config.PathStyle,
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// PersistArtifactBytes writes one object and returns the address to hand back
// to the client.
//
// The chain is walked in order and the first success wins; a target that just
// failed is skipped until its cooldown expires, so one dead bucket cannot slow
// every response down. When every target fails the caller keeps the original
// upstream link, which is why this returns an error instead of falling back
// inside.
func PersistArtifactBytes(ctx context.Context, channelID int, objectKey, contentType string, data []byte) (*StoredArtifact, error) {
	if len(data) == 0 {
		return nil, ErrTaskArtifactStoreDisabled
	}
	chain := ResolveArtifactStorageChainForChannelID(channelID)
	if chain.Empty() {
		return nil, ErrTaskArtifactStoreDisabled
	}
	if err := blobstore.ValidateKey(objectKey); err != nil {
		return nil, err
	}

	options := blobstore.PutOptions{
		ContentType:   strings.TrimSpace(contentType),
		ContentLength: int64(len(data)),
	}

	var lastErr error
	for _, target := range chain.Targets {
		if artifactTargetCoolingDown(target.healthKey) {
			continue
		}
		object, putErr := target.Backend.Put(ctx, objectKey, bytes.NewReader(data), options)
		if putErr != nil {
			lastErr = putErr
			logArtifactTargetFailure(target.healthKey, putErr)
			continue
		}
		clearArtifactTargetFailure(target.healthKey)
		url, urlErr := target.artifactURL(ctx, objectKey)
		if urlErr != nil {
			return nil, urlErr
		}
		return &StoredArtifact{
			Key:     objectKey,
			URL:     url,
			Size:    object.Size,
			Backend: target.TargetKey,
		}, nil
	}

	if lastErr == nil {
		lastErr = ErrTaskArtifactStoreDisabled
	}
	return nil, lastErr
}

// artifactURL is the address written back into the response.
//
// A public base yields a permanent link, which is what makes a rewritten body
// stay valid. Without one the only honest address is a presigned URL: it expires,
// so the caller must be aware the link has a deadline.
func (t *ResolvedArtifactStorage) artifactURL(ctx context.Context, objectKey string) (string, error) {
	if t.PublicBaseURL != "" {
		key := strings.TrimLeft(objectKey, "/")
		if t.keyPrefix != "" {
			key = t.keyPrefix + "/" + key
		}
		return t.PublicBaseURL + "/" + key, nil
	}
	return t.Backend.PresignGet(ctx, objectKey, 0)
}
