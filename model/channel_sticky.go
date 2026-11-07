package model

import (
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
)

// GetStickyEnabledKey returns an enabled key chosen by rendezvous hashing over
// the caller-supplied seed, skipping indexes the same request already tried.
//
// Rendezvous (highest-random-weight) instead of `hash(seed) % len(keys)` so
// that adding or disabling one key only remaps the seeds of that key. A modulo
// mapping reshuffles every seed on any key-list change, which defeats the point
// of a sticky key: the upstream prompt cache would go cold for everyone.
//
// A channel can carry thousands of keys, so nothing that only changes when the
// channel changes is recomputed per request. stickyKeyPlan holds the hash of
// every key *and* the list of keys currently eligible, so a request walks the
// eligible set — not the whole list — and pays one seed hash plus one
// multiply-shift per candidate.
//
// Disabled keys are the reason this matters: a channel where half the keys are
// out of credit would otherwise spend every request looking up 3000 statuses to
// discard half of them. The plan is built from the live status map and is
// revalidated lazily — see pick — because key status changes have no version
// counter to watch.
//
// The seed is deliberately a caller concern. model cannot see the request, and
// an empty seed falls back to the regular rotation so a caller that forgets to
// pass one still gets working routing.
//
// APIDUO:multi-key-sticky
func (channel *Channel) GetStickyEnabledKey(seed string, skip map[int]struct{}) (string, int, *types.NewAPIError) {
	if channel == nil {
		return "", 0, types.NewError(errors.New("channel is nil"), types.ErrorCodeGetChannelFailed)
	}
	if !channel.ChannelInfo.IsMultiKey {
		return channel.Key, 0, nil
	}
	keys := channel.GetKeys()
	if len(keys) == 0 {
		return "", 0, types.NewError(errors.New("no keys available"), types.ErrorCodeChannelNoAvailableKey)
	}
	if strings.TrimSpace(seed) == "" {
		return channel.GetNextEnabledKey()
	}

	seedHash := stickyHash(seed)
	statusList := channel.ChannelInfo.MultiKeyStatusList

	// Two rounds at most: the first uses the cached plan, the second rebuilds if
	// the plan turned out to predate a status change. A rebuild is O(n) and only
	// happens once per status change, never per request.
	for range 2 {
		plan := stickyPlanFor(channel.Id, keys, statusList, channel.ActiveKeyLimit())
		best, stale := plan.pick(seedHash, statusList, skip)
		if stale {
			stickyPlanCache.Delete(channel.Id)
			continue
		}
		if best < 0 {
			// Second pass ignores `skip`: a request that burned through every key
			// on retries still needs a key rather than a failed channel.
			best, stale = plan.pick(seedHash, statusList, nil)
			if stale {
				stickyPlanCache.Delete(channel.Id)
				continue
			}
		}
		if best >= 0 {
			return keys[best], best, nil
		}
		break
	}
	return "", 0, types.NewError(errors.New("no enabled keys"), types.ErrorCodeChannelNoAvailableKey)
}

// stickyKeyPlan is the precomputed side of selection: key hashes, the keys that
// are worth scoring at all, and the fingerprint used to notice that the key list
// changed. Built off the request path; the request only scores `eligible`.
type stickyKeyPlan struct {
	keyCount int
	probe    [3]uint64
	hashes   []uint64
	// statusCount is the size of the status map the plan was built from. The
	// lazy check in pick only notices a key turning *off*; a key coming back on
	// is invisible to it because the plan never scores that index at all, and a
	// lane nobody scores never gets traffic again. One len() per request closes
	// that hole — enabling a key removes its entry, so the size changes.
	statusCount int
	// minActive is the active cap the eligible list was trimmed to. It has to be
	// part of the cache check: changing the cap without changing the keys or the
	// statuses would otherwise keep serving the previous width.
	minActive int
	// builtAt bounds how long the plan may serve without being rebuilt.
	builtAt time.Time
	// eligible holds indexes of keys that are not blank and not disabled at
	// build time. Walking this instead of the full list is what keeps a channel
	// with many burned keys cheap.
	eligible []int
}

// pick returns the eligible key with the highest score for this seed.
//
// `stale` means the winner is disabled according to the live status map, so the
// plan was built before that status changed. Checking the winner — one map
// lookup — instead of every candidate keeps the common path at zero lookups,
// and a stale plan costs one extra rebuild rather than a wrong key: the caller
// rebuilds and asks again.
func (plan *stickyKeyPlan) pick(seedHash uint64, statusList map[int]int, skip map[int]struct{}) (int, bool) {
	best := -1
	var bestScore uint64
	for _, i := range plan.eligible {
		if skip != nil {
			if _, tried := skip[i]; tried {
				continue
			}
		}
		score := stickyMix64(seedHash ^ plan.hashes[i])
		if best < 0 || score > bestScore {
			best, bestScore = i, score
		}
	}
	if best < 0 {
		return -1, false
	}
	if statusList != nil {
		if status, ok := statusList[best]; ok && status != common.ChannelStatusEnabled {
			return -1, true
		}
	}
	return best, false
}

