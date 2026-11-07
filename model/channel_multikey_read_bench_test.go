package model

import (
	"strconv"
	"strings"
	"testing"
)

// Production channels arrive from the cache with Keys already parsed
// (channel_cache.go fills it), so the benchmark must too — otherwise every
// GetKeys call would split a 49KB string and dominate the measurement.
func fingerprintBenchChannel(keys []string) *Channel {
	channel := fingerprintChannel(keys)
	channel.Keys = keys
	return channel
}

func fingerprintBenchKeys(keyCount int) []string {
	keys := make([]string, keyCount)
	for i := range keys {
		// Same shape and length as a real provider key, so the length prefix in
		// the fingerprint cannot shortcut the scan — this is the worst case.
		keys[i] = "sk-" + strings.Repeat("a", 20) + strconv.Itoa(100000+i)
	}
	return keys
}

func BenchmarkMultiKeyFingerprint(b *testing.B) {
	key := "sk-aaaaaaaaaaaaaaaaaaaa100001"
	b.ReportAllocs()
	for b.Loop() {
		_ = MultiKeyFingerprint(key)
	}
}

func BenchmarkGetKeyByFingerprintFastPath(b *testing.B) {
	keys := fingerprintBenchKeys(100)
	channel := fingerprintBenchChannel(keys)
	fingerprint := MultiKeyFingerprint(keys[50])
	b.ReportAllocs()
	for b.Loop() {
		_, _ = channel.GetKeyByFingerprint(50, fingerprint)
	}
}

func BenchmarkGetKeyByFingerprintScan100(b *testing.B) {
	keys := fingerprintBenchKeys(100)
	channel := fingerprintBenchChannel(keys)
	// Index hint is wrong on purpose: forces the scan.
	fingerprint := MultiKeyFingerprint(keys[99])
	b.ReportAllocs()
	for b.Loop() {
		_, _ = channel.GetKeyByFingerprint(0, fingerprint)
	}
}

func BenchmarkGetKeyByFingerprintScan3000(b *testing.B) {
	keys := fingerprintBenchKeys(3000)
	channel := fingerprintBenchChannel(keys)
	fingerprint := MultiKeyFingerprint(keys[2999])
	b.ReportAllocs()
	for b.Loop() {
		_, _ = channel.GetKeyByFingerprint(0, fingerprint)
	}
}

func BenchmarkGetKeyByFingerprintScan10000(b *testing.B) {
	keys := fingerprintBenchKeys(10000)
	channel := fingerprintBenchChannel(keys)
	fingerprint := MultiKeyFingerprint(keys[9999])
	b.ReportAllocs()
	for b.Loop() {
		_, _ = channel.GetKeyByFingerprint(0, fingerprint)
	}
}

func BenchmarkGetKeyByFingerprintLegacyNoFingerprint3000(b *testing.B) {
	keys := fingerprintBenchKeys(3000)
	channel := fingerprintBenchChannel(keys)
	b.ReportAllocs()
	for b.Loop() {
		_, _ = channel.GetKeyByFingerprint(0, "")
	}
}

func BenchmarkGetKeyByFingerprintMissing3000(b *testing.B) {
	keys := fingerprintBenchKeys(3000)
	channel := fingerprintBenchChannel(keys)
	// A key that is not in the list: worst case, the full scan finds nothing.
	fingerprint := MultiKeyFingerprint("sk-does-not-exist-at-all")
	b.ReportAllocs()
	for b.Loop() {
		_, _ = channel.GetKeyByFingerprint(0, fingerprint)
	}
}

func BenchmarkGetTaskKeyRetained(b *testing.B) {
	keys := fingerprintBenchKeys(100)
	channel := fingerprintBenchChannel(keys)
	task := &Task{}
	task.PrivateData.UpstreamKey = keys[42]
	task.PrivateData.UpstreamKeyAt = 1 << 40
	task.PrivateData.MultiKeyIndex = 42
	b.ReportAllocs()
	for b.Loop() {
		_, _ = GetTaskKey(channel, task)
	}
}

func BenchmarkGetTaskKeyFingerprintFallback(b *testing.B) {
	keys := fingerprintBenchKeys(3000)
	channel := fingerprintBenchChannel(keys)
	task := &Task{}
	task.PrivateData.MultiKeyFingerprint = MultiKeyFingerprint(keys[2999])
	task.PrivateData.MultiKeyIndex = 0
	b.ReportAllocs()
	for b.Loop() {
		_, _ = GetTaskKey(channel, task)
	}
}
