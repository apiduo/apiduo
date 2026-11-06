package service

import (
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// Failure tracking for storage targets.
//
// A fallback chain is only useful if a dead target is cheap to skip. Without
// this, every read against a three-target chain pays the failing target's
// timeout before reaching a healthy one, which turns one broken bucket into a
// gateway-wide slowdown. The rule here is deliberately blunt: after a failure
// the target is skipped for a short, doubling window, and the first request
// after the window probes it again.
//
// "Object not found" is not a failure and never reaches this file - it means
// the artifact lives in another target, which is the normal case for a chain.

const (
	artifactTargetFailureBaseDelay = 15 * time.Second
	artifactTargetFailureMaxDelay  = 5 * time.Minute
	// artifactTargetFailureMaxEntries bounds the map. Targets are configured
	// objects, not request-derived, so hitting this means the operator is
	// churning credentials; dropping everything is cheaper than tracking it.
	artifactTargetFailureMaxEntries = 256
)

type artifactTargetFailure struct {
	failures int
	until    time.Time
}

var artifactTargetHealth = struct {
	mu       sync.Mutex
	failures map[blobBackendKey]*artifactTargetFailure
}{failures: make(map[blobBackendKey]*artifactTargetFailure, 4)}

// artifactTargetCoolingDown reports whether the target should be skipped.
func artifactTargetCoolingDown(key blobBackendKey) bool {
	artifactTargetHealth.mu.Lock()
	defer artifactTargetHealth.mu.Unlock()
	record, ok := artifactTargetHealth.failures[key]
	if !ok {
		return false
	}
	return time.Now().Before(record.until)
}

// recordArtifactTargetFailure extends the cooldown window.
//
// It returns true only for the first failure in a streak, which is what the
// caller uses to decide whether the event deserves a log line: a target that
// stays down must not flood the log on every request.
func recordArtifactTargetFailure(key blobBackendKey) bool {
	artifactTargetHealth.mu.Lock()
	defer artifactTargetHealth.mu.Unlock()

	if len(artifactTargetHealth.failures) >= artifactTargetFailureMaxEntries {
		clear(artifactTargetHealth.failures)
	}

	record, ok := artifactTargetHealth.failures[key]
	if !ok {
		record = &artifactTargetFailure{}
		artifactTargetHealth.failures[key] = record
	}
	record.failures++
	delay := artifactTargetFailureBaseDelay
	for i := 1; i < record.failures && delay < artifactTargetFailureMaxDelay; i++ {
		delay *= 2
	}
	if delay > artifactTargetFailureMaxDelay {
		delay = artifactTargetFailureMaxDelay
	}
	record.until = time.Now().Add(delay)
	return record.failures == 1
}

// clearArtifactTargetFailure forgets the streak after a success, so the next
// outage starts from the short window again.
func clearArtifactTargetFailure(key blobBackendKey) {
	artifactTargetHealth.mu.Lock()
	defer artifactTargetHealth.mu.Unlock()
	delete(artifactTargetHealth.failures, key)
}

// artifactTargetFailureCount is used by tests to assert the cooldown lifecycle.
func artifactTargetFailureCount(key blobBackendKey) int {
	artifactTargetHealth.mu.Lock()
	defer artifactTargetHealth.mu.Unlock()
	if record, ok := artifactTargetHealth.failures[key]; ok {
		return record.failures
	}
	return 0
}

func logArtifactTargetFailure(key blobBackendKey, err error) {
	// Logged once per streak; the cooldown keeps the next requests quiet.
	if recordArtifactTargetFailure(key) {
		common.SysError(common.LogText("artifact storage target failed, cooling down: endpoint=%s bucket=%s error=%v",
			key.endpoint, key.bucket, err))
	}
}
