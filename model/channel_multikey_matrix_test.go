package model

import (
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
)

// The matrix below exists because the same setting has to behave identically in
// every mode, and key data is edited while requests are in flight. Both are easy
// to break in a way a single-mode test never notices: the cap was honoured by
// random and ignored by polling for exactly that reason.

var matrixModes = []struct {
	name string
	mode constant.MultiKeyMode
}{
	{"random", constant.MultiKeyModeRandom},
	{"polling", constant.MultiKeyModePolling},
	{"sticky", constant.MultiKeyModeSticky},
	{"unset", ""},
}

// pickIndex drives the real request path for the mode: sticky requests carry a
// seed and go through GetStickyEnabledKey, everything else goes through
// GetNextEnabledKey. Testing sticky through GetNextEnabledKey would only cover
// the seedless path (task polling, balance checks).
func pickIndex(t *testing.T, channel *Channel, seed string) int {
	t.Helper()
	if channel.ChannelInfo.MultiKeyMode == constant.MultiKeyModeSticky {
		_, index, apiErr := channel.GetStickyEnabledKey(seed, nil)
		if apiErr != nil {
			t.Fatalf("sticky returned an error: %v", apiErr)
		}
		return index
	}
	_, index, apiErr := channel.GetNextEnabledKey()
	if apiErr != nil {
		t.Fatalf("selection returned an error: %v", apiErr)
	}
	return index
}

// setKeys mimics what the cache refresh does: both the text and the parsed slice.
// Writing only Key leaves GetKeys returning the stale slice, which hides every
// edit in a test.
func setKeys(channel *Channel, keys []string) {
	channel.Key = strings.Join(keys, "\n")
	channel.Keys = keys
	channel.ChannelInfo.MultiKeySize = len(keys)
}

func matrixKeys(count int) []string {
	keys := make([]string, count)
	for i := range keys {
		keys[i] = "sk-matrix-" + strconv.Itoa(i)
	}
	return keys
}

func TestMatrixEveryModeStaysInsideTheWindow(t *testing.T) {
	for _, mode := range matrixModes {
		for _, capSize := range []int{0, 1, 2, 5} {
			for _, keyCount := range []int{1, 3, 6} {
				name := mode.name + "/cap" + strconv.Itoa(capSize) + "/keys" + strconv.Itoa(keyCount)
				t.Run(name, func(t *testing.T) {
					channel := poolChannel(matrixKeys(keyCount), capSize, nil)
					channel.ChannelInfo.MultiKeyMode = mode.mode
					channel.Id = 8100 + keyCount*10 + capSize
					withCachedChannel(t, channel)

					want := keyCount
					if capSize > 0 && capSize < keyCount {
						want = capSize
					}
					seen := make(map[int]bool, want)
					for i := range 240 {
						index := pickIndex(t, channel, "token:"+strconv.Itoa(i))
						if index < 0 || index >= keyCount {
							t.Fatalf("index %d out of range", index)
						}
						if capSize > 0 && index >= capSize {
							t.Fatalf("index %d served outside a cap of %d", index, capSize)
						}
						seen[index] = true
					}
					if len(seen) != want {
						t.Fatalf("touched %d keys, want %d: %v", len(seen), want, seen)
					}
				})
			}
		}
	}
}

func TestMatrixEveryModeSkipsDisabledKeys(t *testing.T) {
	disabled := map[int]int{
		1: common.ChannelStatusAutoDisabled,
		3: common.ChannelStatusManuallyDisabled,
	}
	for _, mode := range matrixModes {
		t.Run(mode.name, func(t *testing.T) {
			channel := poolChannel(matrixKeys(6), 0, disabled)
			channel.ChannelInfo.MultiKeyMode = mode.mode
			channel.Id = 8200 + len(mode.name)
			withCachedChannel(t, channel)

			for i := range 200 {
				index := pickIndex(t, channel, "token:"+strconv.Itoa(i))
				if _, bad := disabled[index]; bad {
					t.Fatalf("served disabled index %d", index)
				}
			}
		})
	}
}

