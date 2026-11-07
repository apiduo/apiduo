package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
)

// "Pool exhausted" has exactly one meaning now: no key can serve. That case must
// disable the channel — a channel with zero usable credentials is not degraded,
// it is broken, and leaving it enabled makes every request pay a failed attempt
// before the fallback.
func TestChannelIsDisabledWhenTheLastKeyDies(t *testing.T) {
	keys := matrixKeys(3)
	channel := poolChannel(keys, 2, nil)
	channel.Status = common.ChannelStatusEnabled

	for i, key := range keys {
		handlerMultiKeyUpdate(channel, key, common.ChannelStatusAutoDisabled, "upstream 401")
		if i < len(keys)-1 && channel.Status != common.ChannelStatusEnabled {
			t.Fatalf("channel disabled after only %d of %d keys died", i+1, len(keys))
		}
	}

	if channel.Status != common.ChannelStatusAutoDisabled {
		t.Fatalf("status = %d, want %d (auto disabled)", channel.Status, common.ChannelStatusAutoDisabled)
	}
	if reason := channel.GetOtherInfo()["status_reason"]; reason != ChannelStatusReasonAllKeysDisabled {
		t.Fatalf("status_reason = %v, want %q", reason, ChannelStatusReasonAllKeysDisabled)
	}
	if _, _, apiErr := channel.GetNextEnabledKey(); apiErr == nil {
		t.Fatal("a channel with no usable key must refuse to select one")
	}
}

// The other side of the boundary, spelled out because it is the part that is
// easy to get wrong: a window that cannot be filled is not an outage. Three keys
// with a batch of five still serves three.
func TestChannelKeepsServingWhileAnyKeyRemains(t *testing.T) {
	keys := matrixKeys(3)
	channel := poolChannel(keys, 5, nil)
	channel.Status = common.ChannelStatusEnabled

	for i, key := range keys[:2] {
		handlerMultiKeyUpdate(channel, key, common.ChannelStatusAutoDisabled, "upstream 401")
		if channel.Status != common.ChannelStatusEnabled {
			t.Fatalf("channel disabled with %d key(s) still usable", len(keys)-i-1)
		}
	}

	// One key left, window of five: still serving, still enabled.
	for range 20 {
		if _, _, apiErr := channel.GetNextEnabledKey(); apiErr != nil {
			t.Fatalf("channel refused to serve with one key left: %v", apiErr)
		}
	}

	handlerMultiKeyUpdate(channel, keys[2], common.ChannelStatusAutoDisabled, "upstream 401")
	if channel.Status != common.ChannelStatusAutoDisabled {
		t.Fatalf("last key died but status = %d", channel.Status)
	}
}

// Re-enabling one key has to bring the channel back, otherwise an operator who
// tops up a credential still cannot use the channel without editing it.
func TestReEnablingAKeyRestoresTheChannel(t *testing.T) {
	keys := matrixKeys(2)
	channel := poolChannel(keys, 1, nil)
	channel.Status = common.ChannelStatusEnabled

	for _, key := range keys {
		handlerMultiKeyUpdate(channel, key, common.ChannelStatusAutoDisabled, "upstream 401")
	}
	if channel.Status != common.ChannelStatusAutoDisabled {
		t.Fatalf("precondition: status = %d", channel.Status)
	}

	handlerMultiKeyUpdate(channel, keys[1], common.ChannelStatusEnabled, "")
	if channel.Status != common.ChannelStatusEnabled {
		t.Fatalf("status = %d after re-enabling a key, want enabled", channel.Status)
	}
}
