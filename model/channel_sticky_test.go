package model

import (
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
)

func newStickyTestChannel(keys []string, disabled map[int]int) *Channel {
	channel := &Channel{Key: strings.Join(keys, "\n")}
	channel.ChannelInfo.IsMultiKey = true
	channel.ChannelInfo.MultiKeySize = len(keys)
	channel.ChannelInfo.MultiKeyMode = constant.MultiKeyModeSticky
	if len(disabled) > 0 {
		channel.ChannelInfo.MultiKeyStatusList = disabled
	}
	return channel
}

func TestGetStickyEnabledKeySingleKeyIsUntouched(t *testing.T) {
	channel := &Channel{Key: "only-key"}
	key, index, apiErr := channel.GetStickyEnabledKey("token:1", nil)
	if apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}
	if key != "only-key" || index != 0 {
		t.Fatalf("single-key channel must return its key, got %q index %d", key, index)
	}
}

func TestGetStickyEnabledKeyIsStableForSameSeed(t *testing.T) {
	channel := newStickyTestChannel([]string{"a", "b", "c", "d"}, nil)
	first, firstIndex, apiErr := channel.GetStickyEnabledKey("token:42", nil)
	if apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}
	for range 20 {
		key, index, err := channel.GetStickyEnabledKey("token:42", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if key != first || index != firstIndex {
			t.Fatalf("same seed must map to the same key: %q/%d vs %q/%d", first, firstIndex, key, index)
		}
	}
}

func TestGetStickyEnabledKeySpreadsSeeds(t *testing.T) {
	keys := []string{"a", "b", "c", "d"}
	channel := newStickyTestChannel(keys, nil)
	seen := make(map[string]int, len(keys))
	for i := range 4000 {
		key, _, apiErr := channel.GetStickyEnabledKey("token:"+strconv.Itoa(i), nil)
		if apiErr != nil {
			t.Fatalf("unexpected error: %v", apiErr)
		}
		seen[key]++
	}
	if len(seen) != len(keys) {
		t.Fatalf("expected every key to receive traffic, got %v", seen)
	}
	for key, count := range seen {
		if count < 600 {
			t.Fatalf("key %q got %d of 4000 seeds, distribution is skewed: %v", key, count, seen)
		}
	}
}

func TestGetStickyEnabledKeySkipsTriedIndexes(t *testing.T) {
	channel := newStickyTestChannel([]string{"a", "b", "c", "d"}, nil)
	_, firstIndex, apiErr := channel.GetStickyEnabledKey("token:7", nil)
	if apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}
	_, secondIndex, apiErr := channel.GetStickyEnabledKey("token:7", map[int]struct{}{firstIndex: {}})
	if apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}
	if secondIndex == firstIndex {
		t.Fatalf("retry must move to another key, still at index %d", secondIndex)
	}
}

func TestGetStickyEnabledKeyIgnoresSkipWhenEveryKeyTried(t *testing.T) {
	channel := newStickyTestChannel([]string{"a", "b"}, nil)
	all := map[int]struct{}{0: {}, 1: {}}
	key, index, apiErr := channel.GetStickyEnabledKey("token:9", all)
	if apiErr != nil {
		t.Fatalf("exhausted retries must still return a key, got error: %v", apiErr)
	}
	if key == "" || index < 0 {
		t.Fatalf("expected a concrete key, got %q index %d", key, index)
	}
}

func TestGetStickyEnabledKeySkipsDisabledKeys(t *testing.T) {
	disabled := map[int]int{0: common.ChannelStatusAutoDisabled, 2: common.ChannelStatusManuallyDisabled}
	channel := newStickyTestChannel([]string{"a", "b", "c", "d"}, disabled)
	for i := range 500 {
		_, index, apiErr := channel.GetStickyEnabledKey("token:"+strconv.Itoa(i), nil)
		if apiErr != nil {
			t.Fatalf("unexpected error: %v", apiErr)
		}
		if _, isDisabled := disabled[index]; isDisabled {
			t.Fatalf("sticky routing picked disabled index %d", index)
		}
	}
}

func TestGetStickyEnabledKeyAllDisabledReportsNoKeys(t *testing.T) {
	disabled := map[int]int{0: common.ChannelStatusAutoDisabled, 1: common.ChannelStatusAutoDisabled}
	channel := newStickyTestChannel([]string{"a", "b"}, disabled)
	if _, _, apiErr := channel.GetStickyEnabledKey("token:1", nil); apiErr == nil {
		t.Fatal("expected an error when every key is disabled")
	}
}

