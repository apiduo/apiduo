package model

import (
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
)

func poolChannel(keys []string, minActive int, disabled map[int]int) *Channel {
	channel := &Channel{Key: strings.Join(keys, "\n")}
	channel.ChannelInfo.IsMultiKey = true
	channel.ChannelInfo.MultiKeySize = len(keys)
	channel.ChannelInfo.MinActiveKeys = minActive
	if len(disabled) > 0 {
		channel.ChannelInfo.MultiKeyStatusList = disabled
	}
	channel.Keys = keys
	return channel
}

// sampledIndexes runs the real rotation path and records which keys it hands
// out. Sampling the selector instead of recomputing the eligible list is the
// point: the cap has to hold for what requests actually get.
func sampledIndexes(channel *Channel, rounds int) map[int]int {
	seen := make(map[int]int, rounds)
	for range rounds {
		_, index, apiErr := channel.GetNextEnabledKey()
		if apiErr != nil {
			return nil
		}
		seen[index]++
	}
	return seen
}

func TestActiveCapLimitsRotation(t *testing.T) {
	channel := poolChannel([]string{"k0", "k1", "k2", "k3", "k4", "k5"}, 3, nil)
	channel.ChannelInfo.MultiKeyMode = constant.MultiKeyModeRandom

	seen := sampledIndexes(channel, 600)
	if seen == nil {
		t.Fatal("rotation returned an error")
	}
	if len(seen) != 3 {
		t.Fatalf("rotation touched %d keys, want 3: %v", len(seen), seen)
	}
	for index := range seen {
		if index > 2 {
			t.Fatalf("key %d served despite a cap of 3: %v", index, seen)
		}
	}
}

func TestActiveCapKeepsSurplusKeysEnabled(t *testing.T) {
	// The point of capping positionally: nothing about a spare key's state
	// changes, so no counter, filter or badge has to learn a new value.
	channel := poolChannel([]string{"k0", "k1", "k2", "k3"}, 2, nil)
	channel.ChannelInfo.MultiKeyMode = constant.MultiKeyModeRandom

	sampledIndexes(channel, 100)

	for i := range 4 {
		if status, ok := channel.ChannelInfo.MultiKeyStatusList[i]; ok {
			t.Fatalf("key %d got status %d; the cap must not write statuses", i, status)
		}
	}
}

func TestActiveCapSlidesWhenAnActiveKeyIsDisabled(t *testing.T) {
	channel := poolChannel([]string{"k0", "k1", "k2", "k3"}, 2, nil)
	channel.ChannelInfo.MultiKeyMode = constant.MultiKeyModeRandom
	channel.ChannelInfo.MultiKeyStatusList = map[int]int{0: common.ChannelStatusAutoDisabled}

	seen := sampledIndexes(channel, 400)
	if seen == nil {
		t.Fatal("rotation returned an error")
	}
	// Available keys are 1,2,3; the cap keeps the first two, so k2 takes k0's
	// slot without anyone running a refill pass.
	if len(seen) != 2 || seen[1] == 0 || seen[2] == 0 {
		t.Fatalf("expected keys 1 and 2 in rotation, got %v", seen)
	}
	if seen[0] != 0 {
		t.Fatalf("disabled key 0 served: %v", seen)
	}
	if seen[3] != 0 {
		t.Fatalf("key 3 is still beyond the cap and must stay idle: %v", seen)
	}
}

func TestActiveCapSkipsDisabledKeysInsideTheWindow(t *testing.T) {
	channel := poolChannel([]string{"k0", "k1", "k2", "k3"}, 2, map[int]int{
		1: common.ChannelStatusManuallyDisabled,
	})
	channel.ChannelInfo.MultiKeyMode = constant.MultiKeyModeRandom

	seen := sampledIndexes(channel, 400)
	if len(seen) != 2 || seen[0] == 0 || seen[2] == 0 {
		t.Fatalf("expected keys 0 and 2, got %v", seen)
	}
	if seen[1] != 0 {
		t.Fatalf("disabled key 1 served: %v", seen)
	}
}