func TestMatrixEveryModeErrorsWhenNothingIsUsable(t *testing.T) {
	all := make(map[int]int, 4)
	for i := range 4 {
		all[i] = common.ChannelStatusAutoDisabled
	}
	for _, mode := range matrixModes {
		t.Run(mode.name, func(t *testing.T) {
			channel := poolChannel(matrixKeys(4), 2, all)
			channel.ChannelInfo.MultiKeyMode = mode.mode
			channel.Id = 8300 + len(mode.name)

			if _, _, apiErr := channel.GetNextEnabledKey(); apiErr == nil {
				t.Fatal("expected an error when every key is disabled")
			}
			if _, _, apiErr := channel.GetStickyEnabledKey("token:1", nil); apiErr == nil {
				t.Fatal("expected an error from sticky when every key is disabled")
			}
		})
	}
}

// Sticky without a batch is the plain case: a token owns one key and keeps it.
func TestStickyWithoutBatchKeepsSeedOnOneKey(t *testing.T) {
	channel := poolChannel(matrixKeys(6), 0, nil)
	channel.ChannelInfo.MultiKeyMode = constant.MultiKeyModeSticky
	channel.Id = 8401

	first := make(map[string]int, 300)
	for i := range 300 {
		seed := "token:" + strconv.Itoa(i)
		index := pickIndex(t, channel, seed)
		first[seed] = index
		// Repeat immediately: the answer must not depend on call order.
		if again := pickIndex(t, channel, seed); again != index {
			t.Fatalf("seed %s moved from %d to %d between two calls", seed, index, again)
		}
	}

	// Spread across all six, and each roughly a sixth.
	counts := make(map[int]int, 6)
	for _, index := range first {
		counts[index]++
	}
	if len(counts) != 6 {
		t.Fatalf("sticky used %d of 6 keys: %v", len(counts), counts)
	}
	for index, count := range counts {
		if count < 30 || count > 70 {
			t.Fatalf("key %d took %d of 300 seeds: %v", index, count, counts)
		}
	}
}

// Sticky with a batch: the same, but every token has to land in the window.
func TestStickyWithBatchKeepsSeedInsideTheWindow(t *testing.T) {
	channel := poolChannel(matrixKeys(6), 2, nil)
	channel.ChannelInfo.MultiKeyMode = constant.MultiKeyModeSticky
	channel.Id = 8402

	first := make(map[string]int, 300)
	for i := range 300 {
		seed := "token:" + strconv.Itoa(i)
		index := pickIndex(t, channel, seed)
		if index > 1 {
			t.Fatalf("seed %s landed on key %d outside a cap of 2", seed, index)
		}
		first[seed] = index
		if again := pickIndex(t, channel, seed); again != index {
			t.Fatalf("seed %s moved from %d to %d", seed, index, again)
		}
	}

	counts := make(map[int]int, 2)
	for _, index := range first {
		counts[index]++
	}
	if len(counts) != 2 {
		t.Fatalf("both window keys must be used: %v", counts)
	}
	for index, count := range counts {
		if count < 110 || count > 190 {
			t.Fatalf("key %d took %d of 300 seeds: %v", index, count, counts)
		}
	}
}

// Retrying must reach the other key in the window, not a spare outside it.
func TestStickyRetryStaysInsideTheWindow(t *testing.T) {
	channel := poolChannel(matrixKeys(6), 2, nil)
	channel.ChannelInfo.MultiKeyMode = constant.MultiKeyModeSticky
	channel.Id = 8403

	skip := make(map[int]struct{}, 2)
	seen := make(map[int]bool, 2)
	for range 2 {
		_, index, apiErr := channel.GetStickyEnabledKey("token:42", skip)
		if apiErr != nil {
			t.Fatalf("unexpected error: %v", apiErr)
		}
		if index > 1 {
			t.Fatalf("retry escaped the window: index %d", index)
		}
		if seen[index] {
			t.Fatalf("retry returned the same key %d again", index)
		}
		seen[index] = true
		skip[index] = struct{}{}
	}
	if len(seen) != 2 {
		t.Fatalf("expected both window keys, got %v", seen)
	}
}

