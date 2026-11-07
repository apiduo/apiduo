package model

import "sort"

// The active-set cap for multi-key channels.
//
// A channel can hold far more keys than it wants spending at once: a hundred
// imported credentials with only ten serving is a legitimate setup. The cap is
// expressed positionally — at most MinActiveKeys *available* keys serve, taken
// in list order — instead of giving the surplus keys a state of their own.
//
// That choice buys three things. A disabled key hands its slot to the next
// available key with no bookkeeping at all, because the available list simply
// shifts; every key's status keeps meaning exactly what it meant before, so no
// caller, counter, filter or badge has to learn a fourth value; and there is no
// rebalance pass to run on save, on auto-disable or on a manual toggle, which
// removes five hooks from files upstream rewrites.
//
// The cost is that the operator picks the active set by ordering the list, not
// by toggling individual keys. Everything beyond the cap stays enabled and is a
// spare: it is never selected, and it enters rotation on its own the moment the
// available count drops below the cap.

// ActiveKeyLimit returns how many keys may serve at once, or 0 for no cap.
func (channel *Channel) ActiveKeyLimit() int {
	if channel == nil || !channel.ChannelInfo.IsMultiKey {
		return 0
	}
	if channel.ChannelInfo.MinActiveKeys <= 0 {
		return 0
	}
	return channel.ChannelInfo.MinActiveKeys
}

// limitActive trims an ordered index list to the active cap. The slice is
// returned as-is when the cap is absent, so the uncapped path allocates nothing.
func limitActive(indexes []int, limit int) []int {
	if limit > 0 && len(indexes) > limit {
		return indexes[:limit]
	}
	return indexes
}

// isActiveIndex reports whether a key index is inside the eligible set.
//
// The set is the single source of truth for "may this key serve": random picks
// from it, the sticky plan is built from it, and polling — which walks the raw
// key ring — has to ask it. Without this check polling ignored the cap
// completely, because its loop never looked at the eligible list at all.
func isActiveIndex(eligible []int, index int) bool {
	position := sort.SearchInts(eligible, index)
	return position < len(eligible) && eligible[position] == index
}

// nextActiveIndex returns the next key that may serve, wrapping around.
//
// The polling cursor used to step by one raw key index. Under a cap that puts it
// outside the window — after the tenth key it points at the first spare, and the
// scan then walks every remaining key before wrapping back. Pointing the cursor
// at the next eligible key keeps the scan at one iteration and keeps its meaning
// honest: "the key that serves next", not "the next array slot".
func nextActiveIndex(eligible []int, index int) int {
	if len(eligible) == 0 {
		return index
	}
	position := sort.SearchInts(eligible, index)
	if position < len(eligible) && eligible[position] == index {
		return eligible[(position+1)%len(eligible)]
	}
	if position >= len(eligible) {
		return eligible[0]
	}
	return eligible[position]
}
