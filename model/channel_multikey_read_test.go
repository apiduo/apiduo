package model

import (
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
)

func fingerprintChannel(keys []string) *Channel {
	channel := &Channel{Key: strings.Join(keys, "\n")}
	channel.ChannelInfo.IsMultiKey = true
	channel.ChannelInfo.MultiKeySize = len(keys)
	return channel
}

// The recorded index is only a hint. Editing the key list must not move a task
// onto a different credential, so the fingerprint decides.
func TestGetKeyByFingerprintFastPathWhenListUnchanged(t *testing.T) {
	keys := []string{"k0", "k1", "k2", "k3"}
	channel := fingerprintChannel(keys)

	key, index := channel.GetKeyByFingerprint(2, MultiKeyFingerprint("k2"))
	if key != "k2" || index != 2 {
		t.Fatalf("got %q index %d, want k2 at 2", key, index)
	}
}

func TestGetKeyByFingerprintSurvivesInsertionAbove(t *testing.T) {
	// The task was submitted when k2 sat at index 2; someone then added a line
	// at the top, so k2 is now at index 4 and every recorded index is wrong.
	keys := []string{"new-a", "new-b", "k0", "k1", "k2", "k3"}
	channel := fingerprintChannel(keys)

	key, index := channel.GetKeyByFingerprint(2, MultiKeyFingerprint("k2"))
	if key != "k2" {
		t.Fatalf("insertion above moved the task onto %q", key)
	}
	if index != 4 {
		t.Fatalf("got index %d, want the key's current position 4", index)
	}
}

func TestGetKeyByFingerprintSurvivesDeletionAbove(t *testing.T) {
	keys := []string{"k1", "k2", "k3"}
	channel := fingerprintChannel(keys)

	// k2 was at index 2 before k0 was removed.
	key, index := channel.GetKeyByFingerprint(2, MultiKeyFingerprint("k2"))
	if key != "k2" || index != 1 {
		t.Fatalf("got %q index %d, want k2 at 1", key, index)
	}
}

func TestGetKeyByFingerprintFallsBackWhenKeyRemoved(t *testing.T) {
	keys := []string{"k0", "k1", "k3"}
	channel := fingerprintChannel(keys)

	// k2 is gone: the task cannot be served by its own credential any more.
	key, index := channel.GetKeyByFingerprint(2, MultiKeyFingerprint("k2"))
	if key == "k2" {
		t.Fatal("returned a key that no longer exists")
	}
	if key != "k0" || index != 0 {
		t.Fatalf("got %q index %d, want the first usable fallback", key, index)
	}
}

func TestGetKeyByFingerprintFallsBackWhenKeyContentReplaced(t *testing.T) {
	// Same position, different credential: the recorded task belongs to the old
	// key, which no longer exists.
	keys := []string{"k0", "rotated", "k2"}
	channel := fingerprintChannel(keys)

	key, _ := channel.GetKeyByFingerprint(1, MultiKeyFingerprint("k1"))
	if key == "rotated" {
		t.Fatal("matched a different credential that happens to occupy the old position")
	}
}

func TestGetKeyByFingerprintFallsBackWhenRecordedKeyDisabled(t *testing.T) {
	keys := []string{"k0", "k1", "k2"}
	channel := fingerprintChannel(keys)
	channel.ChannelInfo.MultiKeyStatusList = map[int]int{1: common.ChannelStatusAutoDisabled}

	key, _ := channel.GetKeyByFingerprint(1, MultiKeyFingerprint("k1"))
	if key == "k1" {
		t.Fatal("returned a disabled key")
	}
}

func TestGetKeyByFingerprintSkipsDisabledWhileScanning(t *testing.T) {
	keys := []string{"k0", "k1", "k2", "k3"}
	channel := fingerprintChannel(keys)
	// k0 and k1 are unusable, k2 is the task's key and still fine.
	channel.ChannelInfo.MultiKeyStatusList = map[int]int{
		0: common.ChannelStatusAutoDisabled,
		1: common.ChannelStatusManuallyDisabled,
	}

	key, index := channel.GetKeyByFingerprint(99, MultiKeyFingerprint("k2"))
	if key != "k2" || index != 2 {
		t.Fatalf("got %q index %d, want k2 at 2", key, index)
	}
}

