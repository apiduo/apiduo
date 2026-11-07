package model

import (
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// taskKeyRetentionSeconds bounds how long a task keeps its own copy of the
// credential. Long enough for any queue an async provider realistically has,
// short enough that an edited or revoked key stops being replayed.
const taskKeyRetentionSeconds = 24 * 3600

// GetTaskKey resolves the credential a task was submitted with.
//
// The short-lived plaintext copy wins over the fingerprint lookup because it
// survives the one case the fingerprint cannot: the operator removed or replaced
// that key, so nothing in the channel points at it any more, yet the upstream
// account still owns the task.
//
// Disabled state is deliberately not consulted — a key can be flagged for
// billing reasons while still being the only credential that can see the task.
//
// APIDUO:multi-key-task-binding
func GetTaskKey(channel *Channel, task *Task) (string, int) {
	if task == nil {
		return "", -1
	}
	if key := strings.TrimSpace(task.PrivateData.UpstreamKey); key != "" {
		recordedAt := task.PrivateData.UpstreamKeyAt
		if recordedAt <= 0 || time.Now().Unix()-recordedAt <= taskKeyRetentionSeconds {
			return key, task.PrivateData.MultiKeyIndex
		}
	}
	return channel.GetKeyByFingerprint(task.PrivateData.MultiKeyIndex, task.PrivateData.MultiKeyFingerprint)
}

// MultiKeyFingerprint is the stable identity of a key: a hash, never the secret.
//
// The key list is edited by hand, so a position is not an identity. Inserting one
// line renumbers every key after it, and deleting one shifts them back. Anything
// that has to refer to "this key" later — a task that upstream scopes to the
// credential that created it — must record this instead of an index.
//
// APIDUO:multi-key-task-binding
// The length is encoded alongside the hash so a scan can reject a key with one
// integer comparison instead of hashing it. Without that prefix, resolving a
// task on a 3000-key channel costs a hash and a string allocation per key.
func MultiKeyFingerprint(key string) string {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return ""
	}
	buf := make([]byte, 0, 24)
	buf = strconv.AppendInt(buf, int64(len(trimmed)), 10)
	buf = append(buf, '-')
	buf = strconv.AppendUint(buf, stickyHash(trimmed), 16)
	return string(buf)
}

func parseMultiKeyFingerprint(value string) (keyLength int, hash uint64, ok bool) {
	dash := strings.IndexByte(value, '-')
	if dash <= 0 {
		return 0, 0, false
	}
	length, err := strconv.Atoi(value[:dash])
	if err != nil || length <= 0 {
		return 0, 0, false
	}
	hash, err = strconv.ParseUint(value[dash+1:], 16, 64)
	if err != nil {
		return 0, 0, false
	}
	return length, hash, true
}

func multiKeyMatches(key string, keyLength int, hash uint64) bool {
	trimmed := strings.TrimSpace(key)
	return len(trimmed) == keyLength && stickyHash(trimmed) == hash
}

// GetKeyByFingerprint resolves the key a task was submitted with.
//
// Async upstream APIs scope a task to the credential that created it: polling a
// task that key N created while authenticating with key M answers 404, because
// M's account cannot see it. Rotation must therefore not apply here, and neither
// may a bare index — both drift as soon as the key list is edited.
//
// `index` is a hint, not the identity: when it still points at a key with the
// same fingerprint the lookup is O(1); otherwise the list is scanned. When the
// key is gone entirely (deleted, replaced, or disabled) it falls back to the
// first usable key so the caller still has a credential and can surface the
// upstream error honestly.
//
// It never advances the polling cursor: a status poll is an idempotent GET and
// must not consume a turn that a real generation request needs.
//
// APIDUO:multi-key-task-binding
func (channel *Channel) GetKeyByFingerprint(index int, fingerprint string) (string, int) {
	if channel == nil {
		return "", -1
	}
	if !channel.ChannelInfo.IsMultiKey {
		return channel.Key, 0
	}

	keys := channel.GetKeys()
	if len(keys) == 0 {
		return channel.Key, 0
	}

	statusList := channel.ChannelInfo.MultiKeyStatusList
	keyLength, hash, hasFingerprint := parseMultiKeyFingerprint(strings.TrimSpace(fingerprint))

	if hasFingerprint {
		if multiKeyUsable(keys, index, statusList) && multiKeyMatches(keys[index], keyLength, hash) {
			return keys[index], index
		}
		// Compare against the cached key hashes first: one integer compare per
		// key instead of a TrimSpace plus a hash. A hit is still verified against
		// the live key, because a stale plan must never resolve to a different
		// credential — it may only fail to find one.
		plan := stickyPlanFor(channel.Id, keys, statusList, channel.ActiveKeyLimit())
		for i, cached := range plan.hashes {
			if cached != hash {
				continue
			}
			if !multiKeyUsable(keys, i, statusList) {
				continue
			}
			if multiKeyMatches(keys[i], keyLength, hash) {
				return keys[i], i
			}
		}
		// The plan only covers untrimmed key text; fall back to hashing directly
		// for keys that carry surrounding whitespace.
		for i := range keys {
			if !multiKeyUsable(keys, i, statusList) {
				continue
			}
			if multiKeyMatches(keys[i], keyLength, hash) {
				return keys[i], i
			}
		}
	}

	for i := range keys {
		if multiKeyUsable(keys, i, statusList) {
			return keys[i], i
		}
	}
	// Every key is disabled or blank: hand back the raw field so the caller
	// fails with an upstream error it can report, not an empty header.
	return channel.Key, 0
}

func multiKeyUsable(keys []string, index int, statusList map[int]int) bool {
	if index < 0 || index >= len(keys) {
		return false
	}
	if strings.TrimSpace(keys[index]) == "" {
		return false
	}
	if statusList != nil {
		if status, ok := statusList[index]; ok && status != common.ChannelStatusEnabled {
			return false
		}
	}
	return true
}
