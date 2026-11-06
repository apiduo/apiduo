package dto

import "strings"

// APIDUO:artifact-storage:BEGIN
//
// ChannelArtifactStorageConfig is the per-channel opt-in for keeping generated
// artifacts in an object store instead of only at the upstream.
//
// It is a channel attribute with no independent lifecycle, so it lives in the
// channel's settings JSON rather than a table of its own. Three states are
// meaningful and they are deliberately not collapsed into one boolean:
//
//	absent / Enabled=false   -> keep proxying the upstream (the old behaviour)
//	Enabled=true, no Endpoint -> inherit the gateway-wide object storage
//	Enabled=true, Endpoint set -> use this channel's own object storage
//
// The middle state exists so an operator can turn storage on for a channel
// without restating the credentials that already live in the global settings.
type ChannelArtifactStorageConfig struct {
	// Enabled is the checkbox an operator ticks to keep this channel's
	// artifacts. Leaving it clear is not an error: the channel simply stays on
	// the upstream proxy path.
	Enabled bool `json:"enabled,omitempty"`
	// Provider labels the target and picks addressing defaults. Empty is
	// treated as a generic S3 endpoint.
	Provider string `json:"provider,omitempty"`
	// Endpoint is the override switch. When it is empty every field below is
	// ignored and the global settings apply, which keeps a half-filled form
	// from pointing a channel at a nonexistent bucket.
	Endpoint  string `json:"endpoint,omitempty"`
	Bucket    string `json:"bucket,omitempty"`
	Region    string `json:"region,omitempty"`
	AccessKey string `json:"access_key,omitempty"`
	SecretKey string `json:"secret_key,omitempty"`
	Prefix    string `json:"prefix,omitempty"`
	// PathStyle defaults to the global value. It is a pointer so an explicit
	// false for a virtual-host endpoint survives a round trip.
	PathStyle *bool `json:"path_style,omitempty"`
	// PresignTTLSeconds of zero inherits the global value.
	PresignTTLSeconds int `json:"presign_ttl_seconds,omitempty"`
	// PublicBaseURL overrides the global public prefix for this channel, for the
	// case where its bucket is served from a different CDN host.
	PublicBaseURL string `json:"public_base_url,omitempty"`
	// RewritePaths lists the JSONPath selectors used to locate artifacts in this
	// channel's responses.
	//
	// It lives on the channel because response shapes are provider-specific: a
	// Gemini body nests bytes under candidates[].content.parts[].inlineData,
	// while an OpenAI image body puts a link at data[].url. A gateway-wide rule
	// set could only ever be the union of every shape, which is how the wrong
	// field gets rewritten.
	RewritePaths []string `json:"rewrite_paths,omitempty"`
}

// IsEnabled reports whether the channel opted into artifact storage at all.
func (c *ChannelArtifactStorageConfig) IsEnabled() bool {
	return c != nil && c.Enabled
}

// HasOverride reports whether the channel supplies its own endpoint. A config
// with Enabled set and no endpoint is the inheritance case, not an override.
func (c *ChannelArtifactStorageConfig) HasOverride() bool {
	return c != nil && c.Enabled && strings.TrimSpace(c.Endpoint) != ""
}

// APIDUO:artifact-storage:END