// Editing key data is the case that breaks caches. After each edit the very next
// request has to be valid — a live key, not disabled, inside the window.
func TestKeyEditsTakeEffectOnTheNextRequest(t *testing.T) {
	for _, mode := range matrixModes {
		t.Run(mode.name, func(t *testing.T) {
			keys := matrixKeys(6)
			channel := poolChannel(keys, 3, nil)
			channel.ChannelInfo.MultiKeyMode = mode.mode
			channel.Id = 8500 + len(mode.name)
			withCachedChannel(t, channel)

			// The window is the first three *available* keys, so its members are
			// positions, not absolute indexes: disabling key 1 hands its slot to
			// key 3.
			assertServable := func(label string, live []string, disabled map[int]int, capSize int) {
				t.Helper()
				active := make(map[int]bool, capSize)
				for i, key := range live {
					if strings.TrimSpace(key) == "" {
						continue
					}
					if status, ok := disabled[i]; ok && status != common.ChannelStatusEnabled {
						continue
					}
					if len(active) == capSize {
						break
					}
					active[i] = true
				}
				for i := range 60 {
					key, index := pickKey(t, channel, "token:"+strconv.Itoa(i))
					if index < 0 || index >= len(live) {
						t.Fatalf("%s: index %d out of range %d", label, index, len(live))
					}
					if !active[index] {
						t.Fatalf("%s: served index %d which is not in the window %v", label, index, active)
					}
					if key != live[index] {
						t.Fatalf("%s: returned %q for index %d, live key is %q", label, key, index, live[index])
					}
				}
			}

			assertServable("initial", keys, nil, 3)

			// Append.
			keys = append(keys, "sk-matrix-appended")
			setKeys(channel, keys)
			assertServable("append", keys, nil, 3)

			// Insert in the middle: every later index shifts.
			keys = append(keys[:2], append([]string{"sk-matrix-inserted"}, keys[2:]...)...)
			setKeys(channel, keys)
			assertServable("insert", keys, nil, 3)

			// Overwrite one key in place, same length.
			keys[4] = "sk-matrix-replaced"
			setKeys(channel, keys)
			assertServable("replace", keys, nil, 3)

			// Delete from the middle.
			keys = append(keys[:1], keys[2:]...)
			setKeys(channel, keys)
			assertServable("delete", keys, nil, 3)

			// Disable a key inside the window.
			channel.ChannelInfo.MultiKeyStatusList = map[int]int{
				1: common.ChannelStatusManuallyDisabled,
			}
			assertServable("disable", keys, map[int]int{1: common.ChannelStatusManuallyDisabled}, 3)

			// Re-enable it, then widen the window.
			channel.ChannelInfo.MultiKeyStatusList = nil
			channel.ChannelInfo.MinActiveKeys = len(keys)
			assertServable("widen", keys, nil, len(keys))
		})
	}
}

func pickKey(t *testing.T, channel *Channel, seed string) (string, int) {
	t.Helper()
	if channel.ChannelInfo.MultiKeyMode == constant.MultiKeyModeSticky {
		key, index, apiErr := channel.GetStickyEnabledKey(seed, nil)
		if apiErr != nil {
			t.Fatalf("sticky returned an error: %v", apiErr)
		}
		return key, index
	}
	key, index, apiErr := channel.GetNextEnabledKey()
	if apiErr != nil {
		t.Fatalf("selection returned an error: %v", apiErr)
	}
	return key, index
}

// Rewriting the whole list must never leak a key from the previous content, and
// must never attribute an old fingerprint to a new key.
func TestOverwritingTheKeyListNeverResurrectsOldKeys(t *testing.T) {
	old := matrixKeys(8)
	channel := poolChannel(old, 0, nil)
	channel.ChannelInfo.MultiKeyMode = constant.MultiKeyModeSticky
	channel.Id = 8601

	oldFingerprint := MultiKeyFingerprint(old[2])

	next := make([]string, len(old))
	for i := range next {
		next[i] = "sk-new-" + strconv.Itoa(i)
	}
	setKeys(channel, next)

	live := make(map[string]bool, len(next))
	for _, key := range next {
		live[key] = true
	}
	for i := range 200 {
		_, index := pickKey(t, channel, "token:"+strconv.Itoa(i))
		key := next[index]
		if !live[key] {
			t.Fatalf("selection returned %q which is not in the current list", key)
		}
	}

	// The old key is gone, so its fingerprint must not resolve to whatever now
	// sits at that index — that would hand a task to the wrong credential.
	if key, _ := channel.GetKeyByFingerprint(2, oldFingerprint); key == next[2] {
		t.Fatalf("old fingerprint resolved to the replacement key %q", key)
	}
}