func TestGetStickyEnabledKeyRemapsOnlyTheRemovedKey(t *testing.T) {
	before := newStickyTestChannel([]string{"a", "b", "c", "d"}, nil)
	after := newStickyTestChannel([]string{"a", "c", "d"}, nil)

	stable := 0
	total := 0
	for i := range 2000 {
		seed := "token:" + strconv.Itoa(i)
		previous, _, apiErr := before.GetStickyEnabledKey(seed, nil)
		if apiErr != nil {
			t.Fatalf("unexpected error: %v", apiErr)
		}
		if previous == "b" {
			continue
		}
		total++
		current, _, apiErr := after.GetStickyEnabledKey(seed, nil)
		if apiErr != nil {
			t.Fatalf("unexpected error: %v", apiErr)
		}
		if current == previous {
			stable++
		}
	}
	if total == 0 {
		t.Fatal("no seeds left the removed key, test is meaningless")
	}
	if ratio := float64(stable) / float64(total); ratio < 0.99 {
		t.Fatalf("removing one key remapped %.2f%% of unrelated seeds, want >= 99%%", (1-ratio)*100)
	}
}

func TestGetStickyEnabledKeyEmptySeedFallsBackToRotation(t *testing.T) {
	channel := newStickyTestChannel([]string{"a", "b", "c"}, nil)
	channel.ChannelInfo.MultiKeyMode = constant.MultiKeyModeRandom
	key, _, apiErr := channel.GetStickyEnabledKey("", nil)
	if apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}
	if key == "" {
		t.Fatal("empty seed must still return a usable key")
	}
}

func TestGetStickyEnabledKeySkipsBlankLines(t *testing.T) {
	channel := newStickyTestChannel([]string{"a", "", "b", "   "}, nil)
	for i := range 300 {
		key, _, apiErr := channel.GetStickyEnabledKey("token:"+strconv.Itoa(i), nil)
		if apiErr != nil {
			t.Fatalf("unexpected error: %v", apiErr)
		}
		if strings.TrimSpace(key) == "" {
			t.Fatalf("sticky routing returned a blank key for token:%d", i)
		}
	}
}

func TestGetStickyEnabledKeyAllBlankReportsNoKeys(t *testing.T) {
	channel := newStickyTestChannel([]string{"", "   ", "\n"}, nil)
	if _, _, apiErr := channel.GetStickyEnabledKey("token:1", nil); apiErr == nil {
		t.Fatal("a key list made only of blank lines must report no enabled keys")
	}
}

func TestGetNextEnabledKeyKeepsSpreadingForStickyChannels(t *testing.T) {
	// Task polling, balance checks and channel tests call GetNextEnabledKey
	// directly and have no request seed. Falling into the default branch there
	// would pin all of them to keys[0].
	channel := newStickyTestChannel([]string{"a", "b", "c", "d"}, nil)
	seen := make(map[string]int, 4)
	for range 400 {
		key, _, apiErr := channel.GetNextEnabledKey()
		if apiErr != nil {
			t.Fatalf("unexpected error: %v", apiErr)
		}
		seen[key]++
	}
	if len(seen) < 3 {
		t.Fatalf("seedless rotation collapsed onto %d key(s): %v", len(seen), seen)
	}
}

func TestGetStickyEnabledKeyIgnoresKeyOrder(t *testing.T) {
	before := newStickyTestChannel([]string{"a", "b", "c", "d"}, nil)
	after := newStickyTestChannel([]string{"d", "c", "b", "a"}, nil)
	for i := range 500 {
		seed := "token:" + strconv.Itoa(i)
		first, _, apiErr := before.GetStickyEnabledKey(seed, nil)
		if apiErr != nil {
			t.Fatalf("unexpected error: %v", apiErr)
		}
		second, _, apiErr := after.GetStickyEnabledKey(seed, nil)
		if apiErr != nil {
			t.Fatalf("unexpected error: %v", apiErr)
		}
		if first != second {
			t.Fatalf("reordering the key list moved seed %s from %q to %q", seed, first, second)
		}
	}
}

func TestGetStickyEnabledKeyDistributionStaysTight(t *testing.T) {
	keys := []string{"a", "b", "c", "d", "e"}
	channel := newStickyTestChannel(keys, nil)
	const total = 10000
	seen := make(map[string]int, len(keys))
	for i := range total {
		key, _, apiErr := channel.GetStickyEnabledKey("token:"+strconv.Itoa(i), nil)
		if apiErr != nil {
			t.Fatalf("unexpected error: %v", apiErr)
		}
		seen[key]++
	}
	expected := total / len(keys)
	low, high := expected*85/100, expected*115/100
	for key, count := range seen {
		if count < low || count > high {
			t.Fatalf("key %q got %d of %d, want %d +/- 15%%: %v", key, count, total, expected, seen)
		}
	}
}

