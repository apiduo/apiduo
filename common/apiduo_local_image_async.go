package common

import (
	"os"
	"sync"
)

// APIDUO:local-image-async BEGIN

var (
	localImageAsyncOnce sync.Once
	localImageAsyncOn   bool
)

// LocalImageAsyncEnabled reports whether the gateway may execute a synchronous
// image upstream itself.
//
// Off by default. Turning it on lets an asynchronous endpoint select a
// synchronous channel: the submit only stores the request, and a worker replays
// it later. That costs a channel slot for the whole generation and keeps the
// request body in the task row, so it is a capability an operator opts into
// rather than a behavior that appears on upgrade.
//
// Read once: every dispatch decision in a process must agree, and an env read on
// the hot path would be a measurable cost for a value that never changes.
func LocalImageAsyncEnabled() bool {
	localImageAsyncOnce.Do(func() {
		localImageAsyncOn = os.Getenv("LOCAL_IMAGE_ASYNC_ENABLED") == "true"
	})
	return localImageAsyncOn
}

// APIDUO:local-image-async END