func TestGetKeyByFingerprintHandlesDuplicateKeys(t *testing.T) {
	keys := []string{"dup", "k1", "dup"}
	channel := fingerprintChannel(keys)

	// Either copy is the same credential, so the first match is correct.
	key, index := channel.GetKeyByFingerprint(2, MultiKeyFingerprint("dup"))
	if key != "dup" {
		t.Fatalf("got %q, want the duplicated key", key)
	}
	if index != 0 && index != 2 {
		t.Fatalf("got index %d, want one of the duplicate positions", index)
	}
}

func TestGetKeyByFingerprintLegacyTaskWithoutFingerprint(t *testing.T) {
	keys := []string{"k0", "k1"}
	channel := fingerprintChannel(keys)

	// Tasks created before the field existed recorded nothing.
	key, index := channel.GetKeyByFingerprint(0, "")
	if key != "k0" || index != 0 {
		t.Fatalf("got %q index %d, want the first usable key", key, index)
	}
}

func TestGetKeyByFingerprintSingleKeyChannel(t *testing.T) {
	channel := &Channel{Key: "single"}
	if key, index := channel.GetKeyByFingerprint(7, "whatever"); key != "single" || index != 0 {
		t.Fatalf("got %q index %d, want single at 0", key, index)
	}
}

func TestGetKeyByFingerprintAllDisabledReturnsRawField(t *testing.T) {
	channel := fingerprintChannel([]string{"k0", "k1"})
	channel.ChannelInfo.MultiKeyStatusList = map[int]int{
		0: common.ChannelStatusAutoDisabled,
		1: common.ChannelStatusAutoDisabled,
	}

	if key, _ := channel.GetKeyByFingerprint(0, MultiKeyFingerprint("k0")); key != channel.Key {
		t.Fatalf("got %q, want the raw key field as a last resort", key)
	}
}

func TestGetKeyByFingerprintIgnoresBlankLines(t *testing.T) {
	// GetKeys trims leading newlines, so the blanks have to sit in the middle to
	// stay in the list: ["x", "", "  ", "k2", "k3"].
	keys := []string{"x", "", "  ", "k2", "k3"}
	channel := fingerprintChannel(keys)

	key, index := channel.GetKeyByFingerprint(1, "")
	if key != "x" || index != 0 {
		t.Fatalf("got %q index %d, want the first non-blank key x at 0", key, index)
	}
	key, index = channel.GetKeyByFingerprint(3, MultiKeyFingerprint("k2"))
	if key != "k2" || index != 3 {
		t.Fatalf("got %q index %d, want k2 at 3", key, index)
	}
}

func TestGetKeyByFingerprintDoesNotTouchPollingCursor(t *testing.T) {
	previous := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() { common.MemoryCacheEnabled = previous })

	channel := newPollingTestChannel(9300, 3)
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

	if _, _, apiErr := channel.GetNextEnabledKey(); apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}
	before := channel.ChannelInfo.MultiKeyPollingIndex

	for range 20 {
		if key, _ := channel.GetKeyByFingerprint(1, MultiKeyFingerprint("sk-poll-1")); key != "sk-poll-1" {
			t.Fatalf("got %q, want sk-poll-1", key)
		}
	}
	if after := channel.ChannelInfo.MultiKeyPollingIndex; after != before {
		t.Fatalf("read-only lookup moved the cursor from %d to %d", before, after)
	}
}

