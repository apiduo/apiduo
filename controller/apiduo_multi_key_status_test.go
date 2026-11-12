package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

const (
	remapKeyA = "sk-remap-aaaaaaaaaaaaaaaaaaaa"
	remapKeyB = "sk-remap-bbbbbbbbbbbbbbbbbbbb"
	remapKeyC = "sk-remap-cccccccccccccccccccc"
)

func remapChannel(keys string, status map[int]int) *model.Channel {
	channel := &model.Channel{Key: keys}
	channel.ChannelInfo.IsMultiKey = true
	channel.ChannelInfo.MultiKeyStatusList = status
	return channel
}

func assertStatus(t *testing.T, got map[int]int, want map[int]int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("status list size = %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for index, status := range want {
		if got[index] != status {
			t.Fatalf("status[%d] = %d, want %d (full: %v)", index, got[index], status, got)
		}
	}
}

// The reported bug: disabling slot 1 (key B) and then reversing the list left the
// marker on slot 1, which now holds key C.
func TestRemapMultiKeyStatusFollowsKeyAcrossReorder(t *testing.T) {
	origin := remapChannel(remapKeyA+"\n"+remapKeyB+"\n"+remapKeyC,
		map[int]int{1: common.ChannelStatusManuallyDisabled})
	updated := remapChannel(remapKeyB+"\n"+remapKeyC+"\n"+remapKeyA, nil)

	remapMultiKeyStatusAfterKeyEdit(origin, updated)

	assertStatus(t, updated.ChannelInfo.MultiKeyStatusList, map[int]int{0: common.ChannelStatusManuallyDisabled})
}

func TestRemapMultiKeyStatusFollowsKeyAfterInsertion(t *testing.T) {
	origin := remapChannel(remapKeyA+"\n"+remapKeyB,
		map[int]int{1: common.ChannelStatusManuallyDisabled})
	updated := remapChannel("sk-remap-newkey\n"+remapKeyA+"\n"+remapKeyB, nil)

	remapMultiKeyStatusAfterKeyEdit(origin, updated)

	assertStatus(t, updated.ChannelInfo.MultiKeyStatusList, map[int]int{2: common.ChannelStatusManuallyDisabled})
}

// A marker for a key that no longer exists cannot be attached to any visible
// line, so it must disappear rather than land on a neighbour.
func TestRemapMultiKeyStatusDropsMarkerForRemovedKey(t *testing.T) {
	origin := remapChannel(remapKeyA+"\n"+remapKeyB+"\n"+remapKeyC,
		map[int]int{1: common.ChannelStatusManuallyDisabled})
	updated := remapChannel(remapKeyA+"\n"+remapKeyC, nil)

	remapMultiKeyStatusAfterKeyEdit(origin, updated)

	assertStatus(t, updated.ChannelInfo.MultiKeyStatusList, map[int]int{})
}

func TestRemapMultiKeyStatusCarriesReasonAndTime(t *testing.T) {
	origin := remapChannel(remapKeyA+"\n"+remapKeyB,
		map[int]int{0: common.ChannelStatusManuallyDisabled})
	origin.ChannelInfo.MultiKeyDisabledReason = map[int]string{0: "401 from upstream"}
	origin.ChannelInfo.MultiKeyDisabledTime = map[int]int64{0: 1794400000}
	updated := remapChannel(remapKeyB+"\n"+remapKeyA, nil)

	remapMultiKeyStatusAfterKeyEdit(origin, updated)

	if updated.ChannelInfo.MultiKeyDisabledReason[1] != "401 from upstream" {
		t.Fatalf("reason not moved: %v", updated.ChannelInfo.MultiKeyDisabledReason)
	}
	if updated.ChannelInfo.MultiKeyDisabledTime[1] != 1794400000 {
		t.Fatalf("time not moved: %v", updated.ChannelInfo.MultiKeyDisabledTime)
	}
	if _, stale := updated.ChannelInfo.MultiKeyDisabledReason[0]; stale {
		t.Fatalf("stale reason left at old index: %v", updated.ChannelInfo.MultiKeyDisabledReason)
	}
}

func TestRemapMultiKeyStatusNoopWhenKeyUnchanged(t *testing.T) {
	keys := remapKeyA + "\n" + remapKeyB
	origin := remapChannel(keys, map[int]int{1: common.ChannelStatusManuallyDisabled})
	updated := remapChannel(keys, nil)

	remapMultiKeyStatusAfterKeyEdit(origin, updated)

	if len(updated.ChannelInfo.MultiKeyStatusList) != 0 {
		t.Fatalf("unchanged key list must keep the stored order untouched: %v",
			updated.ChannelInfo.MultiKeyStatusList)
	}
}

func TestRemapMultiKeyStatusNoopWhenNothingDisabled(t *testing.T) {
	origin := remapChannel(remapKeyA+"\n"+remapKeyB, nil)
	updated := remapChannel(remapKeyB+"\n"+remapKeyA, nil)

	remapMultiKeyStatusAfterKeyEdit(origin, updated)

	if updated.ChannelInfo.MultiKeyStatusList != nil {
		t.Fatalf("no markers to remap, got %v", updated.ChannelInfo.MultiKeyStatusList)
	}
}

func TestRemapMultiKeyStatusIgnoresSingleKeyChannels(t *testing.T) {
	origin := &model.Channel{Key: remapKeyA}
	origin.ChannelInfo.MultiKeyStatusList = map[int]int{0: common.ChannelStatusManuallyDisabled}
	updated := &model.Channel{Key: remapKeyB}

	remapMultiKeyStatusAfterKeyEdit(origin, updated)

	if updated.ChannelInfo.MultiKeyStatusList != nil {
		t.Fatalf("single-key channel must not gain a status list: %v", updated.ChannelInfo.MultiKeyStatusList)
	}
}