// A task is bound to the credential that created it. Every edit below must keep
// that credential reachable, except deleting the key itself.
func TestTaskKeySurvivesKeyListEdits(t *testing.T) {
	keys := matrixKeys(5)
	channel := poolChannel(keys, 2, nil)
	channel.Id = 8701

	const bound = 4
	task := &Task{}
	task.PrivateData.MultiKeyIndex = bound
	task.PrivateData.MultiKeyFingerprint = MultiKeyFingerprint(keys[bound])
	task.PrivateData.UpstreamKey = keys[bound]
	task.PrivateData.UpstreamKeyAt = 1 << 40 // far future: retained copy is fresh

	assertBound := func(label string, live []string) {
		t.Helper()
		key, _ := GetTaskKey(channel, task)
		if key != keys[bound] {
			t.Fatalf("%s: got %q, want the bound key %q", label, key, keys[bound])
		}
		if len(live) > 0 && live[0] == "" {
			t.Fatal("unreachable guard")
		}
	}

	assertBound("baseline", keys)

	// Insert before it: the index shifts but the key is still there.
	inserted := append([]string{"sk-inserted"}, keys...)
	setKeys(channel, inserted)
	assertBound("insert", inserted)

	// Replace its neighbour: the bound key itself is untouched.
	replaced := append([]string(nil), inserted...)
	replaced[2] = "sk-other"
	setKeys(channel, replaced)
	assertBound("neighbour replace", replaced)

	// Disable it for billing reasons: a flagged key is still the only credential
	// that can see the task, so the retained copy has to win.
	channel.ChannelInfo.MultiKeyStatusList = map[int]int{bound: common.ChannelStatusAutoDisabled}
	assertBound("disable", replaced)

	// Once the retained copy expires, the lookup falls back to the fingerprint,
	// which refuses disabled keys and lands on the first usable one. The task is
	// still reachable with *a* credential rather than a blank header.
	// A real expired timestamp: zero would mean "no timestamp recorded", which
	// GetTaskKey treats as still fresh.
	task.PrivateData.UpstreamKeyAt = time.Now().Unix() - taskKeyRetentionSeconds - 1
	channel.ChannelInfo.MultiKeyStatusList = nil
	key, _ := GetTaskKey(channel, task)
	if key == "" {
		t.Fatal("fallback returned an empty key; upstream would see a blank credential")
	}

	// Delete it: nothing can reach that credential any more.
	shrunk := append(replaced[:bound+1], replaced[bound+2:]...)
	setKeys(channel, shrunk)
	if key, _ := GetTaskKey(channel, task); key == keys[bound] {
		t.Fatal("deleted key still resolved")
	}
}

// Polling with a batch must walk the window in order and pick up a key that
// enters it, rather than staying on a fixed pair.
func TestPollingWindowRotatesInOrderAndAdmitsNewKeys(t *testing.T) {
	channel := poolChannel(matrixKeys(4), 2, nil)
	channel.ChannelInfo.MultiKeyMode = constant.MultiKeyModePolling
	channel.Id = 8801
	withCachedChannel(t, channel)

	want := []int{0, 1, 0, 1}
	for round, expected := range want {
		_, index, apiErr := channel.GetNextEnabledKey()
		if apiErr != nil {
			t.Fatalf("round %d: %v", round, apiErr)
		}
		if index != expected {
			t.Fatalf("round %d: got %d, want %d", round, index, expected)
		}
	}

	// Disabling key 0 hands its slot to key 2.
	channel.ChannelInfo.MultiKeyStatusList = map[int]int{0: common.ChannelStatusAutoDisabled}
	seen := make(map[int]bool, 2)
	for range 4 {
		_, index, apiErr := channel.GetNextEnabledKey()
		if apiErr != nil {
			t.Fatalf("unexpected error: %v", apiErr)
		}
		if index == 0 {
			t.Fatal("disabled key served after its status changed")
		}
		seen[index] = true
	}
	if !seen[1] || !seen[2] {
		t.Fatalf("window did not slide to keys 1 and 2: %v", seen)
	}
}

