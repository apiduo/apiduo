package dto

// APIDUO:file-relay:BEGIN
//
// ChannelFileRelayConfig is the /v1/files ceiling for one channel.
//
// The purpose list that used to live here is gone: a channel serves a purpose
// by listing "@files/upload/<purpose>" in its model list, which is the same
// place every other routable capability is declared. Keeping a second list here
// meant two places to read, and the stale one was impossible to spot.
type ChannelFileRelayConfig struct {
	// MaxUploadBytes overrides the provider default for this channel. Zero keeps
	// the backend default.
	MaxUploadBytes int64 `json:"max_upload_bytes,omitempty"`
}

// APIDUO:file-relay:END
