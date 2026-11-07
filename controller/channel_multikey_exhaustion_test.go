package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// The admin action path has its own exhaustion check, separate from the one the
// relay uses when a key fails. Both have to agree: the channel goes down when no
// key can serve, and only then.
func TestMultiKeyChannelDisableAndRestoreByAdminActions(t *testing.T) {
	channel := &model.Channel{Key: "sk-a\nsk-b"}
	channel.ChannelInfo.IsMultiKey = true
	channel.ChannelInfo.MultiKeySize = 2
	channel.ChannelInfo.MultiKeyStatusList = map[int]int{0: common.ChannelStatusAutoDisabled}
	channel.Status = common.ChannelStatusEnabled

	if disableMultiKeyChannelIfUnavailable(channel) {
		t.Fatal("channel disabled while one key could still serve")
	}
	if channel.Status != common.ChannelStatusEnabled {
		t.Fatalf("status = %d, want enabled", channel.Status)
	}

	channel.ChannelInfo.MultiKeyStatusList[1] = common.ChannelStatusAutoDisabled
	if !disableMultiKeyChannelIfUnavailable(channel) {
		t.Fatal("channel stayed up with no usable key")
	}
	if channel.Status != common.ChannelStatusManuallyDisabled {
		t.Fatalf("status = %d, want manually disabled", channel.Status)
	}
	if reason := channel.GetOtherInfo()["status_reason"]; reason != model.ChannelStatusReasonAllKeysDisabled {
		t.Fatalf("status_reason = %v, want %q", reason, model.ChannelStatusReasonAllKeysDisabled)
	}

	// Restoring one key brings it back, because the disable was machine-made.
	delete(channel.ChannelInfo.MultiKeyStatusList, 1)
	restoreMultiKeyChannelIfAvailable(channel)
	if channel.Status != common.ChannelStatusEnabled {
		t.Fatalf("status = %d after restoring a key, want enabled", channel.Status)
	}
	if reason := channel.GetOtherInfo()["status_reason"]; reason != "" {
		t.Fatalf("status_reason still set: %v", reason)
	}
}

// A channel an operator disabled by hand must not be revived by a key edit.
func TestRestoreLeavesAManuallyDisabledChannelAlone(t *testing.T) {
	channel := &model.Channel{Key: "sk-a"}
	channel.ChannelInfo.IsMultiKey = true
	channel.ChannelInfo.MultiKeySize = 1
	channel.Status = common.ChannelStatusManuallyDisabled
	info := channel.GetOtherInfo()
	info["status_reason"] = "operator took it offline"
	channel.SetOtherInfo(info)

	restoreMultiKeyChannelIfAvailable(channel)

	if channel.Status != common.ChannelStatusManuallyDisabled {
		t.Fatalf("status = %d, a manual disable must survive", channel.Status)
	}
}