func TestGetStickyEnabledKeyDisablingAKeyKeepsOtherSeeds(t *testing.T) {
	keys := []string{"a", "b", "c", "d"}
	healthy := newStickyTestChannel(keys, nil)
	disabled := newStickyTestChannel(keys, map[int]int{1: common.ChannelStatusAutoDisabled})

	stable, total := 0, 0
	for i := range 2000 {
		seed := "token:" + strconv.Itoa(i)
		before, _, apiErr := healthy.GetStickyEnabledKey(seed, nil)
		if apiErr != nil {
			t.Fatalf("unexpected error: %v", apiErr)
		}
		if before == "b" {
			continue
		}
		total++
		after, _, apiErr := disabled.GetStickyEnabledKey(seed, nil)
		if apiErr != nil {
			t.Fatalf("unexpected error: %v", apiErr)
		}
		if after == before {
			stable++
		}
	}
	if total == 0 {
		t.Fatal("no seeds were mapped outside the disabled key")
	}
	if ratio := float64(stable) / float64(total); ratio < 0.99 {
		t.Fatalf("disabling one key remapped %.2f%% of unrelated seeds, want >= 99%%", (1-ratio)*100)
	}
}

func TestStickyLargeChannelSharesStayEven(t *testing.T) {
	// 3000 keys is the size a real channel reaches; each key should still see
	// roughly its 1/n share. A group picked without weighting by its size would
	// hand the small groups' keys a bigger slice than the large groups' keys.
	const keyCount = 3000
	const samples = 60000
	keys := make([]string, keyCount)
	for i := range keys {
		keys[i] = "sk-bucket-" + strconv.Itoa(i)
	}
	channel := newStickyTestChannel(keys, nil)
	channel.Id = 9001

	counts := make([]int, keyCount)
	for i := range samples {
		_, index, apiErr := channel.GetStickyEnabledKey("token:"+strconv.Itoa(i), nil)
		if apiErr != nil {
			t.Fatalf("unexpected error: %v", apiErr)
		}
		counts[index]++
	}

	unused := 0
	for index, count := range counts {
		if count == 0 {
			unused++
			continue
		}
		// Expected 20 per key; Poisson noise allows a wide band, a structural
		// weighting bug lands far outside it.
		if count > 60 {
			t.Fatalf("key %d took %d of %d samples, expected ~20", index, count, samples)
		}
	}
	if unused > 0 {
		t.Fatalf("%d of %d keys never received traffic", unused, keyCount)
	}
}

func TestStickyLargeChannelRemapStaysNarrow(t *testing.T) {
	const keyCount = 3000
	keys := make([]string, keyCount)
	for i := range keys {
		keys[i] = "sk-remap-" + strconv.Itoa(i)
	}
	before := newStickyTestChannel(keys[:keyCount-1], nil)
	before.Id = 9002
	after := newStickyTestChannel(keys[:keyCount-2], nil)
	after.Id = 9003

	stable, total := 0, 0
	for i := range 4000 {
		seed := "token:" + strconv.Itoa(i)
		previous, _, apiErr := before.GetStickyEnabledKey(seed, nil)
		if apiErr != nil {
			t.Fatalf("unexpected error: %v", apiErr)
		}
		if previous == keys[keyCount-2] {
			continue
		}
		total++
		current, _, apiErr := after.GetStickyEnabledKey(seed, nil)
		if apiErr != nil {
			t.Fatalf("unexpected error: %v", apiErr)
		}
		if current == previous {
			stable++
		}
	}
	if total == 0 {
		t.Fatal("no seeds were mapped to the removed key")
	}
	// Flat rendezvous: removing one key only moves the seeds that were mapped to
	// it, so 1/3000 of the rest may shift. Anything near a full remap means the
	// mapping started depending on the key total again.
	if ratio := float64(stable) / float64(total); ratio < 0.999 {
		t.Fatalf("removing one key remapped %.2f%% of unrelated seeds, want >= 99.9%%", (1-ratio)*100)
	}
}