func TestActiveCapZeroMeansNoLimit(t *testing.T) {
	channel := poolChannel([]string{"k0", "k1", "k2", "k3", "k4", "k5"}, 0, nil)
	channel.ChannelInfo.MultiKeyMode = constant.MultiKeyModeRandom

	if got := channel.ActiveKeyLimit(); got != 0 {
		t.Fatalf("limit = %d, want 0", got)
	}
	if seen := sampledIndexes(channel, 1200); len(seen) != 6 {
		t.Fatalf("uncapped rotation touched %d keys, want 6: %v", len(seen), seen)
	}
}

func TestActiveCapAboveKeyCountIsIgnored(t *testing.T) {
	channel := poolChannel([]string{"k0", "k1", "k2"}, 10, nil)
	channel.ChannelInfo.MultiKeyMode = constant.MultiKeyModeRandom

	if seen := sampledIndexes(channel, 600); len(seen) != 3 {
		t.Fatalf("a cap larger than the key list limited rotation to %d keys: %v", len(seen), seen)
	}
}

func TestActiveCapUnknownModeStillRespectsIt(t *testing.T) {
	// The unknown-mode guard spreads instead of pinning keys[0]; the cap has to
	// apply there too, or an unset mode would quietly ignore the setting.
	channel := poolChannel([]string{"k0", "k1", "k2", "k3"}, 2, nil)

	seen := sampledIndexes(channel, 400)
	if len(seen) != 2 || seen[2] != 0 || seen[3] != 0 {
		t.Fatalf("unset mode ignored the cap: %v", seen)
	}
}

func TestActiveCapAppliesToStickySelection(t *testing.T) {
	channel := poolChannel([]string{"k0", "k1", "k2", "k3", "k4", "k5"}, 2, nil)
	channel.ChannelInfo.MultiKeyMode = constant.MultiKeyModeSticky
	channel.Id = 7101

	seen := make(map[int]int, 2)
	for i := range 400 {
		_, index, apiErr := channel.GetStickyEnabledKey("token:"+strconv.Itoa(i), nil)
		if apiErr != nil {
			t.Fatalf("unexpected error: %v", apiErr)
		}
		seen[index]++
		if index > 1 {
			t.Fatalf("sticky served key %d despite a cap of 2", index)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("sticky touched %d keys, want 2: %v", len(seen), seen)
	}
}

func TestActiveCapChangeRebuildsTheStickyPlan(t *testing.T) {
	channel := poolChannel([]string{"k0", "k1", "k2", "k3"}, 4, nil)
	channel.ChannelInfo.MultiKeyMode = constant.MultiKeyModeSticky
	channel.Id = 7102

	for i := range 50 {
		if _, _, apiErr := channel.GetStickyEnabledKey("token:"+strconv.Itoa(i), nil); apiErr != nil {
			t.Fatalf("unexpected error: %v", apiErr)
		}
	}

	// Tighten the cap. Keys and statuses are unchanged, so only the limit can
	// invalidate the cached plan — if it does not, the old width keeps serving.
	channel.ChannelInfo.MinActiveKeys = 1
	for i := range 50 {
		key, index, apiErr := channel.GetStickyEnabledKey("token:"+strconv.Itoa(i), nil)
		if apiErr != nil {
			t.Fatalf("unexpected error: %v", apiErr)
		}
		if index != 0 || key != "k0" {
			t.Fatalf("cap change kept the old width: got %q at %d", key, index)
		}
	}
}

func TestActiveCapOnSingleKeyChannel(t *testing.T) {
	channel := &Channel{Key: "sk-single"}
	channel.ChannelInfo.MinActiveKeys = 1
	if got := channel.ActiveKeyLimit(); got != 0 {
		t.Fatalf("single-key channel reported a cap of %d", got)
	}
}

func withCachedChannel(t *testing.T, channel *Channel) {
	t.Helper()
	previous := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() { common.MemoryCacheEnabled = previous })

	channelSyncLock.Lock()
	if channelsIDM == nil {
		channelsIDM = make(map[int]*Channel)
	}
	channelsIDM[channel.Id] = channel
	channelSyncLock.Unlock()
	t.Cleanup(func() {
		channelSyncLock.Lock()
		delete(channelsIDM, channel.Id)
		channelSyncLock.Unlock()
	})
}

