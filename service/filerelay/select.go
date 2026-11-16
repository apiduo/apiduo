package filerelay

import (
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/endpoint"
)

var (
	// ErrNoPurposeChannel means nothing opted in. It is a configuration gap, not
	// a client mistake, and the handler answers 503 rather than 400 so an
	// operator sees it in logs as an outage of the feature.
	ErrNoPurposeChannel = errors.New("no channel serves this file purpose")
)

// Selection is the channel chosen for one upload plus the ceiling declared for
// it, so the caller does not have to re-read the configuration.
type Selection struct {
	Channel        *model.Channel
	MaxUploadBytes int64
}

// SelectChannel resolves a channel for one upload.
//
// The purpose is part of the routing name — a channel serves file-extract by
// listing "@files/upload/file-extract" — so an upload goes through the same
// candidate pipeline as a chat request: group membership, priority, weight,
// endpoint capability, and channel health all apply, and there is only one
// implementation of them to keep correct.
func SelectChannel(purpose string, group string) (*Selection, error) {
	if purpose == "" {
		return nil, fmt.Errorf("%w: purpose is empty", ErrNoPurposeChannel)
	}
	channel, err := model.GetRandomSatisfiedChannel(group, endpoint.FileUploadModel(purpose), 0, []dto.ChannelFilter{
		{Kind: dto.FilterRequestPath, RequestPath: "/v1/files"},
	})
	if err != nil {
		return nil, err
	}
	if channel == nil {
		return nil, fmt.Errorf("%w: purpose=%s group=%s", ErrNoPurposeChannel, purpose, group)
	}
	return &Selection{Channel: channel, MaxUploadBytes: model.FileUploadMaxBytes(channel)}, nil
}
