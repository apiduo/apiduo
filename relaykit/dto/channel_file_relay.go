package dto

import "slices"

// APIDUO:file-relay:BEGIN
//
// ChannelFileRelayConfig is the /v1/files opt-in for one channel.
//
// It is a channel attribute with no independent lifecycle: at most seven
// purposes per channel, deleted together with the channel, and read only when
// an upload arrives. That is why it lives in the channel's settings JSON
// instead of a table of its own.
type ChannelFileRelayConfig struct {
	Purposes []string `json:"purposes,omitempty"`
	// MaxUploadBytes overrides the provider default for this channel. Zero keeps
	// the backend default.
	MaxUploadBytes int64 `json:"max_upload_bytes,omitempty"`
}

// ServesFilePurpose reports whether the channel opted into relaying purpose.
func (c *ChannelFileRelayConfig) ServesFilePurpose(purpose string) bool {
	if c == nil || purpose == "" {
		return false
	}
	return slices.Contains(c.Purposes, purpose)
}

// APIDUO:file-relay:END