// A single-key channel must ignore the batch setting entirely; there is nothing
// to rotate to.
func TestBatchIsIgnoredOnSingleKeyChannels(t *testing.T) {
	for _, mode := range matrixModes {
		t.Run(mode.name, func(t *testing.T) {
			channel := &Channel{Key: "sk-only"}
			channel.ChannelInfo.MultiKeyMode = mode.mode
			channel.ChannelInfo.MinActiveKeys = 5
			channel.Id = 8900 + len(mode.name)

			key, index := pickKey(t, channel, "token:1")
			if key != "sk-only" || index != 0 {
				t.Fatalf("got %q at %d", key, index)
			}
		})
	}
}

// Parallel requests must agree with the sequential answer. The sticky plan is
// shared through a sync.Map and may be rebuilt by several goroutines at once;
// a plan that leaked state between rebuilds would show up here.
func TestStickyConcurrentSelectionsAgreeWithSequential(t *testing.T) {
	const keyCount = 12
	const capSize = 5

	channel := poolChannel(matrixKeys(keyCount), capSize, nil)
	channel.ChannelInfo.MultiKeyMode = constant.MultiKeyModeSticky
	channel.Id = 9001

	const workers = 8
	const perWorker = 300
	results := make([][]int, workers)
	var wg sync.WaitGroup
	for worker := range workers {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			indexes := make([]int, perWorker)
			for i := range perWorker {
				_, index, apiErr := channel.GetStickyEnabledKey("token:"+strconv.Itoa(worker*perWorker+i), nil)
				if apiErr != nil {
					t.Errorf("worker %d: %v", worker, apiErr)
					return
				}
				indexes[i] = index
			}
			results[worker] = indexes
		}(worker)
	}
	wg.Wait()

	for worker := range workers {
		for i := range perWorker {
			seed := "token:" + strconv.Itoa(worker*perWorker+i)
			_, want, apiErr := channel.GetStickyEnabledKey(seed, nil)
			if apiErr != nil {
				t.Fatalf("sequential replay failed: %v", apiErr)
			}
			if got := results[worker][i]; got != want {
				t.Fatalf("seed %s: concurrent %d, sequential %d", seed, got, want)
			}
			if want >= capSize {
				t.Fatalf("seed %s escaped the window: %d", seed, want)
			}
		}
	}
}