func TestStickyPlanRebuildsWhenKeyListChanges(t *testing.T) {
	channel := newStickyTestChannel([]string{"a", "b", "c"}, nil)
	channel.Id = 9004
	if _, _, apiErr := channel.GetStickyEnabledKey("token:1", nil); apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}

	// Different length: the plan must be rebuilt, and selection must only ever
	// return a key that is in the current list.
	channel.Key = "x\ny\nz\nw"
	channel.Keys = nil
	live := map[string]bool{"x": true, "y": true, "z": true, "w": true}
	for i := range 200 {
		key, _, apiErr := channel.GetStickyEnabledKey("token:"+strconv.Itoa(i), nil)
		if apiErr != nil {
			t.Fatalf("unexpected error: %v", apiErr)
		}
		if !live[key] {
			t.Fatalf("selection returned stale key %q after the key list changed", key)
		}
	}
}

func TestStickyPlanRebuildsAfterAKeyIsDisabled(t *testing.T) {
	keys := []string{"a", "b", "c", "d"}
	channel := newStickyTestChannel(keys, nil)
	channel.Id = 9100

	_, disabledIndex, apiErr := channel.GetStickyEnabledKey("token:7", nil)
	if apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}
	// Disable exactly the key this seed owns. The plan was built while every key
	// was enabled, so the next request must notice and rebuild instead of
	// handing back a key that is known to be dead.
	channel.ChannelInfo.MultiKeyStatusList = map[int]int{disabledIndex: common.ChannelStatusAutoDisabled}

	for range 5 {
		key, index, apiErr := channel.GetStickyEnabledKey("token:7", nil)
		if apiErr != nil {
			t.Fatalf("unexpected error: %v", apiErr)
		}
		if index == disabledIndex || key == keys[disabledIndex] {
			t.Fatalf("selection kept returning the disabled key %q (index %d)", key, index)
		}
	}
}

func TestStickyOnlyScoresEligibleKeys(t *testing.T) {
	keys := []string{"a", "b", "c", "d", "e", "f"}
	disabled := map[int]int{
		0: common.ChannelStatusAutoDisabled,
		1: common.ChannelStatusAutoDisabled,
		2: common.ChannelStatusManuallyDisabled,
		4: common.ChannelStatusAutoDisabled,
	}
	channel := newStickyTestChannel(keys, disabled)
	channel.Id = 9101

	for i := range 300 {
		_, index, apiErr := channel.GetStickyEnabledKey("token:"+strconv.Itoa(i), nil)
		if apiErr != nil {
			t.Fatalf("unexpected error: %v", apiErr)
		}
		if index != 3 && index != 5 {
			t.Fatalf("selection returned index %d, only 3 and 5 are enabled", index)
		}
	}
}

func TestStickyRebuildsAfterKeysAreReEnabled(t *testing.T) {
	keys := []string{"a", "b", "c", "d"}
	channel := newStickyTestChannel(keys, map[int]int{
		0: common.ChannelStatusAutoDisabled,
		1: common.ChannelStatusAutoDisabled,
	})
	channel.Id = 9102

	if _, index, apiErr := channel.GetStickyEnabledKey("token:3", nil); apiErr != nil || (index != 2 && index != 3) {
		t.Fatalf("unexpected selection index %d err %v", index, apiErr)
	}

	// Re-enable everything: the plan must stop filtering them out.
	channel.ChannelInfo.MultiKeyStatusList = nil
	seen := make(map[int]bool, len(keys))
	for i := range 200 {
		_, index, apiErr := channel.GetStickyEnabledKey("token:"+strconv.Itoa(i), nil)
		if apiErr != nil {
			t.Fatalf("unexpected error: %v", apiErr)
		}
		seen[index] = true
	}
	if len(seen) < 3 {
		t.Fatalf("re-enabled keys never came back into rotation: %v", seen)
	}
}

func TestGetNextEnabledKeySpreadsWhenModeIsUnset(t *testing.T) {
	// An unset mode means the channel predates the field or was created by an
	// API that never sent one. The console reports it as `random`, so routing
	// must spread; pinning keys[0] made the UI disagree with reality.
	for _, mode := range []constant.MultiKeyMode{"", "legacy-value"} {
		channel := newStickyTestChannel([]string{"a", "b", "c", "d"}, nil)
		channel.ChannelInfo.MultiKeyMode = mode

		seen := make(map[string]int, 4)
		for range 400 {
			key, _, apiErr := channel.GetNextEnabledKey()
			if apiErr != nil {
				t.Fatalf("mode %q: unexpected error: %v", mode, apiErr)
			}
			seen[key]++
		}
		if len(seen) < 3 {
			t.Fatalf("mode %q collapsed onto %d key(s): %v", mode, len(seen), seen)
		}
	}
}
