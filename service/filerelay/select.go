package filerelay

import (
	"cmp"
	"errors"
	"fmt"
	"math/rand"
	"slices"

	"github.com/QuantumNous/new-api/model"
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

// SelectChannel resolves a channel from a purpose alone.
//
// This is the whole reason /v1/files is not run through the model distributor:
// the request has no model to select on. A channel opts in to a purpose in the
// file channel tables, and that opt-in is the routing table.
func SelectChannel(purpose string, group string) (*Selection, error) {
	if purpose == "" {
		return nil, fmt.Errorf("%w: purpose is empty", ErrNoPurposeChannel)
	}
	candidates, err := model.GetFileChannelCandidates(purpose)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("%w: purpose=%s", ErrNoPurposeChannel, purpose)
	}

	matched := make([]model.FileChannelCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if group == "" || channelServesGroup(candidate.Channel, group) {
			matched = append(matched, candidate)
		}
	}
	if len(matched) == 0 {
		return nil, fmt.Errorf("%w: purpose=%s group=%s", ErrNoPurposeChannel, purpose, group)
	}

	// Highest priority wins; ties are broken by weight. Same rule the model
	// distributor uses, so an operator only has to learn it once.
	slices.SortStableFunc(matched, func(a, b model.FileChannelCandidate) int {
		return cmp.Compare(b.Channel.GetPriority(), a.Channel.GetPriority())
	})
	topPriority := matched[0].Channel.GetPriority()
	top := matched[:1]
	for _, candidate := range matched[1:] {
		if candidate.Channel.GetPriority() != topPriority {
			break
		}
		top = append(top, candidate)
	}
	return weightedPick(top), nil
}

func channelServesGroup(channel *model.Channel, group string) bool {
	for _, candidate := range channel.GetGroups() {
		if candidate == group {
			return true
		}
	}
	return false
}

func weightedPick(candidates []model.FileChannelCandidate) *Selection {
	if len(candidates) == 0 {
		return nil
	}
	picked := candidates[0]
	if len(candidates) > 1 {
		total := 0
		for _, candidate := range candidates {
			total += effectiveWeight(candidate.Channel)
		}
		if total > 0 {
			roll := rand.Intn(total)
			for _, candidate := range candidates {
				weight := effectiveWeight(candidate.Channel)
				if roll < weight {
					picked = candidate
					break
				}
				roll -= weight
			}
		}
	}
	return &Selection{Channel: picked.Channel, MaxUploadBytes: picked.MaxUploadBytes}
}

func effectiveWeight(channel *model.Channel) int {
	if weight := channel.GetWeight(); weight > 0 {
		return weight
	}
	return 1
}