func TestActiveCapAppliesToPolling(t *testing.T) {
	// Polling walks the raw key ring on its own. Until it asked the eligible set,
	// a cap of 2 happily served all six keys — the same setting behaved
	// differently per mode, which is worse than the setting not existing.
	channel := poolChannel([]string{"k0", "k1", "k2", "k3", "k4", "k5"}, 2, nil)
	channel.ChannelInfo.MultiKeyMode = constant.MultiKeyModePolling
	channel.Id = 7301
	withCachedChannel(t, channel)

	seen := sampledIndexes(channel, 12)
	if seen == nil {
		t.Fatal("polling returned an error")
	}
	if len(seen) != 2 {
		t.Fatalf("polling served %d keys with a cap of 2: %v", len(seen), seen)
	}
	for index := range seen {
		if index > 1 {
			t.Fatalf("polling served key %d beyond the cap: %v", index, seen)
		}
	}
}

func TestActiveCapPollingWrapsWhenCursorLeavesTheWindow(t *testing.T) {
	channel := poolChannel([]string{"k0", "k1", "k2", "k3"}, 2, nil)
	channel.ChannelInfo.MultiKeyMode = constant.MultiKeyModePolling
	channel.ChannelInfo.MultiKeyPollingIndex = 3
	channel.Id = 7302
	withCachedChannel(t, channel)

	// The cursor points at a spare key. Polling must wrap to the window instead
	// of returning the spare, and must keep advancing from there.
	if _, index, apiErr := channel.GetNextEnabledKey(); apiErr != nil || index != 0 {
		t.Fatalf("want index 0 after wrapping, got %d (err %v)", index, apiErr)
	}
	if _, index, apiErr := channel.GetNextEnabledKey(); apiErr != nil || index != 1 {
		t.Fatalf("want index 1 on the next round, got %d (err %v)", index, apiErr)
	}
}

func TestTaskKeyStillResolvesOutsideTheCap(t *testing.T) {
	// A task belongs to the credential that created it. Shrinking the cap later
	// must not make that credential unreachable, or the task is orphaned.
	channel := poolChannel([]string{"k0", "k1", "k2", "k3", "k4", "k5"}, 2, nil)
	channel.Id = 7303

	key, index := channel.GetKeyByFingerprint(5, MultiKeyFingerprint("k5"))
	if key != "k5" || index != 5 {
		t.Fatalf("got %q at %d, want the bound key k5 outside the cap", key, index)
	}
}

func TestActiveCapPollingCursorStaysInsideTheWindow(t *testing.T) {
	channel := poolChannel([]string{"k0", "k1", "k2", "k3", "k4", "k5"}, 2, nil)
	channel.ChannelInfo.MultiKeyMode = constant.MultiKeyModePolling
	channel.Id = 7304
	withCachedChannel(t, channel)

	// Serve both keys of the window, then look at the cursor. Stepping by one raw
	// index would leave it at 2 — a spare — and the next scan would walk the rest
	// of the list before wrapping.
	if _, index, apiErr := channel.GetNextEnabledKey(); apiErr != nil || index != 0 {
		t.Fatalf("want index 0, got %d (err %v)", index, apiErr)
	}
	if _, index, apiErr := channel.GetNextEnabledKey(); apiErr != nil || index != 1 {
		t.Fatalf("want index 1, got %d (err %v)", index, apiErr)
	}

	channelSyncLock.Lock()
	cursor := channelsIDM[channel.Id].ChannelInfo.MultiKeyPollingIndex
	channelSyncLock.Unlock()
	if cursor != 0 {
		t.Fatalf("cursor = %d after the window wrapped, want 0 (inside the window)", cursor)
	}
}