// Polling is serialised by a per-channel lock, so a burst of parallel requests
// must still produce an exactly even rotation — not duplicate keys.
func TestPollingConcurrentBurstRotatesExactly(t *testing.T) {
	const keyCount = 4
	channel := poolChannel(matrixKeys(keyCount), 0, nil)
	channel.ChannelInfo.MultiKeyMode = constant.MultiKeyModePolling
	channel.Id = 9002
	withCachedChannel(t, channel)

	const workers = 4
	const perWorker = 5
	var (
		mu     sync.Mutex
		counts = make(map[int]int, keyCount)
		wg     sync.WaitGroup
	)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perWorker {
				_, index, apiErr := channel.GetNextEnabledKey()
				if apiErr != nil {
					t.Errorf("unexpected error: %v", apiErr)
					return
				}
				mu.Lock()
				counts[index]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	for index := range keyCount {
		if counts[index] != workers*perWorker/keyCount {
			t.Fatalf("key %d got %d of %d calls: %v", index, counts[index], workers*perWorker, counts)
		}
	}
}

// Turning a batch on is the operation that would silently flush every prompt
// cache if it rehashed. It must not: a token whose key is already inside the
// window keeps it, because narrowing the candidate set cannot beat the winner it
// already had.
func TestStickyEnablingBatchKeepsSeedsAlreadyInside(t *testing.T) {
	channel := poolChannel(matrixKeys(8), 0, nil)
	channel.ChannelInfo.MultiKeyMode = constant.MultiKeyModeSticky
	channel.Id = 9101

	const seeds = 400
	before := make(map[string]int, seeds)
	for i := range seeds {
		seed := "token:" + strconv.Itoa(i)
		before[seed] = pickIndex(t, channel, seed)
	}

	channel.ChannelInfo.MinActiveKeys = 3

	movedInside, movedOutside := 0, 0
	for i := range seeds {
		seed := "token:" + strconv.Itoa(i)
		after := pickIndex(t, channel, seed)
		if after >= 3 {
			t.Fatalf("seed %s escaped the window: %d", seed, after)
		}
		switch {
		case before[seed] < 3:
			if after != before[seed] {
				movedInside++
			}
		default:
			movedOutside++
		}
	}
	if movedInside != 0 {
		t.Fatalf("%d seeds inside the window moved when the batch was applied", movedInside)
	}
	if movedOutside == 0 {
		t.Fatal("no seed moved; the batch had no effect")
	}
}

// A batch of one is legal and means "one key, period".
func TestStickyWithBatchOfOne(t *testing.T) {
	channel := poolChannel(matrixKeys(5), 1, nil)
	channel.ChannelInfo.MultiKeyMode = constant.MultiKeyModeSticky
	channel.Id = 9102

	for i := range 200 {
		_, index, apiErr := channel.GetStickyEnabledKey("token:"+strconv.Itoa(i), nil)
		if apiErr != nil {
			t.Fatalf("unexpected error: %v", apiErr)
		}
		if index != 0 {
			t.Fatalf("batch of one served index %d", index)
		}
	}
}

// A batch larger than the key list degrades to "everything", not to an error.
func TestStickyWithBatchLargerThanTheList(t *testing.T) {
	channel := poolChannel(matrixKeys(3), 10, nil)
	channel.ChannelInfo.MultiKeyMode = constant.MultiKeyModeSticky
	channel.Id = 9103

	seen := make(map[int]bool, 3)
	for i := range 200 {
		_, index, apiErr := channel.GetStickyEnabledKey("token:"+strconv.Itoa(i), nil)
		if apiErr != nil {
			t.Fatalf("unexpected error: %v", apiErr)
		}
		seen[index] = true
	}
	if len(seen) != 3 {
		t.Fatalf("expected all three keys in rotation, got %v", seen)
	}
}

// Plans are cached per channel and rebuilt when the key list, the cap or the
// size of the status map changes. Ageing is the backstop for everything those
// checks cannot see.
func TestStickyPlanAgesOut(t *testing.T) {
	keys := matrixKeys(6)
	status := map[int]int{0: common.ChannelStatusAutoDisabled}

	first := stickyPlanFor(9201, keys, status, 0)
	if again := stickyPlanFor(9201, keys, status, 0); again != first {
		t.Fatal("an unchanged plan must be reused, not rebuilt on every request")
	}

	// Simulate the window elapsing instead of sleeping.
	first.builtAt = time.Now().Add(-2 * stickyPlanMaxAge)
	rebuilt := stickyPlanFor(9201, keys, status, 0)
	if rebuilt == first {
		t.Fatal("a plan past its age must be rebuilt")
	}
	if rebuilt.statusCount != len(status) {
		t.Fatalf("rebuilt plan kept a stale status count: %d", rebuilt.statusCount)
	}
	if isActiveIndex(rebuilt.eligible, 0) {
		t.Fatalf("rebuild ignored the status map: %v", rebuilt.eligible)
	}
}

// The hole ageing closes: swapping which key is disabled keeps the status map
// the same size, so the size check sees nothing and a re-enabled key would stay
// out of rotation for as long as the channel is idle.
func TestStickyPlanAgeAdmitsAReEnabledKey(t *testing.T) {
	keys := matrixKeys(4)
	keyOneDisabled := map[int]int{1: common.ChannelStatusAutoDisabled}
	keyZeroDisabled := map[int]int{0: common.ChannelStatusAutoDisabled}

	before := stickyPlanFor(9202, keys, keyOneDisabled, 0)
	if !isActiveIndex(before.eligible, 0) || isActiveIndex(before.eligible, 1) {
		t.Fatalf("unexpected baseline eligibility: %v", before.eligible)
	}

	swapped := stickyPlanFor(9202, keys, keyZeroDisabled, 0)
	if swapped != before {
		t.Fatal("precondition broken: the swap was expected to be invisible to the size check")
	}

	before.builtAt = time.Now().Add(-2 * stickyPlanMaxAge)
	refreshed := stickyPlanFor(9202, keys, keyZeroDisabled, 0)

	if !isActiveIndex(refreshed.eligible, 1) {
		t.Fatalf("re-enabled key 1 is still excluded after ageing: %v", refreshed.eligible)
	}
	if isActiveIndex(refreshed.eligible, 0) {
		t.Fatalf("disabled key 0 is still eligible after ageing: %v", refreshed.eligible)
	}
}
