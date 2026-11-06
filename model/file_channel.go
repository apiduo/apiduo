package model

import (
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// APIDUO:file-relay:BEGIN
//
// The file relay opt-in is a channel attribute, so it lives in the channel's
// other_settings JSON under file_relay instead of a dedicated table.
//
// The scale decides this: at most seven purposes per channel, a few dozen
// channels, read only when an upload arrives, and deleted together with the
// channel. A join table would add a migration, an API, and cascade cleanup to
// model something the channel row already owns.
type FileChannelCandidate struct {
	Channel        *Channel
	MaxUploadBytes int64
}

// GetFileChannelCandidates returns the enabled channels that declared purpose.
func GetFileChannelCandidates(purpose string) ([]FileChannelCandidate, error) {
	if purpose == "" {
		return nil, errors.New("file purpose is required")
	}
	var channels []*Channel
	if err := DB.Where("status = ?", common.ChannelStatusEnabled).Find(&channels).Error; err != nil {
		return nil, err
	}
	candidates := make([]FileChannelCandidate, 0, len(channels))
	for _, channel := range channels {
		// A substring check keeps the common case — most channels do not serve
		// files — from paying for a JSON parse on every upload.
		if !strings.Contains(channel.OtherSettings, "file_relay") {
			continue
		}
		fileRelay := channel.GetOtherSettings().FileRelay
		if !fileRelay.ServesFilePurpose(purpose) {
			continue
		}
		candidates = append(candidates, FileChannelCandidate{
			Channel:        channel,
			MaxUploadBytes: fileRelay.MaxUploadBytes,
		})
	}
	return candidates, nil
}

// APIDUO:file-relay:END
