package model

import (
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
)

func newPollingTestChannel(id int, keyCount int) *Channel {
	keys := make([]string, keyCount)
	for i := range keys {
		keys[i] = "sk-poll-" + strconv.Itoa(i)
	}
	channel := &Channel{Id: id, Key: joinKeys(keys)}
	channel.ChannelInfo.IsMultiKey = true
	channel.ChannelInfo.MultiKeySize = keyCount
	channel.ChannelInfo.MultiKeyMode = constant.MultiKeyModePolling
	channel.Keys = keys
	return channel
}

func joinKeys(keys []string) string {
	out := ""
	for i, key := range keys {
		if i > 0 {
			out += "\n"
		}
		out += key
	}
	return out
}

// The polling cursor lives in the cached ChannelInfo. Callers that hold a
// database copy of the channel (relay_task locks the origin channel that way)
// read the cursor from the cache but used to write it back to their own copy,
// so the cursor never advanced and every such request got the same key.
func TestPollingAdvancesWhenCallerHoldsADatabaseCopy(t *testing.T) {
	previous := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() { common.MemoryCacheEnabled = previous })

	cached := newPollingTestChannel(9200, 4)
	channelSyncLock.Lock()
	if channelsIDM == nil {
		channelsIDM = make(map[int]*Channel)
	}
	channelsIDM[cached.Id] = cached
	channelSyncLock.Unlock()
	t.Cleanup(func() {
		channelSyncLock.Lock()
		delete(channelsIDM, cached.Id)
		channelSyncLock.Unlock()
	})

	// A separate object, the way model.GetChannelById hands one out.
	callerCopy := newPollingTestChannel(cached.Id, 4)

	const rounds = 8
	seen := make(map[int]int, 4)
	for range rounds {
		_, index, apiErr := callerCopy.GetNextEnabledKey()
		if apiErr != nil {
			t.Fatalf("unexpected error: %v", apiErr)
		}
		seen[index]++
	}

	if len(seen) != 4 {
		t.Fatalf("polling touched %d of 4 keys in %d rounds: %v", len(seen), rounds, seen)
	}
	for index, count := range seen {
		if count != rounds/4 {
			t.Fatalf("index %d got %d of %d rounds: %v", index, count, rounds, seen)
		}
	}
}

func TestPollingAdvancesInOrder(t *testing.T) {
	previous := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() { common.MemoryCacheEnabled = previous })

	channel := newPollingTestChannel(9201, 4)
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

	want := []int{0, 1, 2, 3, 0, 1, 2, 3}
	for round, expected := range want {
		_, index, apiErr := channel.GetNextEnabledKey()
		if apiErr != nil {
			t.Fatalf("round %d: unexpected error: %v", round, apiErr)
		}
		if index != expected {
			t.Fatalf("round %d: got index %d, want %d (polling must stay in order)", round, index, expected)
		}
	}
}

func TestPollingSkipsDisabledKeysWithoutStalling(t *testing.T) {
	previous := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() { common.MemoryCacheEnabled = previous })

	channel := newPollingTestChannel(9202, 4)
	channel.ChannelInfo.MultiKeyStatusList = map[int]int{1: common.ChannelStatusAutoDisabled}
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

	counts := make(map[int]int, 3)
	for range 9 {
		_, index, apiErr := channel.GetNextEnabledKey()
		if apiErr != nil {
			t.Fatalf("unexpected error: %v", apiErr)
		}
		if index == 1 {
			t.Fatal("polling returned a disabled key")
		}
		counts[index]++
	}
	for _, index := range []int{0, 2, 3} {
		if counts[index] != 3 {
			t.Fatalf("index %d got %d of 9 rounds: %v", index, counts[index], counts)
		}
	}
}
