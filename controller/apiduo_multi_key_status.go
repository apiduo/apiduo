package controller

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// multiKeyPreview renders the console label for one key: a recognizable head
// plus its tail.
//
// The head alone cannot separate two entries: every JWT starts with the same
// `eyJhbGciOi…` header, so a "first 10 chars" preview shows the same text for
// every key of a JWT-backed channel. Operators identify a bad key from the tail
// — that is what upstream 401s quote, and what the previous investigation used
// to tell tok1 from tok2. Six characters is enough to disambiguate a handful of
// keys and far too little to reconstruct one.
//
// Keys shorter than head+tail get a narrower window rather than nothing: a
// self-hosted service key is `sk-` plus its subdomain, eight to twelve
// characters, so the 10+6 rule matched none of them and every row of the panel
// read `#1 #2 #3` — the exact choice this label exists to make. The window
// still hides at least a third of the key (and never more than four characters
// per side), so the row stays distinguishable without being reproducible.
func multiKeyPreview(key string) string {
	const head, tail = 10, 6
	runes := []rune(strings.TrimSpace(key))
	n := len(runes)
	if n == 0 {
		return ""
	}
	headLen, tailLen := head, tail
	if n <= head+tail {
		if n <= 2 {
			// Too short to mask anything and still name the key.
			return "…"
		}
		hidden := max(2, n/3)
		headLen = min(4, (n-hidden)/2)
		tailLen = min(4, n-hidden-headLen)
	}
	return string(runes[:headLen]) + "…" + string(runes[n-tailLen:])
}

// multiKeyKeyState is one key's disabled state, carried by identity rather than
// by position so it can survive an edit of the key list.
type multiKeyKeyState struct {
	status int
	reason string
	at     int64
}

// remapMultiKeyStatusAfterKeyEdit reattaches disabled markers to the keys they
// were recorded for.
//
// MultiKeyStatusList / MultiKeyDisabledReason / MultiKeyDisabledTime are keyed
// by position in the key list. Editing that list — reordering it, inserting a
// line, or pasting a replacement set — renumbers everything after the edit, so a
// marker left untouched in the stored maps silently jumps to whichever key now
// occupies the old slot. The operator then sees key B disabled after disabling
// key A, and the genuinely broken key keeps serving.
//
// Identity is the fingerprint, the same one task polling resolves keys with
// (model.MultiKeyFingerprint): a length prefix plus a hash, never the secret. A
// marker whose key is no longer present is dropped, because it cannot be
// attached to any line the operator can see.
func remapMultiKeyStatusAfterKeyEdit(origin, updated *model.Channel) {
	if origin == nil || updated == nil || !updated.ChannelInfo.IsMultiKey {
		return
	}
	// An empty request key means "keep the stored key" (GORM skips zero values),
	// and an unchanged string cannot have moved any marker.
	if updated.Key == "" || updated.Key == origin.Key {
		return
	}
	if len(origin.ChannelInfo.MultiKeyStatusList) == 0 {
		return
	}

	oldKeys := origin.GetKeys()
	byFingerprint := make(map[string]multiKeyKeyState, len(oldKeys))
	for i, key := range oldKeys {
		status, ok := origin.ChannelInfo.MultiKeyStatusList[i]
		if !ok || status == common.ChannelStatusEnabled {
			continue
		}
		fingerprint := model.MultiKeyFingerprint(key)
		if fingerprint == "" {
			continue
		}
		if _, duplicate := byFingerprint[fingerprint]; duplicate {
			// Two identical lines describe one credential; keep the first marker.
			continue
		}
		state := multiKeyKeyState{status: status}
		if origin.ChannelInfo.MultiKeyDisabledReason != nil {
			state.reason = origin.ChannelInfo.MultiKeyDisabledReason[i]
		}
		if origin.ChannelInfo.MultiKeyDisabledTime != nil {
			state.at = origin.ChannelInfo.MultiKeyDisabledTime[i]
		}
		byFingerprint[fingerprint] = state
	}
	if len(byFingerprint) == 0 {
		return
	}

	newKeys := updated.GetKeys()
	statusList := make(map[int]int)
	reasonList := make(map[int]string)
	timeList := make(map[int]int64)
	for i, key := range newKeys {
		state, ok := byFingerprint[model.MultiKeyFingerprint(key)]
		if !ok {
			continue
		}
		statusList[i] = state.status
		if state.reason != "" {
			reasonList[i] = state.reason
		}
		if state.at > 0 {
			timeList[i] = state.at
		}
	}

	// Overwrite all three maps together: leaving a stale companion map behind
	// would pair a new index with another key's reason.
	updated.ChannelInfo.MultiKeyStatusList = nil
	updated.ChannelInfo.MultiKeyDisabledReason = nil
	updated.ChannelInfo.MultiKeyDisabledTime = nil
	if len(statusList) > 0 {
		updated.ChannelInfo.MultiKeyStatusList = statusList
	}
	if len(reasonList) > 0 {
		updated.ChannelInfo.MultiKeyDisabledReason = reasonList
	}
	if len(timeList) > 0 {
		updated.ChannelInfo.MultiKeyDisabledTime = timeList
	}
}