func TestGetKeyByFingerprintConcurrentLookups(t *testing.T) {
	keys := make([]string, 64)
	for i := range keys {
		keys[i] = "sk-conc-" + strconv.Itoa(i)
	}
	channel := fingerprintChannel(keys)
	target := MultiKeyFingerprint("sk-conc-37")

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				key, index := channel.GetKeyByFingerprint(0, target)
				if key != "sk-conc-37" || index != 37 {
					t.Errorf("concurrent lookup got %q index %d", key, index)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// The fingerprint can only find a key that still exists. The retained copy is
// what keeps a task reachable after the operator edits the key list.
func TestGetTaskKeyUsesRetainedKeyAfterChannelKeyWasRemoved(t *testing.T) {
	channel := fingerprintChannel([]string{"k1", "k2"})
	task := &Task{}
	task.PrivateData.UpstreamKey = "sk-removed"
	task.PrivateData.UpstreamKeyAt = time.Now().Unix()
	task.PrivateData.MultiKeyFingerprint = MultiKeyFingerprint("sk-removed")

	key, _ := GetTaskKey(channel, task)
	if key != "sk-removed" {
		t.Fatalf("got %q, want the retained credential", key)
	}
}

func TestGetTaskKeyIgnoresRetainedKeyAfterExpiry(t *testing.T) {
	channel := fingerprintChannel([]string{"k0", "k1", "k2"})
	task := &Task{}
	task.PrivateData.UpstreamKey = "sk-stale"
	task.PrivateData.UpstreamKeyAt = time.Now().Unix() - taskKeyRetentionSeconds - 1
	task.PrivateData.MultiKeyFingerprint = MultiKeyFingerprint("k1")

	key, index := GetTaskKey(channel, task)
	if key != "k1" || index != 1 {
		t.Fatalf("got %q index %d, want the fingerprint result k1 at 1", key, index)
	}
}

func TestGetTaskKeyRetainedKeySurvivesDisable(t *testing.T) {
	channel := fingerprintChannel([]string{"k0", "k1"})
	channel.ChannelInfo.MultiKeyStatusList = map[int]int{1: common.ChannelStatusAutoDisabled}
	task := &Task{}
	task.PrivateData.UpstreamKey = "k1"
	task.PrivateData.UpstreamKeyAt = time.Now().Unix()

	// A key flagged for billing still owns the task; refusing it here would turn
	// a billing problem into an orphaned task.
	if key, _ := GetTaskKey(channel, task); key != "k1" {
		t.Fatalf("got %q, want the retained key even while disabled", key)
	}
}

func TestGetTaskKeyFallsBackWhenNoRetainedKey(t *testing.T) {
	channel := fingerprintChannel([]string{"k0", "k1"})
	task := &Task{}
	task.PrivateData.MultiKeyFingerprint = MultiKeyFingerprint("k1")

	if key, index := GetTaskKey(channel, task); key != "k1" || index != 1 {
		t.Fatalf("got %q index %d, want k1 at 1", key, index)
	}
}

func TestGetTaskKeyHandlesNilInputs(t *testing.T) {
	if key, index := GetTaskKey(nil, nil); key != "" || index != -1 {
		t.Fatalf("nil task must return empty, got %q index %d", key, index)
	}

	// A retained key still works when the channel row itself is gone.
	task := &Task{}
	task.PrivateData.UpstreamKey = "sk-orphan"
	task.PrivateData.UpstreamKeyAt = time.Now().Unix()
	if key, _ := GetTaskKey(nil, task); key != "sk-orphan" {
		t.Fatalf("got %q, want the retained key without a channel", key)
	}
}

func TestGetTaskKeyRetentionBoundary(t *testing.T) {
	channel := fingerprintChannel([]string{"k0", "k1"})
	task := &Task{}
	task.PrivateData.UpstreamKey = "sk-boundary"
	task.PrivateData.UpstreamKeyAt = time.Now().Unix() - taskKeyRetentionSeconds
	task.PrivateData.MultiKeyFingerprint = MultiKeyFingerprint("k1")

	// Exactly at the limit still counts as retained.
	if key, _ := GetTaskKey(channel, task); key != "sk-boundary" {
		t.Fatalf("got %q, want the retained key at the boundary", key)
	}
}