// stickyPlanCache is keyed by channel id: one channel keeps one plan across
// requests, and a rebuilt key list replaces it in place. Entries for deleted
// channels are not evicted; each is bounded by the channel's own key count and
// channels are created far less often than requests are served.
var stickyPlanCache sync.Map

// stickyPlanMaxAge bounds how stale a plan may get.
//
// The plan notices key-list edits and cap changes exactly, and notices status
// changes only by the size of the status map. That leaves one hole: disabling
// one key while enabling another in the same edit keeps the size identical, so
// the newly enabled key stays out of the eligible list and never gets traffic.
// Closing it with a version counter would mean bumping that counter at every
// write site — handlers upstream rewrites. Ageing the plan instead costs one
// clock read per request and caps the staleness at this window; a rebuild on a
// 3000-key channel is about 45us and happens at most once per window.
var stickyPlanMaxAge = 30 * time.Second

func (plan *stickyKeyPlan) expired(now time.Time) bool {
	return now.Sub(plan.builtAt) > stickyPlanMaxAge
}

func stickyPlanFor(channelID int, keys []string, statusList map[int]int, minActive int) *stickyKeyPlan {
	if cached, ok := stickyPlanCache.Load(channelID); ok {
		if plan, ok := cached.(*stickyKeyPlan); ok && plan.matches(keys) &&
			plan.statusCount == len(statusList) && plan.minActive == minActive &&
			!plan.expired(time.Now()) {
			return plan
		}
	}
	plan := buildStickyKeyPlan(keys, statusList, minActive)
	stickyPlanCache.Store(channelID, plan)
	return plan
}

func (plan *stickyKeyPlan) matches(keys []string) bool {
	if plan == nil || plan.keyCount != len(keys) {
		return false
	}
	return plan.probe == stickyPlanProbe(keys)
}

// stickyPlanProbe samples three positions instead of hashing the whole list. A
// plan that survives a same-length replacement of an unsampled key still
// returns the live key (selection reads the current slice), it only scores that
// position with the previous content — a stable mapping, not a wrong one.
func stickyPlanProbe(keys []string) [3]uint64 {
	return [3]uint64{
		stickyHash(keys[0]),
		stickyHash(keys[len(keys)/2]),
		stickyHash(keys[len(keys)-1]),
	}
}

func buildStickyKeyPlan(keys []string, statusList map[int]int, minActive int) *stickyKeyPlan {
	plan := &stickyKeyPlan{
		keyCount:    len(keys),
		probe:       stickyPlanProbe(keys),
		hashes:      make([]uint64, len(keys)),
		eligible:    make([]int, 0, len(keys)),
		statusCount: len(statusList),
		minActive:   minActive,
		builtAt:     time.Now(),
	}

	for i := range keys {
		plan.hashes[i] = stickyHash(keys[i])
		// A blank line in the key textarea is not a key. Rotation would spend a
		// share of requests on it and retry its way out; sticky would pin the
		// same token to the blank entry every time.
		if strings.TrimSpace(keys[i]) == "" {
			continue
		}
		if statusList != nil {
			if status, ok := statusList[i]; ok && status != common.ChannelStatusEnabled {
				continue
			}
		}
		plan.eligible = append(plan.eligible, i)
	}
	plan.eligible = limitActive(plan.eligible, minActive)
	return plan
}

// stickyKeyScore keeps a directly callable score for benchmarks and tests.
func stickyKeyScore(seed string, key string) uint64 {
	return stickyMix64(stickyHash(seed) ^ stickyHash(key))
}

const (
	stickyFNVOffset64 = 14695981039346656037
	stickyFNVPrime64  = 1099511628211
)

// stickyHash is FNV-1a written out instead of via hash/fnv: this runs once per
// key at plan build and once per request for the seed, and the stdlib version
// allocates a digest object on every call.
func stickyHash(s string) uint64 {
	h := uint64(stickyFNVOffset64)
	for i := range len(s) {
		h ^= uint64(s[i])
		h *= stickyFNVPrime64
	}
	return h
}

// stickyMix64 is the MurmurHash3 finalizer. FNV-1a's high bits are poorly
// diffused for short similar inputs — measured over four keys, comparing the
// raw values steered half the seeds to one key. Mixing restores the uniformity
// the highest-score-wins step depends on.
func stickyMix64(x uint64) uint64 {
	x ^= x >> 33
	x *= 0xff51afd7ed558ccd
	x ^= x >> 33
	x *= 0xc4ceb9fe1a85ec53
	x ^= x >> 33
	return x
}
